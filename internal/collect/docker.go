package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// dockerClient talks to the Docker Engine API over its unix socket. Thin on
// purpose — a couple of GETs, no SDK.
type dockerClient struct {
	http *http.Client
}

func newDockerClient(sock string) *dockerClient {
	return &dockerClient{
		http: &http.Client{
			Timeout: 5 * time.Second,
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

func (d *dockerClient) snapshot() *DockerBlock {
	var list []struct {
		ID     string   `json:"Id"`
		Names  []string `json:"Names"`
		State  string   `json:"State"`
		Status string   `json:"Status"`
	}
	if err := d.get("/containers/json", &list); err != nil {
		return &DockerBlock{}
	}

	b := &DockerBlock{Running: len(list)}
	for _, c := range list {
		name := c.ID[:12]
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		ct := Container{ID: c.ID[:12], Name: name, Status: c.Status}
		d.fillStats(c.ID, &ct)
		d.fillInspect(c.ID, &ct)
		b.Containers = append(b.Containers, ct)
	}
	return b
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

	cpuDelta := float64(s.CPU.Usage.Total - s.PreCPU.Usage.Total)
	sysDelta := float64(s.CPU.Usage.System - s.PreCPU.Usage.System)
	if sysDelta > 0 && cpuDelta > 0 {
		n := float64(s.CPU.OnlineCPUs)
		if n == 0 {
			n = 1
		}
		ct.CPUPct = round2(cpuDelta / sysDelta * n * 100)
	}

	used := s.Mem.Usage
	if s.Mem.Stats.InactiveFile > 0 {
		used -= s.Mem.Stats.InactiveFile
	} else if s.Mem.Stats.Cache > 0 && s.Mem.Stats.Cache < used {
		used -= s.Mem.Stats.Cache
	}
	ct.MemUsed = used
	ct.MemLimit = s.Mem.Limit
	ct.PIDs = s.PidsStats.Current
	for _, nw := range s.Networks {
		ct.NetRxBps += float64(nw.RxBytes)
		ct.NetTxBps += float64(nw.TxBytes)
	}
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
	ct.RestartCount = ins.RestartCount
	ct.OOMKilled = ins.State.OOMKilled
	if ins.State.Health != nil {
		ct.Health = ins.State.Health.Status
	}
}
