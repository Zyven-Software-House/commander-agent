// Package collect turns the host state into a Sample. It leans on gopsutil for
// the portable metrics and reads a few things straight from /proc that gopsutil
// doesn't expose cleanly (swap-io rate, oom counter, boot id).
package collect

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/Zyven-Software-House/commander-agent/internal/config"
)

type Collector struct {
	mounts     []string
	dockerSock string
	docker     *dockerClient

	// deltas
	lastAt      time.Time
	lastSwapIn  uint64
	lastSwapOut uint64
	lastNetRx   uint64
	lastNetTx   uint64
	lastNetIf   string

	prevTotal, prevSteal, prevIowait     float64
	lastReadB, lastWriteB, lastTicks, lastOps uint64

	// slow (docker) cache
	lastDocker   *DockerBlock
	lastDockerAt time.Time
}

func New(st config.Static) *Collector {
	mounts := st.RootMounts
	if len(mounts) == 0 {
		mounts = []string{"/"}
	}
	c := &Collector{mounts: mounts, dockerSock: st.DockerSock}
	if st.DockerSock != "" {
		c.docker = newDockerClient(st.DockerSock)
	}
	return c
}

// Prime does the first read so the next Collect has deltas to work with.
func (c *Collector) Prime() {
	_, _ = cpu.Percent(0, false)
	_, _ = cpu.Percent(0, true)
	_, _ = cpu.Times(false)
	_, _ = disk.IOCounters()
	c.lastAt = time.Now()
	c.lastSwapIn, c.lastSwapOut = readVmstatSwap()
	c.lastNetIf = defaultIface()
	c.lastNetRx, c.lastNetTx = ifaceCounters(c.lastNetIf)
}

func (c *Collector) Collect(b config.Block) Sample {
	now := time.Now()
	elapsed := now.Sub(c.lastAt).Seconds()
	if elapsed <= 0 {
		elapsed = 1
	}
	s := Sample{At: now.UTC().Format(time.RFC3339)}

	if b.Collect.CPU {
		s.CPU = c.collectCPU()
		s.Load = collectLoad()
	}
	if b.Collect.Mem {
		s.Mem, s.Swap = c.collectMem(elapsed)
	}
	if b.Collect.Disk {
		s.Disk = c.collectDisk(elapsed)
	}
	if b.Collect.Net {
		s.Net = c.collectNet(elapsed)
	}
	s.Host = collectHost()

	if b.Collect.Systemd {
		s.System = &System{FailedUnits: failedUnits()}
	}
	if b.Collect.Docker && c.docker != nil {
		s.Docker = c.collectDocker(now)
	}

	c.lastAt = now
	return s
}

/* ------------------------------------------------------------------ cpu */

func (c *Collector) collectCPU() *CPU {
	agg, err := cpu.Percent(0, false)
	if err != nil || len(agg) == 0 {
		return nil
	}
	out := &CPU{Pct: round2(agg[0])}
	if per, err := cpu.Percent(0, true); err == nil {
		out.PerCore = make([]float64, len(per))
		for i, v := range per {
			out.PerCore[i] = round2(v)
		}
	}
	if times, err := cpu.Times(false); err == nil && len(times) == 1 {
		t := times[0]
		total := t.User + t.System + t.Idle + t.Nice + t.Iowait + t.Irq + t.Softirq + t.Steal
		if c.prevTotal > 0 && total > c.prevTotal {
			d := total - c.prevTotal
			out.Steal = round2((t.Steal - c.prevSteal) / d * 100)
			out.Iowait = round2((t.Iowait - c.prevIowait) / d * 100)
		}
		c.prevTotal, c.prevSteal, c.prevIowait = total, t.Steal, t.Iowait
	}
	return out
}

func collectLoad() *Load {
	avg, err := load.Avg()
	if err != nil {
		return nil
	}
	return &Load{One: round2(avg.Load1), Five: round2(avg.Load5), Fifteen: round2(avg.Load15)}
}

/* ------------------------------------------------------------------ mem */

func (c *Collector) collectMem(elapsed float64) (*Mem, *Swap) {
	vm, err := mem.VirtualMemory()
	if err != nil {
		return nil, nil
	}
	sw, _ := mem.SwapMemory()

	in, outp := readVmstatSwap()
	var ioBps float64
	if c.lastSwapIn > 0 || c.lastSwapOut > 0 {
		pages := float64((in - c.lastSwapIn) + (outp - c.lastSwapOut))
		ioBps = round2(pages * 4096 / elapsed)
	}
	c.lastSwapIn, c.lastSwapOut = in, outp

	m := &Mem{Total: vm.Total, Available: vm.Available}
	var s *Swap
	if sw != nil {
		s = &Swap{Total: sw.Total, Used: sw.Used, IOBps: ioBps}
	} else {
		s = &Swap{IOBps: ioBps}
	}
	return m, s
}

/* ----------------------------------------------------------------- disk */

