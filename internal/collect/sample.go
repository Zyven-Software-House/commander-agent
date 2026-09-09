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

type Container struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	CPUPct       float64 `json:"cpuPct"`
	MemUsed      uint64  `json:"memUsed"`
	MemLimit     uint64  `json:"memLimit"`
	NetRxBps     float64 `json:"netRxBps"`
	NetTxBps     float64 `json:"netTxBps"`
	PIDs         uint64  `json:"pids"`
	RestartCount int     `json:"restartCount"`
	Health       string  `json:"health,omitempty"`
	OOMKilled    bool    `json:"oomKilled"`
	Status       string  `json:"status"`
}
