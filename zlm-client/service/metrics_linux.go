//go:build linux

package service

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"zlm-admin/model"
)

type netSnap struct {
	rx, tx uint64
	at     time.Time
}

type diskSnap struct {
	read, write uint64
	at          time.Time
}

type cpuSnap struct {
	idle, total uint64
	ok          bool
}

var (
	hostMu     sync.Mutex
	lastCPU    cpuSnap
	lastNet    netSnap
	lastDiskIO diskSnap
)

func collectHost(diskPath string) model.HostMetrics {
	h := model.HostMetrics{GOOS: runtime.GOOS, CPUCores: runtime.NumCPU(), DiskPath: diskPath}
	h.Hostname, _ = os.Hostname()
	if diskPath == "" {
		h.DiskPath = "/"
	}
	h.CPUPercent = linuxCPU()
	h.Load1, h.Load5, h.Load15 = linuxLoad()
	h.MemTotal, h.MemUsed, h.MemPercent = linuxMem()
	h.DiskTotal, h.DiskUsed, h.DiskPercent = linuxDisk(h.DiskPath)
	h.NetRxBps, h.NetTxBps = linuxNet()
	h.NetCapBps = linuxNetCap()
	if h.NetCapBps > 0 {
		h.NetUtilPct = float64(h.NetRxBps+h.NetTxBps) * 8 * 100 / float64(h.NetCapBps)
		if h.NetUtilPct > 100 {
			h.NetUtilPct = 100
		}
	}
	h.DiskReadBps, h.DiskWriteBps = linuxDiskIO()
	h.UptimeSec = linuxUptime()
	return h
}

func linuxCPU() float64 {
	idle, total, ok := readCPU()
	if !ok {
		return 0
	}
	hostMu.Lock()
	defer hostMu.Unlock()
	if !lastCPU.ok {
		lastCPU = cpuSnap{idle, total, true}
		return 0
	}
	di := idle - lastCPU.idle
	dt := total - lastCPU.total
	lastCPU = cpuSnap{idle, total, true}
	if dt == 0 {
		return 0
	}
	busy := 1 - float64(di)/float64(dt)
	if busy < 0 {
		return 0
	}
	return busy * 100
}

func readCPU() (idle, total uint64, ok bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return
	}
	fs := strings.Fields(sc.Text())
	if len(fs) < 5 || fs[0] != "cpu" {
		return
	}
	var nums []uint64
	for _, s := range fs[1:] {
		n, _ := strconv.ParseUint(s, 10, 64)
		nums = append(nums, n)
		total += n
	}
	if len(nums) > 3 {
		idle = nums[3]
	}
	if len(nums) > 4 {
		idle += nums[4]
	}
	return idle, total, true
}

func linuxLoad() (float64, float64, float64) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0
	}
	fs := strings.Fields(string(b))
	if len(fs) < 3 {
		return 0, 0, 0
	}
	a, _ := strconv.ParseFloat(fs[0], 64)
	b5, _ := strconv.ParseFloat(fs[1], 64)
	c, _ := strconv.ParseFloat(fs[2], 64)
	return a, b5, c
}

func linuxMem() (total, used uint64, pct float64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			total = parseKiB(line) * 1024
		case strings.HasPrefix(line, "MemAvailable:"):
			avail = parseKiB(line) * 1024
		}
	}
	if total > avail {
		used = total - avail
	}
	if total > 0 {
		pct = float64(used) * 100 / float64(total)
	}
	return
}

func parseKiB(line string) uint64 {
	fs := strings.Fields(line)
	if len(fs) < 2 {
		return 0
	}
	n, _ := strconv.ParseUint(fs[1], 10, 64)
	return n
}

func linuxDisk(path string) (total, used uint64, pct float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return
	}
	total = st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total > free {
		used = total - free
	}
	if total > 0 {
		pct = float64(used) * 100 / float64(total)
	}
	return
}

func linuxUptime() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fs := strings.Fields(string(b))
	if len(fs) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fs[0], 64)
	return v
}

func linuxNet() (rxBps, txBps uint64) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer f.Close()
	var rx, tx uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		name := strings.TrimSpace(parts[0])
		if name == "lo" || len(parts) < 2 {
			continue
		}
		fs := strings.Fields(parts[1])
		if len(fs) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fs[0], 10, 64)
		t, _ := strconv.ParseUint(fs[8], 10, 64)
		rx += r
		tx += t
	}
	now := time.Now()
	hostMu.Lock()
	defer hostMu.Unlock()
	if !lastNet.at.IsZero() {
		dt := now.Sub(lastNet.at).Seconds()
		if dt > 0.2 {
			if rx >= lastNet.rx {
				rxBps = uint64(float64(rx-lastNet.rx) / dt)
			}
			if tx >= lastNet.tx {
				txBps = uint64(float64(tx-lastNet.tx) / dt)
			}
		}
	}
	lastNet = netSnap{rx, tx, now}
	return
}

func linuxNetCap() uint64 {
	ents, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return 0
	}
	var max uint64
	for _, e := range ents {
		name := e.Name()
		if name == "lo" {
			continue
		}
		b, err := os.ReadFile("/sys/class/net/" + name + "/speed")
		if err != nil {
			continue
		}
		mbps, _ := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
		if mbps == 0 || mbps > 100000 {
			continue
		}
		bits := mbps * 1000 * 1000
		if bits > max {
			max = bits
		}
	}
	return max
}

func linuxDiskIO() (readBps, writeBps uint64) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return
	}
	defer f.Close()
	var rd, wr uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fs := strings.Fields(sc.Text())
		if len(fs) < 14 {
			continue
		}
		name := fs[2]
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
			continue
		}
		if hasDigitSuffixPartition(name) {
			continue
		}
		secR, _ := strconv.ParseUint(fs[5], 10, 64)
		secW, _ := strconv.ParseUint(fs[9], 10, 64)
		rd += secR * 512
		wr += secW * 512
	}
	now := time.Now()
	hostMu.Lock()
	defer hostMu.Unlock()
	if !lastDiskIO.at.IsZero() {
		dt := now.Sub(lastDiskIO.at).Seconds()
		if dt > 0.2 {
			if rd >= lastDiskIO.read {
				readBps = uint64(float64(rd-lastDiskIO.read) / dt)
			}
			if wr >= lastDiskIO.write {
				writeBps = uint64(float64(wr-lastDiskIO.write) / dt)
			}
		}
	}
	lastDiskIO = diskSnap{rd, wr, now}
	return
}

func hasDigitSuffixPartition(name string) bool {
	if strings.HasPrefix(name, "nvme") {
		return strings.Contains(name, "p") && name[len(name)-1] >= '0' && name[len(name)-1] <= '9'
	}
	if len(name) < 4 {
		return false
	}
	last := name[len(name)-1]
	return last >= '0' && last <= '9' && (strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") || strings.HasPrefix(name, "hd") || strings.HasPrefix(name, "xvd"))
}
