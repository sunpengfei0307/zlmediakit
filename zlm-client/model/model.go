package model

type HostMetrics struct {
	GOOS         string  `json:"goos"`
	CPUPercent   float64 `json:"cpu_percent"`
	CPUCores     int     `json:"cpu_cores"`
	Load1        float64 `json:"load1"`
	Load5        float64 `json:"load5"`
	Load15       float64 `json:"load15"`
	MemTotal     uint64  `json:"mem_total"`
	MemUsed      uint64  `json:"mem_used"`
	MemPercent   float64 `json:"mem_percent"`
	DiskTotal    uint64  `json:"disk_total"`
	DiskUsed     uint64  `json:"disk_used"`
	DiskPercent  float64 `json:"disk_percent"`
	DiskPath     string  `json:"disk_path"`
	NetRxBps     uint64  `json:"net_rx_bps"`
	NetTxBps     uint64  `json:"net_tx_bps"`
	NetCapBps    uint64  `json:"net_cap_bps"`
	NetUtilPct   float64 `json:"net_util_percent"`
	DiskReadBps  uint64  `json:"disk_read_bps"`
	DiskWriteBps uint64  `json:"disk_write_bps"`
	UptimeSec    float64 `json:"uptime_sec"`
	Hostname     string  `json:"hostname"`
}

type MetricSample struct {
	T        int64   `json:"t"`
	Push     int     `json:"push"`
	Pull     int     `json:"pull"`
	Conn     int     `json:"conn"`
	CPU      float64 `json:"cpu"`
	Mem      float64 `json:"mem"`
	NetUtil  float64 `json:"net_util"`
	NetRxBps uint64  `json:"net_rx"`
	NetTxBps uint64  `json:"net_tx"`
	InBps    uint64  `json:"in_bps"`
	OutBps   uint64  `json:"out_bps"`
}

type LogFileInfo struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mtime"`
}

type HookEvent struct {
	Time   string         `json:"time"`
	Event  string         `json:"event"`
	Server string         `json:"server"`
	Body   map[string]any `json:"body"`
}
