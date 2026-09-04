package service

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

type dashJob struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	out    string
	src    string
}

var (
	dashMu    sync.Mutex
	dashJobs  = map[string]*dashJob{}
	ffmpegBin = ""
)

const defaultDashRoot = "/data/zlm"

func dashJobKey(nodeID, app, stream string) string {
	return nodeID + "|" + app + "|" + stream
}

func dashJobStale(job *dashJob, out, src string) bool {
	if job == nil {
		return false
	}
	return job.out != out || job.src != src
}

func ffmpegCandidates() []string {
	out := make([]string, 0, 8)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	if config.C != nil {
		add(config.C.Basic.FFmpeg)
		for _, n := range config.C.Nodes {
			add(n.FFmpeg)
		}
	}
	add("/data/sunpf/ffmpeg-builds/build/release/bin/ffmpeg")
	add("/usr/local/bin/ffmpeg")
	add("/usr/bin/ffmpeg")
	return out
}

func lookFFmpeg() string {
	if ffmpegBin != "" {
		return ffmpegBin
	}
	for _, p := range ffmpegCandidates() {
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			ffmpegBin = p
			return p
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		ffmpegBin = p
		return p
	}
	ffmpegBin = "ffmpeg"
	return ffmpegBin
}

func FFmpegPath() string {
	return lookFFmpeg()
}

func DashEnabled() bool {
	return config.C != nil && config.C.Basic.EnableDash
}

func SetDashEnabled(on bool) error {
	return ApplyDashSettings(on, "")
}

func ApplyDashSettings(on bool, ffmpeg string) error {
	if config.C == nil {
		return fmt.Errorf("config not loaded")
	}
	config.C.Basic.EnableDash = on
	if p := strings.TrimSpace(ffmpeg); p != "" {
		config.C.Basic.FFmpeg = p
	}
	ffmpegBin = ""
	if err := config.Save(); err != nil {
		return err
	}
	if !on {
		dashMu.Lock()
		stopAllDashLocked()
		dashMu.Unlock()
		return nil
	}
	if H != nil {
		go H.EnsureDASHAll()
	}
	return nil
}

func dashFFmpegCmd(rtmpIn, out string) string {
	dir, _ := dashFFmpegArgs(rtmpIn, out)
	bin := lookFFmpeg()
	return "mkdir -p " + dir + "/stream0 " + dir + "/stream1 && " + bin + " -i " + rtmpIn +
		" \\\n  -c:v copy -c:a copy \\\n  -f dash \\\n  -seg_duration 4 \\\n  -window_size 5 \\\n" +
		"  -extra_window_size 75 \\\n" +
		"  -use_template 1 -use_timeline 1 \\\n" +
		"  -init_seg_name 'init-stream$RepresentationID$.m4s' \\\n" +
		"  -media_seg_name 'stream$RepresentationID$/chunk-stream$RepresentationID$-$Number%05d$.m4s' \\\n  " + out
}

func dashFFmpegArgs(rtmpIn, out string) (dir string, args []string) {
	dir = out
	if i := strings.LastIndex(out, "/"); i > 0 {
		dir = out[:i]
	}
	args = []string{
		"-hide_banner", "-nostdin", "-loglevel", "warning",
		"-i", rtmpIn,
		"-c:v", "copy", "-c:a", "copy",
		"-f", "dash",
		"-seg_duration", "4",
		"-window_size", "5",
		"-extra_window_size", "75",
		"-use_template", "1",
		"-use_timeline", "1",
		"-init_seg_name", "init-stream$RepresentationID$.m4s",
		"-media_seg_name", "stream$RepresentationID$/chunk-stream$RepresentationID$-$Number%05d$.m4s",
		out,
	}
	return dir, args
}

func (h *Hub) EnsureDASH(nodeID, vhost, app, stream string) map[string]any {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return map[string]any{"code": -1, "msg": "unknown node"}
	}
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" || stream == "" {
		return map[string]any{"code": -1, "msg": "missing app/stream"}
	}
	out := dashOutputFile(n, vhost, app, stream)
	rtmpIn := localRTMPPlayURL(n, app, stream)
	enabled := DashEnabled()
	key := dashJobKey(nodeID, app, stream)
	dashMu.Lock()
	defer dashMu.Unlock()
	if job := dashJobs[key]; job != nil && job.cmd != nil && job.cmd.Process != nil {
		if !dashJobStale(job, out, rtmpIn) {
			return map[string]any{"code": 0, "enabled": enabled, "running": true, "mpd": out, "ffmpeg": lookFFmpeg(), "msg": "DASH 转封装已在运行"}
		}
		if job.cancel != nil {
			job.cancel()
		}
		delete(dashJobs, key)
	}
	if !enabled {
		return map[string]any{"code": 0, "enabled": false, "running": false, "mpd": out, "cmd": dashFFmpegCmd(rtmpIn, out), "ffmpeg": lookFFmpeg(), "msg": "运维台未开启 DASH 自动转封装"}
	}
	dir, args := dashFFmpegArgs(rtmpIn, out)
	_ = os.MkdirAll(filepath.Join(dir, "stream0"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "stream1"), 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	bin := lookFFmpeg()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return map[string]any{"code": -1, "enabled": true, "running": false, "mpd": out, "cmd": dashFFmpegCmd(rtmpIn, out), "ffmpeg": bin, "msg": "启动 ffmpeg 失败: " + err.Error()}
	}
	job := &dashJob{cmd: cmd, cancel: cancel, out: out, src: rtmpIn}
	dashJobs[key] = job
	logger.Infor("dash ffmpeg start %s pid=%d bin=%s out=%s", key, cmd.Process.Pid, bin, out)
	go func() {
		err := cmd.Wait()
		dashMu.Lock()
		if dashJobs[key] == job {
			delete(dashJobs, key)
		}
		dashMu.Unlock()
		if err != nil {
			logger.Warnf("dash ffmpeg exit %s: %v %s", key, err, strings.TrimSpace(stderr.String()))
		}
	}()
	return map[string]any{"code": 0, "enabled": true, "running": true, "mpd": out, "ffmpeg": bin, "msg": "已启动 DASH 转封装"}
}

func (h *Hub) EnsureDASHAll() {
	if h == nil || h.zlm == nil || !DashEnabled() {
		return
	}
	var nodes []config.Node
	if config.C != nil {
		nodes = append(nodes, config.C.Nodes...)
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		v, err := h.zlm.call(n, "getMediaList", nil)
		if err != nil || v == nil {
			continue
		}
		for _, row := range asSlice(v["data"]) {
			app, stream := asString(row["app"]), asString(row["stream"])
			if app == "" || stream == "" {
				continue
			}
			key := n.ID + "|" + app + "|" + stream
			if seen[key] {
				continue
			}
			seen[key] = true
			h.EnsureDASH(n.ID, asString(row["vhost"]), app, stream)
		}
	}
}

func stopAllDashLocked() {
	for k, job := range dashJobs {
		if job.cancel != nil {
			job.cancel()
		}
		delete(dashJobs, k)
	}
}
