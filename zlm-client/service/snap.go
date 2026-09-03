package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

const (
	defaultSnapInterval = 15
	minSnapInterval     = 5
	maxSnapInterval     = 300
	minSnapBytes        = 800
	maxSnapBytes        = 16 << 20
)

var (
	snapConfigMu sync.RWMutex
	snapBusy     sync.Map
	snapWorkers  = make(chan struct{}, 2)
)

func SnapEnabled() bool {
	snapConfigMu.RLock()
	defer snapConfigMu.RUnlock()
	return config.C != nil && config.C.Basic.EnableSnap
}

func SnapInterval() int {
	snapConfigMu.RLock()
	defer snapConfigMu.RUnlock()
	if config.C == nil {
		return defaultSnapInterval
	}
	return ClampSnapInterval(config.C.Basic.SnapInterval)
}

func ClampSnapInterval(n int) int {
	if n <= 0 {
		return defaultSnapInterval
	}
	if n < minSnapInterval {
		return minSnapInterval
	}
	if n > maxSnapInterval {
		return maxSnapInterval
	}
	return n
}

func ApplySnapSettings(on bool, interval int) error {
	snapConfigMu.Lock()
	defer snapConfigMu.Unlock()
	if config.C == nil {
		return fmt.Errorf("config not loaded")
	}
	config.C.Basic.EnableSnap = on
	config.C.Basic.SnapInterval = ClampSnapInterval(interval)
	return config.Save()
}

func snapRootOf(n config.Node) string {
	if p := strings.TrimSpace(n.MP4Save); p != "" {
		return filepath.Join(filepath.Dir(p), "snap")
	}
	if p := strings.TrimSpace(n.WWW); p != "" {
		return filepath.Join(p, "snap")
	}
	return "/data/zlm/snap"
}

func snapStreamName(stream string) string {
	s := strings.TrimSpace(stream)
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" {
		return "_"
	}
	return s
}

func snapCoverPath(n config.Node, stream string) string {
	stream = snapStreamName(stream)
	if stream == "_" {
		return ""
	}
	return filepath.Join(snapRootOf(n), stream, "latest.jpg")
}

func snapArchivePath(n config.Node, stream string, t time.Time) string {
	stream = snapStreamName(stream)
	return filepath.Join(snapRootOf(n), stream, t.Format("2006-01-02"), t.Format("150405")+".jpg")
}

func uniqueSnapPath(p string) string {
	if _, err := os.Stat(p); err != nil {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s_%d%s", base, i, ext)
		if _, err := os.Stat(cand); err != nil {
			return cand
		}
	}
	return fmt.Sprintf("%s_%d%s", base, time.Now().UnixNano(), ext)
}

func snapPlayURL(n config.Node, app, stream string) string {
	return fmt.Sprintf("rtmp://127.0.0.1:%d/%s/%s", nz(n.RTMPPort, 1935), app, stream)
}