func (c *Collector) collectDisk(elapsed float64) *Disk {
	// pick the fullest of the configured mounts as "root"
	var root FS
	for _, m := range c.mounts {
		u, err := disk.Usage(m)
		if err != nil {
			continue
		}
		fs := FS{
			Mount:        m,
			Total:        u.Total,
			Used:         u.Used,
			UsedPct:      round2(u.UsedPercent),
			InodeUsedPct: round2(u.InodesUsedPercent),
		}
		if fs.UsedPct >= root.UsedPct {
			root = fs
		}
	}

	io := DiskIO{}
	if counters, err := disk.IOCounters(); err == nil {
		var rB, wB, ticks, ops uint64
		for name, ct := range counters {
			if isVirtualDisk(name) {
				continue
			}
			rB += ct.ReadBytes
			wB += ct.WriteBytes
			ticks += ct.ReadTime + ct.WriteTime
			ops += ct.ReadCount + ct.WriteCount
		}
		if c.lastReadB > 0 || c.lastWriteB > 0 {
			io.ReadBps = round2(float64(rB-c.lastReadB) / elapsed)
			io.WriteBps = round2(float64(wB-c.lastWriteB) / elapsed)
			if dops := ops - c.lastOps; dops > 0 {
				io.AwaitMs = round2(float64(ticks-c.lastTicks) / float64(dops))
			}
		}
		c.lastReadB, c.lastWriteB, c.lastTicks, c.lastOps = rB, wB, ticks, ops
	}

	return &Disk{Root: root, IO: io}
}

func isVirtualDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "dm-", "sr", "fd", "md"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

/* ------------------------------------------------------------------ net */

func (c *Collector) collectNet(elapsed float64) *Net {
	iface := c.lastNetIf
	if iface == "" {
		iface = defaultIface()
		c.lastNetIf = iface
	}
	rx, tx := ifaceCounters(iface)
	n := &Net{}
	if c.lastNetRx > 0 || c.lastNetTx > 0 {
		n.In.Bps = round2(float64(rx-c.lastNetRx) / elapsed)
		n.Out.Bps = round2(float64(tx-c.lastNetTx) / elapsed)
	}
	c.lastNetRx, c.lastNetTx = rx, tx
	return n
}

func ifaceCounters(iface string) (rx, tx uint64) {
	stats, err := net.IOCounters(true)
	if err != nil {
		return
	}
	for _, s := range stats {
		if s.Name == iface {
			return s.BytesRecv, s.BytesSent
		}
	}
	return
}

// defaultIface reads /proc/net/route for the interface owning the default route.
func defaultIface() string {
	f, err := os.Open("/proc/net/route")
	if err != nil {
		return "eth0"
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[1] == "00000000" {
			return fields[0]
		}
	}
	return "eth0"
}

/* ----------------------------------------------------------------- host */

func collectHost() *Host {
	h := &Host{BootID: bootID(), OOMKillCount: readVmstatKey("oom_kill")}
	if raw, err := os.ReadFile("/proc/stat"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(line, "procs_running ") {
				h.ProcsRunning, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "procs_running ")))
			}
		}
	}
	if raw, err := os.ReadFile("/proc/sys/fs/file-nr"); err == nil {
		f := strings.Fields(string(raw))
		if len(f) == 3 {
			used, _ := strconv.ParseFloat(f[0], 64)
			max, _ := strconv.ParseFloat(f[2], 64)
			if max > 0 {
				h.FDUsedPct = round2(used / max * 100)
			}
		}
	}
	return h
}

func bootID() string { return BootID() }

// BootID is the kernel's random boot id — changes on every reboot.
func BootID() string {
	raw, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw))
}

/* --------------------------------------------------------------- vmstat */

func readVmstatSwap() (in, out uint64) {
	return readVmstatKey("pswpin"), readVmstatKey("pswpout")
}

func readVmstatKey(key string) uint64 {
	f, err := os.Open("/proc/vmstat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 2 && fields[0] == key {
			v, _ := strconv.ParseUint(fields[1], 10, 64)
			return v
		}
	}
	return 0
}

/* -------------------------------------------------------------- systemd */

func failedUnits() []string {
	out, err := exec.Command("systemctl", "list-units", "--state=failed", "--no-legend", "--plain", "--no-pager").Output()
	if err != nil {
		return []string{}
	}
	units := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && strings.Contains(f[0], ".") { // unit names always have an extension
			units = append(units, f[0])
		}
	}
	return units
}

/* --------------------------------------------------------------- docker */

func (c *Collector) collectDocker(now time.Time) *DockerBlock {
	if c.lastDocker != nil && now.Sub(c.lastDockerAt) < 10*time.Second {
		return c.lastDocker
	}
	b := c.docker.snapshot()
	c.lastDocker, c.lastDockerAt = b, now
	return b
}

/* ---------------------------------------------------------------- utils */

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
