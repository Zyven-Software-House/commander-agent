// Package alerts turns a Sample + thresholds into the alert conditions the API's
// AlertEvaluator understands. Sustained-window conditions are held here so a
// single spike doesn't fire.
package alerts

import (
	"time"

	"github.com/Zyven-Software-House/commander-agent/internal/collect"
	"github.com/Zyven-Software-House/commander-agent/internal/config"
)

type Alert struct {
	Condition string         `json:"condition"`
	Severity  string         `json:"severity"` // warning | critical
	Scope     string         `json:"scope,omitempty"`
	Context   map[string]any `json:"context,omitempty"`
}

type Evaluator struct {
	cores      int
	oomBaseline uint64
	sustained  map[string]time.Time    // condition|scope -> first-seen
	restarts   map[string]int          // container -> last restartCount
}

func New(cores int) *Evaluator {
	if cores < 1 {
		cores = 1
	}
	return &Evaluator{cores: cores, sustained: map[string]time.Time{}, restarts: map[string]int{}}
}

const (
	crit = "critical"
	warn = "warning"
)

func (e *Evaluator) Evaluate(s collect.Sample, cfg config.Block) []Alert {
	now := time.Now()
	var out []Alert
	th := cfg.Alerts

	held := func(key string, d time.Duration) bool {
		first, ok := e.sustained[key]
		if !ok {
			e.sustained[key] = now
			return false
		}
		return now.Sub(first) >= d
	}
	clearHold := func(key string) { delete(e.sustained, key) }

	// ---- disk usage / inodes (per mount) ----
	if s.Disk != nil {
		fs := s.Disk.Root
		if t, ok := th["diskUsedPct"]; ok {
			switch {
			case t.Crit > 0 && fs.UsedPct >= t.Crit:
				out = append(out, Alert{"disk_full", crit, fs.Mount, ctx(fs.UsedPct, t.Crit)})
			case t.Warn > 0 && fs.UsedPct >= t.Warn:
				out = append(out, Alert{"disk_full", warn, fs.Mount, ctx(fs.UsedPct, t.Warn)})
			}
		}
		if t, ok := th["inodeUsedPct"]; ok && fs.InodeUsedPct > 0 {
			switch {
			case t.Crit > 0 && fs.InodeUsedPct >= t.Crit:
				out = append(out, Alert{"inode_full", crit, fs.Mount, ctx(fs.InodeUsedPct, t.Crit)})
			case t.Warn > 0 && fs.InodeUsedPct >= t.Warn:
				out = append(out, Alert{"inode_full", warn, fs.Mount, ctx(fs.InodeUsedPct, t.Warn)})
			}
		}
		if t, ok := th["diskAwaitMs"]; ok && t.Warn > 0 && s.Disk.IO.AwaitMs >= t.Warn {
			if held("disk_latency", 5*time.Minute) {
				out = append(out, Alert{"disk_latency", warn, "", ctx(s.Disk.IO.AwaitMs, t.Warn)})
			}
		} else {
			clearHold("disk_latency")
		}
	}

	// ---- memory pressure ----
	if s.Mem != nil && s.Mem.Total > 0 {
		availPct := float64(s.Mem.Available) / float64(s.Mem.Total) * 100
		swapActive := s.Swap != nil && s.Swap.IOBps > 0
		if t, ok := th["memAvailablePct"]; ok {
			if t.Crit > 0 && availPct <= t.Crit && swapActive {
				out = append(out, Alert{"mem_pressure", crit, "", ctx(round1(availPct), t.Crit)})
			} else if t.Warn > 0 && availPct <= t.Warn {
				out = append(out, Alert{"mem_pressure", warn, "", ctx(round1(availPct), t.Warn)})
			}
		}
	}

	// ---- swap thrash ----
	if s.Swap != nil {
		if t, ok := th["swapIoBps"]; ok && t.Crit > 0 && s.Swap.IOBps >= t.Crit {
			if held("swap_thrash", time.Minute) {
				out = append(out, Alert{"swap_thrash", crit, "", ctx(s.Swap.IOBps, t.Crit)})
			}
		} else {
			clearHold("swap_thrash")
		}
		if t, ok := th["swapUsedPct"]; ok && t.Warn > 0 && s.Swap.Total > 0 {
			usedPct := float64(s.Swap.Used) / float64(s.Swap.Total) * 100
			if usedPct >= t.Warn {
				out = append(out, Alert{"swap_thrash", warn, "used", ctx(round1(usedPct), t.Warn)})
			}
		}
	}

	// ---- oom kills ----
	if s.Host != nil {
		if e.oomBaseline == 0 {
			e.oomBaseline = s.Host.OOMKillCount
		} else if s.Host.OOMKillCount > e.oomBaseline {
			out = append(out, Alert{"oom_kill", crit, "", map[string]any{"value": s.Host.OOMKillCount}})
			e.oomBaseline = s.Host.OOMKillCount
		}
	}

	// ---- cpu steal ----
	if s.CPU != nil {
		if t, ok := th["cpuStealPct"]; ok && t.Warn > 0 && s.CPU.Steal >= t.Warn {
			if held("cpu_steal", 5*time.Minute) {
				out = append(out, Alert{"cpu_steal", warn, "", ctx(s.CPU.Steal, t.Warn)})
			}
		} else {
			clearHold("cpu_steal")
		}
	}

	// ---- load ----
	if s.Load != nil {
		if t, ok := th["loadPerCore"]; ok {
			perCore := s.Load.One / float64(e.cores)
			if t.Crit > 0 && perCore >= t.Crit && held("load_crit", 5*time.Minute) {
				out = append(out, Alert{"load_high", crit, "", ctx(round1(s.Load.One), round1(t.Crit*float64(e.cores)))})
			} else if t.Warn > 0 && s.Load.Five/float64(e.cores) >= t.Warn && held("load_warn", 10*time.Minute) {
				out = append(out, Alert{"load_high", warn, "", ctx(round1(s.Load.Five), round1(t.Warn*float64(e.cores)))})
			} else {
				clearHold("load_crit")
				clearHold("load_warn")
			}
		}
	}

	// ---- systemd ----
	if s.System != nil {
		for _, u := range s.System.FailedUnits {
			out = append(out, Alert{"systemd_failed", crit, u, nil})
		}
	}

	// ---- containers ----
	if s.Docker != nil {
		for _, c := range s.Docker.Containers {
			prev, seen := e.restarts[c.Name]
			e.restarts[c.Name] = c.RestartCount
			if seen && c.RestartCount-prev >= 3 {
				out = append(out, Alert{"container_restart_loop", crit, c.Name, map[string]any{"value": c.RestartCount}})
			}
			if c.OOMKilled {
				out = append(out, Alert{"container_oom", warn, c.Name, nil})
			}
			if c.Health == "unhealthy" && held("unhealthy|"+c.Name, 5*time.Minute) {
				out = append(out, Alert{"container_restart_loop", warn, c.Name, map[string]any{"message": "unhealthy > 5min"}})
			}
		}
	}

	return out
}

func ctx(value, threshold float64) map[string]any {
	return map[string]any{"value": value, "threshold": threshold}
}

func round1(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }
