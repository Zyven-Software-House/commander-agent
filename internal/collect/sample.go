package collect

// Sample is one point in time. The JSON shape is the contract with the API's
// MetricStore::flatten() — keep the keys in sync.
type Sample struct {
	At     string       `json:"at"` // RFC3339
	CPU    *CPU         `json:"cpu,omitempty"`
	Load   *Load        `json:"load,omitempty"`
	Mem    *Mem         `json:"mem,omitempty"`
	Swap   *Swap        `json:"swap,omitempty"`
	Disk   *Disk        `json:"disk,omitempty"`
	Net    *Net         `json:"net,omitempty"`
	Host   *Host        `json:"host,omitempty"`
	System *System      `json:"systemd,omitempty"`
	Docker *DockerBlock `json:"docker,omitempty"`
}

type CPU struct {
	Pct     float64   `json:"pct"`
	Steal   float64   `json:"steal"`
	Iowait  float64   `json:"iowait"`
	PerCore []float64 `json:"perCore,omitempty"`
}

type Load struct {
	One     float64 `json:"1"`
	Five    float64 `json:"5"`
	Fifteen float64 `json:"15"`
}

type Mem struct {
	Total     uint64 `json:"total"`
	Available uint64 `json:"available"`
}

type Swap struct {
	Total uint64  `json:"total"`
	Used  uint64  `json:"used"`
	IOBps float64 `json:"ioBps"`
}

type Disk struct {
	Root FS     `json:"root"`
	IO   DiskIO `json:"io"`
}

type FS struct {
	Mount        string  `json:"mount"`
	Total        uint64  `json:"total"`
	Used         uint64  `json:"used"`
	UsedPct      float64 `json:"usedPct"`
	InodeUsedPct float64 `json:"inodeUsedPct"`
}

type DiskIO struct {
	ReadBps  float64 `json:"readBps"`
	WriteBps float64 `json:"writeBps"`
	AwaitMs  float64 `json:"awaitMs"`
}

type Net struct {
	In  NetDir `json:"in"`
	Out NetDir `json:"out"`
}

type NetDir struct {
	Bps float64 `json:"bps"`
}

type Host struct {
	ProcsRunning int    `json:"procsRunning"`
	FDUsedPct    float64 `json:"fdUsedPct"`
	BootID       string `json:"bootId,omitempty"`
	OOMKillCount uint64 `json:"oomKillCount"`
}

type System struct {
	FailedUnits []string `json:"failedUnits"`
}

type DockerBlock struct {
	Running    int         `json:"running"`
	Containers []Container `json:"containers,omitempty"`
}

// Container is always present from the one cheap /containers/json list call (id/name/image/status).
// The rest (CPUPct..OOMKilled) are pointers, left nil unless this container matched config.Block's
// DockerWatch — collecting them needs a stats+inspect call per container (see dockerClient.snapshot),
// which the server never asks for by default, so "not collected" (nil/absent) has to be distinguishable
// from "measured as zero".
type Container struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Image        string   `json:"image,omitempty"`
	Status       string   `json:"status"`
	CPUPct       *float64 `json:"cpuPct,omitempty"`
	MemUsed      *uint64  `json:"memUsed,omitempty"`
	MemLimit     *uint64  `json:"memLimit,omitempty"`
	NetRxBps     *float64 `json:"netRxBps,omitempty"`
	NetTxBps     *float64 `json:"netTxBps,omitempty"`
	PIDs         *uint64  `json:"pids,omitempty"`
	RestartCount *int     `json:"restartCount,omitempty"`
	Health       string   `json:"health,omitempty"`
	OOMKilled    bool     `json:"oomKilled,omitempty"`
}
