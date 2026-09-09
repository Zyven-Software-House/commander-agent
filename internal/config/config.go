// Package config holds the agent's runtime settings: the immutable bits it is
// installed with (API URL, token, paths) and the mutable block the server
// pushes down and the agent persists so it survives restarts / API outages.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Static is set once at startup from flags / env / the install file.
type Static struct {
	APIBaseURL string // e.g. https://ytox3asfh06f72kv-d.commander.zyven.io/api
	Token      string // cma_...
	ConfigPath string // where the pushed Block is persisted
	DockerSock string // /var/run/docker.sock ("" disables docker collection)
	RootMounts []string
}

// Threshold is a warn/crit pair; either may be zero (meaning "not set").
type Threshold struct {
	Warn float64 `json:"warn,omitempty"`
	Crit float64 `json:"crit,omitempty"`
}

// Block is the server-controlled config. Shape matches AgentConfig::default() on
// the API side.
type Block struct {
	Version   int  `json:"configVersion"`
	Intervals struct {
		Live       int `json:"live"`
		Background int `json:"background"`
		Heartbeat  int `json:"heartbeat"`
	} `json:"intervals"`
	Collect struct {
		CPU     bool `json:"cpu"`
		Mem     bool `json:"mem"`
		Disk    bool `json:"disk"`
		Net     bool `json:"net"`
		Docker  bool `json:"docker"`
		Systemd bool `json:"systemd"`
	} `json:"collect"`
	Alerts map[string]Threshold `json:"alerts"`
}

// Default mirrors the API's AgentConfig::default() so a brand-new agent behaves
// sensibly before its first server round-trip.
func Default() Block {
	var b Block
	b.Version = 0
	b.Intervals.Live = 3
	b.Intervals.Background = 60
	b.Intervals.Heartbeat = 15
	b.Collect.CPU = true
	b.Collect.Mem = true
	b.Collect.Disk = true
	b.Collect.Net = true
	b.Collect.Docker = true
	b.Collect.Systemd = true
	b.Alerts = map[string]Threshold{
		"diskUsedPct":     {Warn: 85, Crit: 92},
		"inodeUsedPct":    {Warn: 80, Crit: 90},
		"memAvailablePct": {Warn: 10, Crit: 4},
		"swapUsedPct":     {Warn: 60},
		"swapIoBps":       {Crit: 10_485_760},
		"loadPerCore":     {Warn: 2, Crit: 4},
		"cpuStealPct":     {Warn: 15},
		"diskAwaitMs":     {Warn: 100},
	}
	return b
}

// Store guards the current Block and persists it.
type Store struct {
	mu    sync.RWMutex
	block Block
	path  string
}

func Load(path string) *Store {
	s := &Store{path: path, block: Default()}
	if raw, err := os.ReadFile(path); err == nil {
		var b Block
		if json.Unmarshal(raw, &b) == nil && b.Version > 0 {
			s.block = b
		}
	}
	return s
}

func (s *Store) Get() Block {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.block
}

// Apply replaces the block (from a server response) and persists it. No-op if
// the incoming version is not newer.
func (s *Store) Apply(b Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if b.Version <= s.block.Version {
		return
	}
	s.block = b
	if s.path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(s.path), 0o755)
	if raw, err := json.MarshalIndent(b, "", "  "); err == nil {
		tmp := s.path + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, s.path)
		}
	}
}