func writeJPEG(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type snapTarget struct {
	App    string
	Stream string
}

type snapWorkerKey struct {
	NodeID string
	App    string
	Stream string
}

type snapWorkerSpec struct {
	Key      snapWorkerKey
	Node     config.Node
	Target   snapTarget
	Interval int
}

type snapWorkerDiscovery struct {
	Specs      []snapWorkerSpec
	Configured map[string]bool
	Synced     map[string]bool
}

type snapWorkerJob struct {
	interval int
	cancel   context.CancelFunc
	done     chan struct{}
}

func scheduledSnapTargets(rows []map[string]any) []snapTarget {
	out := make([]snapTarget, 0)
	seen := map[snapTarget]bool{}
	for _, row := range rows {
		app := strings.TrimSpace(asString(row["app"]))
		stream := strings.TrimSpace(asString(row["stream"]))
		target := snapTarget{App: app, Stream: stream}
		if app == "" || stream == "" || seen[target] {
			continue
		}
		hasVideo := false
		for _, track := range asSlice(row["tracks"]) {
			if int(asFloat(track["codec_type"])) == 0 {
				hasVideo = true
				break
			}
		}
		if !hasVideo {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out
}

func planSnapWorkerSync(current map[snapWorkerKey]int, wanted []snapWorkerSpec) ([]snapWorkerSpec, []snapWorkerKey) {
	wantedByKey := make(map[snapWorkerKey]snapWorkerSpec, len(wanted))
	start := make([]snapWorkerSpec, 0)
	for _, spec := range wanted {
		spec.Interval = ClampSnapInterval(spec.Interval)
		wantedByKey[spec.Key] = spec
		if interval, ok := current[spec.Key]; !ok || interval != spec.Interval {
			start = append(start, spec)
		}
	}
	stop := make([]snapWorkerKey, 0)
	for key, interval := range current {
		spec, ok := wantedByKey[key]
		if !ok || interval != spec.Interval {
			stop = append(stop, key)
		}
	}
	return start, stop
}

func mergeUnsyncedSnapWorkers(current map[snapWorkerKey]int, discovery snapWorkerDiscovery) []snapWorkerSpec {
	wanted := append([]snapWorkerSpec(nil), discovery.Specs...)
	for key, interval := range current {
		if discovery.Configured[key.NodeID] && !discovery.Synced[key.NodeID] {
			wanted = append(wanted, snapWorkerSpec{Key: key, Interval: interval})
		}
	}
	return wanted
}

func readJPEGStream(r io.Reader, onFrame func([]byte) error) error {
	buf := make([]byte, 32<<10)
	frame := make([]byte, 0, 256<<10)
	var prev byte
	havePrev, inFrame := false, false
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if !inFrame {
				if havePrev && prev == 0xff && b == 0xd8 {
					frame = append(frame[:0], 0xff, 0xd8)
					inFrame, havePrev = true, false
					continue
				}
				prev, havePrev = b, true
				continue
			}
			frame = append(frame, b)
			if len(frame) > maxSnapBytes {
				return fmt.Errorf("jpeg frame exceeds %d bytes", maxSnapBytes)
			}
			if len(frame) >= 2 && frame[len(frame)-2] == 0xff && b == 0xd9 {
				if len(frame) >= minSnapBytes {
					out := append([]byte(nil), frame...)
					if err := onFrame(out); err != nil {
						return err
					}
				}
				frame = frame[:0]
				inFrame, havePrev = false, false
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func persistentSnapArgs(n config.Node, target snapTarget, interval int) []string {
	return []string{
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-rw_timeout", "5000000",
		"-i", snapPlayURL(n, target.App, target.Stream),
		"-vf", fmt.Sprintf("fps=1/%d", ClampSnapInterval(interval)),
		"-an", "-c:v", "mjpeg", "-q:v", "3",
		"-f", "image2pipe", "pipe:1",
	}
}

type cappedLogWriter struct {
	data  []byte
	limit int
}

func newCappedLogWriter(limit int) *cappedLogWriter {
	return &cappedLogWriter{limit: limit}
}

func (w *cappedLogWriter) Write(p []byte) (int, error) {
	n := len(p)
	if w.limit <= 0 {
		return n, nil
	}
	if len(p) >= w.limit {
		w.data = append(w.data[:0], p[len(p)-w.limit:]...)
		return n, nil
	}
	overflow := len(w.data) + len(p) - w.limit
	if overflow > 0 {
		copy(w.data, w.data[overflow:])
		w.data = w.data[:len(w.data)-overflow]
	}
	w.data = append(w.data, p...)
	return n, nil
}

func (w *cappedLogWriter) String() string {
	return string(w.data)
}

func runPersistentSnapWorker(ctx context.Context, spec snapWorkerSpec) error {
	cmd := exec.CommandContext(ctx, lookFFmpeg(), persistentSnapArgs(spec.Node, spec.Target, spec.Interval)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := newCappedLogWriter(64 << 10)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	readErr := readJPEGStream(stdout, func(frame []byte) error {
		_, err := writeScheduledSnap(spec.Node, spec.Target.Stream, time.Now(), frame)
		return err
	})
	if readErr != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if readErr != nil {
		return readErr
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%w: %s", waitErr, msg)
		}
		return waitErr
	}
	return fmt.Errorf("ffmpeg snapshot worker exited")
}

func writeScheduledSnap(n config.Node, stream string, at time.Time, body []byte) (string, error) {
	archive := snapArchivePath(n, stream, at)
	if err := writeJPEG(archive, body); err != nil {
		return "", err
	}
	if err := writeJPEG(snapCoverPath(n, stream), body); err != nil {
		return "", err
	}
	return archive, nil
}

func (h *Hub) discoverSnapWorkerSpecs() snapWorkerDiscovery {
	discovery := snapWorkerDiscovery{
		Specs:      make([]snapWorkerSpec, 0),
		Configured: make(map[string]bool),
		Synced:     make(map[string]bool),
	}
	if h == nil || config.C == nil {
		return discovery
	}
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	for _, node := range nodes {
		n := node
		discovery.Configured[n.ID] = true
		ApplyZLMIni(&n)
		result, err := h.zlm.call(n, "getMediaList", nil)
		if err != nil {
			logger.Debug("定时截图查流失败 node=%s: %v", n.ID, err)
			continue
		}
		discovery.Synced[n.ID] = true
		for _, target := range scheduledSnapTargets(asSlice(result["data"])) {
			discovery.Specs = append(discovery.Specs, snapWorkerSpec{
				Key:  snapWorkerKey{NodeID: n.ID, App: target.App, Stream: target.Stream},
				Node: n, Target: target, Interval: SnapInterval(),
			})
		}
	}
	return discovery
}

func (h *Hub) runSnapWorker(ctx context.Context, spec snapWorkerSpec) {
	delay := 2 * time.Second
	run := runPersistentSnapWorker
	if h.snapWorkerRun != nil {
		run = h.snapWorkerRun
	}
	for {
		started := time.Now()
		err := run(ctx, spec)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) >= 30*time.Second {
			delay = 2 * time.Second
		}
		logger.Warnf("定时截图 worker 退出 node=%s stream=%s/%s: %v，%s 后重试",
			spec.Key.NodeID, spec.Key.App, spec.Key.Stream, err, delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

func (h *Hub) reconcileSnapWorkers(ctx context.Context, wanted []snapWorkerSpec) {
	h.snapWorkerMu.Lock()
	defer h.snapWorkerMu.Unlock()
	if h.snapWorkers == nil {
		h.snapWorkers = make(map[snapWorkerKey]*snapWorkerJob)
	}
	current := make(map[snapWorkerKey]int, len(h.snapWorkers))
	for key, job := range h.snapWorkers {
		current[key] = job.interval
	}
	start, stop := planSnapWorkerSync(current, wanted)
	stopping := make([]*snapWorkerJob, 0, len(stop))
	for _, key := range stop {
		if job := h.snapWorkers[key]; job != nil {
			job.cancel()
			stopping = append(stopping, job)
		}
		delete(h.snapWorkers, key)
	}
	for _, job := range stopping {
		<-job.done
	}
	for _, spec := range start {
		workerCtx, cancel := context.WithCancel(ctx)
		job := &snapWorkerJob{interval: spec.Interval, cancel: cancel, done: make(chan struct{})}
		h.snapWorkers[spec.Key] = job
		go func() {
			defer close(job.done)
			h.runSnapWorker(workerCtx, spec)
		}()
	}
}

func (h *Hub) reconcileSnapDiscovery(ctx context.Context, discovery snapWorkerDiscovery) {
	h.snapWorkerMu.Lock()
	current := make(map[snapWorkerKey]int, len(h.snapWorkers))
	for key, job := range h.snapWorkers {
		current[key] = job.interval
	}
	h.snapWorkerMu.Unlock()
	h.reconcileSnapWorkers(ctx, mergeUnsyncedSnapWorkers(current, discovery))
}

func (h *Hub) stopSnapWorkers() {
	h.snapWorkerMu.Lock()
	defer h.snapWorkerMu.Unlock()
	jobs := make([]*snapWorkerJob, 0, len(h.snapWorkers))
	for _, job := range h.snapWorkers {
		job.cancel()
		jobs = append(jobs, job)
	}
	for _, job := range jobs {
		<-job.done
	}
	clear(h.snapWorkers)
}

func (h *Hub) StopSnapWorkers() {
	h.stopSnapWorkers()
}

func (h *Hub) SnapLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer func() {
		timer.Stop()
		h.stopSnapWorkers()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if SnapEnabled() {
				h.reconcileSnapDiscovery(ctx, h.discoverSnapWorkerSpecs())
				timer.Reset(3 * time.Second)
			} else {
				h.stopSnapWorkers()
				timer.Reset(time.Second)
			}
		}
	}
}

func (h *Hub) grabSnap(nodeID, app, stream string) ([]byte, error) {
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" || stream == "" {
		return nil, fmt.Errorf("missing app/stream")
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return nil, fmt.Errorf("unknown node")
	}
	ApplyZLMIni(&n)
	key := nodeID + "|" + app + "|" + stream
	if _, loaded := snapBusy.LoadOrStore(key, struct{}{}); loaded {
		return nil, fmt.Errorf("busy")
	}
	defer snapBusy.Delete(key)
	snapWorkers <- struct{}{}
	defer func() { <-snapWorkers }()
	return ffmpegOneFrame(n, app, stream)
}

func ffmpegOneFrame(n config.Node, app, stream string) ([]byte, error) {
	ff := lookFFmpeg()
	dir, err := os.MkdirTemp("", "zlm-snap-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "frame.jpg")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ff,
		"-hide_banner", "-loglevel", "error",
		"-rw_timeout", "5000000",
		"-i", snapPlayURL(n, app, stream),
		"-y", "-f", "mjpeg", "-frames:v", "1", "-an", out,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg snap: %s", msg)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	if len(body) < minSnapBytes || !bytes.HasPrefix(body, []byte{0xff, 0xd8}) {
		return nil, fmt.Errorf("not a jpeg (%d bytes)", len(body))
	}
	return body, nil
}

func (h *Hub) SnapNow(nodeID, app, stream string) (string, error) {
	body, err := h.grabSnap(nodeID, app, stream)
	if err != nil {
		return "", err
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return "", fmt.Errorf("unknown node")
	}
	ApplyZLMIni(&n)
	out := uniqueSnapPath(snapArchivePath(n, stream, time.Now()))
	if err := writeJPEG(out, body); err != nil {
		return "", err
	}
	return out, nil
}

func (h *Hub) snapCover(nodeID, app, stream string) (string, error) {
	body, err := h.grabSnap(nodeID, app, stream)
	if err != nil {
		return "", err
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return "", fmt.Errorf("unknown node")
	}
	ApplyZLMIni(&n)
	out := snapCoverPath(n, stream)
	if out == "" {
		return "", fmt.Errorf("empty snap path")
	}
	if err := writeJPEG(out, body); err != nil {
		return "", err
	}
	return out, nil
}

func needsSnapCapture(enabled, coverExists bool) bool {
	return !enabled || !coverExists
}

func (h *Hub) ServeSnap(w http.ResponseWriter, r *http.Request, nodeID, app, stream string) {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		http.Error(w, `{"code":-1,"msg":"unknown node"}`, http.StatusNotFound)
		return
	}
	ApplyZLMIni(&n)
	p := snapCoverPath(n, stream)
	st, statErr := os.Stat(p)
	exists := statErr == nil && !st.IsDir()
	if needsSnapCapture(SnapEnabled(), exists) {
		if _, err := h.snapCover(nodeID, app, stream); err != nil {
			st, statErr = os.Stat(p)
			if statErr != nil || st.IsDir() {
				http.Error(w, `{"code":-1,"msg":"no snap"}`, http.StatusNotFound)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, p)
}
