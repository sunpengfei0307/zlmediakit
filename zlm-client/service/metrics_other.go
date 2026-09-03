//go:build !linux

package service

import (
	"os"
	"runtime"
	"zlm-admin/model"
)

func collectHost(diskPath string) model.HostMetrics {
	h := model.HostMetrics{GOOS: runtime.GOOS, CPUCores: runtime.NumCPU(), DiskPath: diskPath}
	h.Hostname, _ = os.Hostname()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	h.MemUsed = ms.Alloc
	h.MemTotal = ms.Sys
	if h.MemTotal > 0 {
		h.MemPercent = float64(h.MemUsed) * 100 / float64(h.MemTotal)
	}
	return h
}
