package service

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

const (
	defaultLiveKeepSec = 600
	minLiveKeepSec     = 30
	maxLiveKeepSec     = 86400
)

var unusedMediaDirNames = []string{"hls", "rec", "ts", "flv", "dash", "__defaultVhost__"}

var keepTopDirs = map[string]bool{
	"mp4":  true,
	"snap": true,
}

type MediaSweepResult struct {
	RemovedDirs  int
	RemovedFiles int
	Kept         []string
	Removed      []string
}

func ClampLiveKeepSec(n int) int {
	if n <= 0 {
		return defaultLiveKeepSec
	}
	if n < minLiveKeepSec {
		return minLiveKeepSec
	}
	if n > maxLiveKeepSec {
		return maxLiveKeepSec
	}
	return n
}

func ParseLiveKeepSec(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultLiveKeepSec
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultLiveKeepSec
	}
	return ClampLiveKeepSec(n)
}

func ParseLiveKeepSecStrict(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("不能为空，有效范围 %d-%d 秒", minLiveKeepSec, maxLiveKeepSec)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("须为整数，有效范围 %d-%d 秒", minLiveKeepSec, maxLiveKeepSec)
	}
	if n < minLiveKeepSec || n > maxLiveKeepSec {
		return 0, fmt.Errorf("数值异常，有效范围 %d-%d 秒", minLiveKeepSec, maxLiveKeepSec)
	}
	return n, nil
}

func unusedMediaDirs(enableVhost bool) []string {
	out := make([]string, 0, len(unusedMediaDirNames))
	for _, name := range unusedMediaDirNames {
		if name == "__defaultVhost__" && enableVhost {
			continue
		}
		out = append(out, name)
	}
	return out
}

func isUnusedMediaDir(name string, enableVhost bool) bool {
	for _, u := range unusedMediaDirs(enableVhost) {
		if strings.EqualFold(name, u) {
			return true
		}
	}
	return false
}

func CleanUnusedMediaDirs(base string, enableVhost bool) []string {
	base = filepath.Clean(strings.TrimSpace(base))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return nil
	}
	var removed []string
	for _, name := range unusedMediaDirs(enableVhost) {
		p := filepath.Join(base, name)
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() && st.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			logger.Warnf("清理无用目录失败 %s: %v", p, err)
			continue
		}
		removed = append(removed, p)
	}
	return removed
}

func SweepMediaRoot(base string, enableVhost bool, keep time.Duration, now time.Time) MediaSweepResult {
	res := MediaSweepResult{}
	base = filepath.Clean(strings.TrimSpace(base))
	if base == "" || base == "." {
		return res
	}
	if keep <= 0 {
		keep = time.Duration(defaultLiveKeepSec) * time.Second
	}
	if now.IsZero() {
		now = time.Now()
	}
	for _, p := range CleanUnusedMediaDirs(base, enableVhost) {
		res.RemovedDirs++
		res.Removed = append(res.Removed, p)
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		return res
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if keepTopDirs[strings.ToLower(name)] {
			res.Kept = append(res.Kept, filepath.Join(base, name))
			continue
		}
		if isUnusedMediaDir(name, enableVhost) {
			continue
		}
		sweepAppDir(filepath.Join(base, name), keep, now, &res)
	}
	return res
}

func sweepAppDir(appDir string, keep time.Duration, now time.Time, res *MediaSweepResult) {
	ents, err := os.ReadDir(appDir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		sweepStreamDir(filepath.Join(appDir, e.Name()), keep, now, res)
	}
}

func sweepStreamDir(dir string, keep time.Duration, now time.Time, res *MediaSweepResult) {
	newest, err := newestModTime(dir)
	if err != nil {
		return
	}
	if !newest.IsZero() && now.Sub(newest) > keep {
		if err := os.RemoveAll(dir); err == nil {
			res.RemovedDirs++
			res.Removed = append(res.Removed, dir)
		}
		return
	}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return nil
		}
		if d.IsDir() {
			if isDateFolder(d.Name()) {
				nt, _ := newestModTime(path)
				if !nt.IsZero() && now.Sub(nt) > keep {
					if err := os.RemoveAll(path); err == nil {
						res.RemovedDirs++
						res.Removed = append(res.Removed, path)
					}
					return filepath.SkipDir
				}
			}
			return nil
		}
		name := d.Name()
		if keepLiveMeta(name) {
			return nil
		}
		if !isLiveSegment(name) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if now.Sub(info.ModTime()) <= keep {
			return nil
		}
		if err := os.Remove(path); err == nil {
			res.RemovedFiles++
			res.Removed = append(res.Removed, path)
		}
		return nil
	})
}

func newestModTime(dir string) (time.Time, error) {
	var newest time.Time
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		mt := info.ModTime()
		if newest.IsZero() || mt.After(newest) {
			newest = mt
		}
		return nil
	})
	return newest, err
}

func keepLiveMeta(name string) bool {
	n := strings.ToLower(name)
	if n == "init.mp4" || strings.HasPrefix(n, "init-stream") {
		return true
	}
	switch filepath.Ext(n) {
	case ".m3u8", ".mpd":
		return true
	}
	return false
}

func isLiveSegment(name string) bool {
	n := strings.ToLower(name)
	switch filepath.Ext(n) {
	case ".ts", ".m4s":
		return true
	case ".mp4":
		return n != "init.mp4"
	}
	return false
}

func mediaRootOf(n config.Node) string {
	root := strings.ReplaceAll(strings.TrimSpace(n.WWW), "\\", "/")
	root = strings.TrimRight(root, "/")
	if strings.HasSuffix(strings.ToLower(root), "/www") {
		root = defaultDashRoot
	}
	if root == "" {
		root = strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(n.Root), "\\", "/"), "/")
	}
	if root == "" || strings.HasSuffix(strings.ToLower(root), "/www") {
		root = defaultDashRoot
	}
	return root
}

func (h *Hub) sweepMediaOnce() {
	if h == nil || config.C == nil {
		return
	}
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	now := time.Now()
	for _, n := range nodes {
		base := mediaRootOf(n)
		keep := time.Duration(ClampLiveKeepSec(n.LiveKeepSec)) * time.Second
		res := SweepMediaRoot(base, n.EnableVhost, keep, now)
		if res.RemovedDirs+res.RemovedFiles > 0 {
			logger.Infor("media sweep base=%s vhost=%v keep=%s dirs=%d files=%d", base, n.EnableVhost, keep, res.RemovedDirs, res.RemovedFiles)
		}
	}
}
