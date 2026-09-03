package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

var saveMu sync.Mutex

// Setup 全局配置
type Setup struct {
	Basic  Basic  `toml:"basic"`
	Loger  Loger  `toml:"loger"`
	Listen string `toml:"listen"`
	Nodes  []Node `toml:"nodes"`
}

// Basic 业务配置
type Basic struct {
	Name         string `toml:"name"`
	Vers         string `toml:"vers"`
	Type         string `toml:"type"`
	Mode         string `toml:"mode"`
	Mark         string `toml:"mark"`
	Role         string `toml:"role"`
	Port         int    `toml:"port"`
	HttpsPort    int    `toml:"https_port"`
	Skey         string `toml:"skey"`
	EnableDash   bool   `toml:"enable_dash" json:"enable_dash"`
	EnableSnap   bool   `toml:"enable_snap" json:"enable_snap"`
	SnapInterval int    `toml:"snap_interval" json:"snap_interval"`
	FFmpeg       string `toml:"ffmpeg" json:"ffmpeg,omitempty"`
}

// Loger 日志配置
type Loger struct {
	Path string `toml:"path"`
	Rank string `toml:"rank"`
	Size int    `toml:"size"`
	Ages int    `toml:"ages"`
	Baks int    `toml:"baks"`
	Pack bool   `toml:"pack"`
}

// Node ZLM 节点
type Node struct {
	ID           string `toml:"id" json:"id"`
	Name         string `toml:"name" json:"name"`
	API          string `toml:"api" json:"api"`
	Secret       string `toml:"secret" json:"secret,omitempty"`
	HTTPPort     int    `toml:"http_port" json:"http_port"`
	HTTPSPort    int    `toml:"https_port" json:"https_port"`
	RTSPPort     int    `toml:"rtsp_port" json:"rtsp_port"`
	RTMPPort     int    `toml:"rtmp_port" json:"rtmp_port"`
	SRTPort      int    `toml:"srt_port" json:"srt_port"`
	WebRTCPort   int    `toml:"webrtc_port" json:"webrtc_port"`
	LocalMetrics bool   `toml:"local_metrics" json:"local_metrics"`
	MetricsURL   string `toml:"metrics_url" json:"metrics_url,omitempty"`
	INI          string `toml:"ini" json:"ini,omitempty"`
	LogDir       string `toml:"log_dir" json:"log_dir,omitempty"`
	Root         string `toml:"root" json:"root,omitempty"`
	Bin          string `toml:"bin" json:"bin,omitempty"`
	FFmpeg       string `toml:"-" json:"ffmpeg,omitempty"`
	WWW          string `toml:"-" json:"www,omitempty"`
	MP4Save      string `toml:"-" json:"mp4_save,omitempty"`
	HLSSave      string `toml:"-" json:"hls_save,omitempty"`
	EnableVhost  bool   `toml:"-" json:"enable_vhost,omitempty"`
	LiveKeepSec  int    `toml:"-" json:"live_keep_sec,omitempty"`
	SipPort      int    `toml:"-" json:"sip_port,omitempty"`
}

var (
	C    *Setup
	File string
)

func New(file string) *Setup {
	cfg := new(Setup)
	cfg.Basic.EnableSnap = false
	cfg.Basic.SnapInterval = 15
	if _, err := toml.DecodeFile(file, cfg); err != nil {
		panic(err)
	}
	cfg.normalize()
	return cfg
}

func (cfg *Setup) normalize() {
	if cfg.Basic.Name == "" {
		cfg.Basic.Name = "ZLM-ADMIN"
	}
	if cfg.Basic.Type == "" {
		cfg.Basic.Type = "web"
	}
	if cfg.Basic.Mode == "" {
		cfg.Basic.Mode = "release"
	}
	if cfg.Loger.Path == "" || cfg.Loger.Path == "auto" {
		cfg.Loger.Path = "log"
	}
	if cfg.Loger.Rank == "" {
		cfg.Loger.Rank = "info"
	}
	if cfg.Loger.Size == 0 {
		cfg.Loger.Size = 200
	}
	if cfg.Loger.Ages == 0 {
		cfg.Loger.Ages = 30
	}
	if cfg.Loger.Baks == 0 {
		cfg.Loger.Baks = 100
	}
	if cfg.Listen != "" && cfg.Basic.Port == 0 {
		p := strings.TrimPrefix(cfg.Listen, ":")
		if n, err := strconv.Atoi(p); err == nil {
			cfg.Basic.Port = n
		}
	}
	if cfg.Basic.Port == 0 {
		cfg.Basic.Port = 7788
	}
	if cfg.Basic.HttpsPort == 0 {
		cfg.Basic.HttpsPort = 7789
	}
	if cfg.Basic.SnapInterval <= 0 {
		cfg.Basic.SnapInterval = 15
	}
	if cfg.Listen == "" {
		cfg.Listen = fmt.Sprintf(":%d", cfg.Basic.Port)
	}
	for i := range cfg.Nodes {
		n := &cfg.Nodes[i]
		if n.Root == "" && n.INI != "" {
			n.Root = filepath.Dir(n.INI)
		}
		if n.Bin == "" && n.Root != "" {
			n.Bin = filepath.Join(n.Root, "MediaServer")
		}
	}
}

func LogDir() string {
	if C == nil || C.Loger.Path == "" || C.Loger.Path == "auto" {
		return "log"
	}
	return C.Loger.Path
}

func findConfigFile() string {
	if file := os.Getenv("ZLM_ADMIN_CONFIG"); file != "" {
		return file
	}
	pwd, _ := os.Getwd()
	dir, _ := filepath.Abs(pwd)
	for {
		candidate := filepath.Join(dir, "core", "config", "config.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return candidate
		}
		dir = parent
	}
}

func Reload(file string) *Setup {
	File = file
	C = New(file)
	fmt.Printf("Hi! setup({path:'%+v'}) parse success!\n", file)
	return C
}

func Save() error {
	saveMu.Lock()
	defer saveMu.Unlock()
	if File == "" {
		File = findConfigFile()
	}
	snapshot := *C
	snapshot.Nodes = append([]Node(nil), C.Nodes...)
	for i := range snapshot.Nodes {
		snapshot.Nodes[i].Secret = ""
	}
	tmp := File + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := toml.NewEncoder(f)
	err = enc.Encode(&snapshot)
	_ = f.Close()
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, File)
}

func init() {
	File = findConfigFile()
	C = New(File)
	fmt.Printf("Hi! setup({path:'%+v'}) parse success!\n", File)
}
