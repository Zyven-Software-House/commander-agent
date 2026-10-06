package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// dockerClient talks to the Docker Engine API over its unix socket. Thin on
// purpose — a couple of GETs, no SDK.
type dockerClient struct {
	http *http.Client
}

// maxConcurrentStats bounds how many /stats+/json calls run at once for watched containers, so even a
// long watch list can't open dozens of simultaneous connections to the Docker daemon.
const maxConcurrentStats = 8

func newDockerClient(sock string) *dockerClient {
	return &dockerClient{
		http: &http.Client{
			// Short on purpose: this must never be why a tick stalls. The list call is one cheap
			// request; watched-container stats run in parallel (maxConcurrentStats), so a slow/stuck
			// container just drops out of this tick's sample instead of blocking the others.
			Timeout: 3 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", sock)
				},
			},
		},
	}
}

func (d *dockerClient) get(path string, v any) error {
	resp, err := d.http.Get("http://unix" + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("docker %s: %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// snapshot lists every container with one cheap call (id/name/image/status — no stats, no inspect),
// then fills in the heavier per-container numbers only for the ones matching watch (see
// config.Block.DockerWatch), in parallel. With watch empty (the default), this is a single GET
// regardless of how many containers are running.
func (d *dockerClient) snapshot(watch []string) *DockerBlock {
	var list []struct {
		ID     string            `json:"Id"`
		Names  []string          `json:"Names"`
		Image  string            `json:"Image"`
		Status string            `json:"Status"`
		Labels map[string]string `json:"Labels"`
	}
	if err := d.get("/containers/json", &list); err != nil {
		return &DockerBlock{}
	}

	b := &DockerBlock{Running: len(list)}
	containers := make([]Container, len(list))
	fullIDs := make([]string, len(list))
	var toWatch []int

	for i, c := range list {
		name := c.ID[:12]
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		containers[i] = Container{ID: c.ID[:12], Name: name, Image: c.Image, Status: c.Status}
		fullIDs[i] = c.ID
		if matchesWatch(name, c.Labels, watch) {
			toWatch = append(toWatch, i)
		}
	}

	d.fillWatched(fullIDs, containers, toWatch)
	b.Containers = containers
	return b
}

// matchesWatch checks name against each pattern in watch: an exact match, a "prefix*" match, or a
// "label:key" / "label:key=value" match against the container's labels.
func matchesWatch(name string, labels map[string]string, watch []string) bool {
	for _, pattern := range watch {
		switch {
		case strings.HasPrefix(pattern, "label:"):
			kv := strings.TrimPrefix(pattern, "label:")
			key, want, hasValue := strings.Cut(kv, "=")
			if got, ok := labels[key]; ok && (!hasValue || got == want) {
				return true
			}
		case strings.HasSuffix(pattern, "*"):
			if strings.HasPrefix(name, strings.TrimSuffix(pattern, "*")) {
				return true
			}
		case pattern == name:
			return true
		}
	}
	return false
}

func (d *dockerClient) fillWatched(fullIDs []string, out []Container, indices []int) {
	if len(indices) == 0 {
		return
	}

	sem := make(chan struct{}, maxConcurrentStats)
	var wg sync.WaitGroup

	for _, i := range indices {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			d.fillStats(fullIDs[i], &out[i])
			d.fillInspect(fullIDs[i], &out[i])
		}(i)
	}

	wg.Wait()
}

func (d *dockerClient) fillStats(id string, ct *Container) {
	var s struct {
		CPU struct {
			Usage struct {
				Total  uint64 `json:"total_usage"`
				System uint64 `json:"system_cpu_usage"`
			} `json:"cpu_usage"`
			OnlineCPUs uint64 `json:"online_cpus"`
		} `json:"cpu_stats"`
		PreCPU struct {
			Usage struct {
				Total  uint64 `json:"total_usage"`
				System uint64 `json:"system_cpu_usage"`
			} `json:"cpu_usage"`
		} `json:"precpu_stats"`
		Mem struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
			Stats struct {
				Cache        uint64 `json:"cache"`
				InactiveFile uint64 `json:"inactive_file"`
			} `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			RxBytes uint64 `json:"rx_bytes"`
			TxBytes uint64 `json:"tx_bytes"`
		} `json:"networks"`
		PidsStats struct {
			Current uint64 `json:"current"`
		} `json:"pids_stats"`
	}
	if err := d.get("/containers/"+id+"/stats?stream=false", &s); err != nil {
		return
	}

	if sysDelta := float64(s.CPU.Usage.System - s.PreCPU.Usage.System); sysDelta > 0 {
		if cpuDelta := float64(s.CPU.Usage.Total - s.PreCPU.Usage.Total); cpuDelta > 0 {
			n := float64(s.CPU.OnlineCPUs)
			if n == 0 {
				n = 1
			}
			pct := round2(cpuDelta / sysDelta * n * 100)
			ct.CPUPct = &pct
		}
	}

	used := s.Mem.Usage
	if s.Mem.Stats.InactiveFile > 0 {
		used -= s.Mem.Stats.InactiveFile
	} else if s.Mem.Stats.Cache > 0 && s.Mem.Stats.Cache < used {
		used -= s.Mem.Stats.Cache
	}
	ct.MemUsed = &used
	ct.MemLimit = &s.Mem.Limit
	ct.PIDs = &s.PidsStats.Current

	var rx, tx float64
	for _, nw := range s.Networks {
		rx += float64(nw.RxBytes)
		tx += float64(nw.TxBytes)
	}
	ct.NetRxBps = &rx
	ct.NetTxBps = &tx
}

func (d *dockerClient) fillInspect(id string, ct *Container) {
	var ins struct {
		RestartCount int `json:"RestartCount"`
		State        struct {
			OOMKilled bool `json:"OOMKilled"`
			Health    *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := d.get("/containers/"+id+"/json", &ins); err != nil {
		return
	}
	ct.RestartCount = &ins.RestartCount
	ct.OOMKilled = ins.State.OOMKilled
	if ins.State.Health != nil {
		ct.Health = ins.State.Health.Status
	}
}
