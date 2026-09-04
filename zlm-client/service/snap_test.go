package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"zlm-admin/core/config"
)

func TestClampSnapInterval(t *testing.T) {
	if ClampSnapInterval(0) != 15 || ClampSnapInterval(3) != 5 || ClampSnapInterval(400) != 300 {
		t.Fatal(ClampSnapInterval(0), ClampSnapInterval(3), ClampSnapInterval(400))
	}
}

func TestSnapPaths(t *testing.T) {
	n := config.Node{MP4Save: "/data/zlm/mp4", RTMPPort: 1935}
	got := snapCoverPath(n, "cam")
	want := filepath.Join("/data/zlm", "snap", "cam", "latest.jpg")
	if got != want {
		t.Fatalf("cover got %s want %s", got, want)
	}
	at := time.Date(2026, 8, 21, 16, 13, 5, 0, time.Local)
	arch := snapArchivePath(n, "cam", at)
	wantArch := filepath.Join("/data/zlm", "snap", "cam", "2026-08-21", "161305.jpg")
	if arch != wantArch {
		t.Fatalf("archive got %s want %s", arch, wantArch)
	}
	if snapPlayURL(n, "live", "cam") != "rtmp://127.0.0.1:1935/live/cam" {
		t.Fatal(snapPlayURL(n, "live", "cam"))
	}
	if snapStreamName("a/b") != "a_b" {
		t.Fatal(snapStreamName("a/b"))
	}
}

func TestSnapAndDashPlayURLFollowsAuthToken(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	prev := H
	H = h
	t.Cleanup(func() { H = prev })
	n := config.Node{RTMPPort: 1935}
	if snapPlayURL(n, "live", "ls_zlm_h264_1080p") != "rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p" {
		t.Fatal("unrestricted snap url must stay anonymous")
	}
	if _, err := h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "ls_zlm_h264_1080p", 0); err != nil {
		t.Fatal(err)
	}
	want := "rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p?token=secret-token"
	if got := snapPlayURL(n, "live", "ls_zlm_h264_1080p"); got != want {
		t.Fatalf("snap url=%s", got)
	}
	if got := localRTMPPlayURL(n, "live", "ls_zlm_h264_1080p"); got != want {
		t.Fatalf("dash url=%s", got)
	}
	if got := snapPlayURL(n, "live", "other"); got != "rtmp://127.0.0.1:1935/live/other" {
		t.Fatalf("other stream=%s", got)
	}
	args := strings.Join(persistentSnapArgs(n, snapTarget{App: "live", Stream: "ls_zlm_h264_1080p"}, 15), " ")
	if !strings.Contains(args, "-i "+want) {
		t.Fatalf("persistent snap args missing token: %s", args)
	}
	if !dashJobStale(&dashJob{out: "/data/zlm/live/cam/dash.mpd", src: "rtmp://127.0.0.1:1935/live/cam"}, "/data/zlm/live/cam/dash.mpd", want) {
		t.Fatal("dash job must restart when play token appears")
	}
}

func TestAuthChangeCancelsDashJobsForTokenRefresh(t *testing.T) {
	h := &Hub{}
	cancelled := false
	dashMu.Lock()
	prev := dashJobs
	dashJobs = map[string]*dashJob{
		"zlm-1|live|cam": {
			out: "/tmp/dash.mpd",
			src: "rtmp://127.0.0.1:1935/live/cam",
			cancel: func() { cancelled = true },
		},
	}
	dashMu.Unlock()
	t.Cleanup(func() {
		dashMu.Lock()
		dashJobs = prev
		dashMu.Unlock()
	})
	h.afterStreamAuthChanged()
	if !cancelled {
		t.Fatal("running dash ffmpeg must stop after auth change")
	}
	dashMu.Lock()
	_, ok := dashJobs["zlm-1|live|cam"]
	dashMu.Unlock()
	if ok {
		t.Fatal("stale dash job must be dropped so EnsureDASH can restart with token")
	}
}

func TestScheduledSnapTargetsDeduplicatesSchemasAndSkipsAudioOnly(t *testing.T) {
	video := []any{map[string]any{"codec_type": float64(0), "ready": true}}
	audio := []any{map[string]any{"codec_type": float64(1), "ready": true}}
	rows := []map[string]any{
		{"app": "live", "stream": "cam", "schema": "rtmp", "tracks": video},
		{"app": "live", "stream": "cam", "schema": "hls", "tracks": video},
		{"app": "live", "stream": "radio", "schema": "rtmp", "tracks": audio},
		{"app": "", "stream": "broken", "schema": "rtmp", "tracks": video},
	}
	got := scheduledSnapTargets(rows)
	want := []snapTarget{{App: "live", Stream: "cam"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestReadJPEGStreamExtractsChunkedFrames(t *testing.T) {
	jpeg := func(fill byte) []byte {
		body := bytes.Repeat([]byte{fill}, minSnapBytes)
		return append(append([]byte{0xff, 0xd8}, body...), 0xff, 0xd9)
	}
	first, second := jpeg(0x11), jpeg(0x22)
	raw := append(append(append([]byte("noise"), first...), second...), 0xff, 0xd8, 0x33)
	reader := io.MultiReader(
		bytes.NewReader(raw[:17]),
		bytes.NewReader(raw[17:len(first)+9]),
		bytes.NewReader(raw[len(first)+9:]),
	)
	var got [][]byte
	if err := readJPEGStream(reader, func(frame []byte) error {
		got = append(got, append([]byte(nil), frame...))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, [][]byte{first, second}) {
		t.Fatalf("frames=%d sizes=%v", len(got), func() []int {
			out := make([]int, len(got))
			for i := range got {
				out[i] = len(got[i])
			}
			return out
		}())
	}
}

func TestPersistentSnapArgsUseImagePipeAtConfiguredInterval(t *testing.T) {
	args := persistentSnapArgs(config.Node{RTMPPort: 1936}, snapTarget{App: "live", Stream: "cam"}, 6)
	got := strings.Join(args, " ")
	for _, want := range []string{
		"-i rtmp://127.0.0.1:1936/live/cam",
		"-vf fps=1/6",
		"-f image2pipe",
		"pipe:1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("args missing %q: %s", want, got)
		}
	}
}

func TestPlanSnapWorkerSyncStartsStopsAndRestartsChangedIntervals(t *testing.T) {
	key := func(stream string) snapWorkerKey {
		return snapWorkerKey{NodeID: "zlm-1", App: "live", Stream: stream}
	}
	current := map[snapWorkerKey]int{
		key("keep"): 6, key("gone"): 6, key("changed"): 5,
	}
	wanted := []snapWorkerSpec{
		{Key: key("keep"), Interval: 6},
		{Key: key("new"), Interval: 6},
		{Key: key("changed"), Interval: 6},
	}
	start, stop := planSnapWorkerSync(current, wanted)
	started := map[snapWorkerKey]int{}
	for _, spec := range start {
		started[spec.Key] = spec.Interval
	}
	stopped := map[snapWorkerKey]bool{}
	for _, k := range stop {
		stopped[k] = true
	}
	if !reflect.DeepEqual(started, map[snapWorkerKey]int{key("new"): 6, key("changed"): 6}) {
		t.Fatalf("start=%+v", started)
	}
	if !reflect.DeepEqual(stopped, map[snapWorkerKey]bool{key("gone"): true, key("changed"): true}) {
		t.Fatalf("stop=%+v", stopped)
	}
}

func TestMergeUnsyncedSnapWorkersPreservesOnlyFailedConfiguredNodes(t *testing.T) {
	failed := snapWorkerKey{NodeID: "failed", App: "live", Stream: "keep"}
	removed := snapWorkerKey{NodeID: "removed", App: "live", Stream: "stop"}
	healthyOld := snapWorkerKey{NodeID: "healthy", App: "live", Stream: "old"}
	healthyNew := snapWorkerKey{NodeID: "healthy", App: "live", Stream: "new"}
	current := map[snapWorkerKey]int{failed: 6, removed: 6, healthyOld: 6}
	discovery := snapWorkerDiscovery{
		Specs:      []snapWorkerSpec{{Key: healthyNew, Interval: 6}},
		Configured: map[string]bool{"failed": true, "healthy": true},
		Synced:     map[string]bool{"healthy": true},
	}
	wanted := mergeUnsyncedSnapWorkers(current, discovery)
	start, stop := planSnapWorkerSync(current, wanted)
	if len(start) != 1 || start[0].Key != healthyNew {
		t.Fatalf("start=%+v", start)
	}
	stopped := map[snapWorkerKey]bool{}
	for _, key := range stop {
		stopped[key] = true
	}
	if !reflect.DeepEqual(stopped, map[snapWorkerKey]bool{removed: true, healthyOld: true}) {
		t.Fatalf("stop=%+v", stopped)
	}
}

func TestReconcileSnapWorkersCancelsRemovedWorker(t *testing.T) {
	started := make(chan snapWorkerKey, 1)
	stopped := make(chan snapWorkerKey, 1)
	h := &Hub{
		snapWorkerRun: func(ctx context.Context, spec snapWorkerSpec) error {
			started <- spec.Key
			<-ctx.Done()
			stopped <- spec.Key
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := snapWorkerSpec{
		Key:      snapWorkerKey{NodeID: "zlm-1", App: "live", Stream: "cam"},
		Node:     config.Node{ID: "zlm-1"},
		Target:   snapTarget{App: "live", Stream: "cam"},
		Interval: 6,
	}
	h.reconcileSnapWorkers(ctx, []snapWorkerSpec{spec})
	select {
	case got := <-started:
		if got != spec.Key {
			t.Fatalf("started=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	h.reconcileSnapWorkers(ctx, nil)
	select {
	case got := <-stopped:
		if got != spec.Key {
			t.Fatalf("stopped=%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("worker was not cancelled")
	}
}

func TestReconcileSnapWorkersWaitsForOldIntervalBeforeReplacement(t *testing.T) {
	events := make(chan string, 8)
	h := &Hub{
		snapWorkerRun: func(ctx context.Context, spec snapWorkerSpec) error {
			events <- fmt.Sprintf("start:%d", spec.Interval)
			<-ctx.Done()
			if spec.Interval == 5 {
				time.Sleep(80 * time.Millisecond)
			}
			events <- fmt.Sprintf("stop:%d", spec.Interval)
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := snapWorkerSpec{
		Key:    snapWorkerKey{NodeID: "zlm-1", App: "live", Stream: "cam"},
		Node:   config.Node{ID: "zlm-1"},
		Target: snapTarget{App: "live", Stream: "cam"},
	}
	base.Interval = 5
	h.reconcileSnapWorkers(ctx, []snapWorkerSpec{base})
	if got := <-events; got != "start:5" {
		t.Fatalf("first event=%q", got)
	}
	base.Interval = 6
	h.reconcileSnapWorkers(ctx, []snapWorkerSpec{base})
	for _, want := range []string{"stop:5", "start:6"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event=%q want=%q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("missing event %q", want)
		}
	}
	h.stopSnapWorkers()
}

func TestCappedLogWriterKeepsBoundedTail(t *testing.T) {
	w := newCappedLogWriter(5)
	_, _ = w.Write([]byte("abc"))
	_, _ = w.Write([]byte("defgh"))
	if got := w.String(); got != "defgh" {
		t.Fatalf("got %q", got)
	}
}

func TestScheduledSnapUsesAuthenticatedZLMClient(t *testing.T) {
	var gotSecret string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.URL.Query().Get("secret")
		_, _ = fmt.Fprint(w, `{"code":0,"data":[]}`)
	}))
	defer server.Close()

	oldConfig := config.C
	defer func() { config.C = oldConfig }()
	config.C = &config.Setup{Nodes: []config.Node{{ID: "zlm-1", API: server.URL, Secret: "api-secret"}}}

	h := &Hub{zlm: newZLM()}
	_ = h.discoverSnapWorkerSpecs()
	if gotSecret != "api-secret" {
		t.Fatalf("getMediaList secret=%q", gotSecret)
	}
}

func TestStartScriptStopsRelativeLegacySnapd(t *testing.T) {
	wd, _ := os.Getwd()
	var body []byte
	var err error
	for _, rel := range []string{
		filepath.Join("..", "control.sh"),
		filepath.Join("..", "..", "control.sh"),
		filepath.Join(wd, "..", "control.sh"),
		filepath.Join(wd, "..", "..", "control.sh"),
	} {
		body, err = os.ReadFile(rel)
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `pkill -f '(^|[[:space:]/])snapd\.sh([[:space:]]|$)'`) {
		t.Fatal("control.sh 未清退以 bash ./snapd.sh 或 bash snapd.sh 启动的遗留进程")
	}
}

func TestWriteScheduledSnapStoresArchiveAndLatest(t *testing.T) {
	root := t.TempDir()
	n := config.Node{MP4Save: filepath.Join(root, "mp4")}
	at := time.Date(2026, 8, 24, 14, 53, 51, 0, time.Local)
	body := []byte{0xff, 0xd8, 0xff, 0xd9}

	archive, err := writeScheduledSnap(n, "cam", at, body)
	if err != nil {
		t.Fatal(err)
	}
	if archive != snapArchivePath(n, "cam", at) {
		t.Fatalf("archive=%s", archive)
	}
	for _, path := range []string{archive, snapCoverPath(n, "cam")} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, body) {
			t.Fatalf("%s got %v", path, got)
		}
	}
}

func TestSnapCoverCapturePolicyReusesPersistentLatest(t *testing.T) {
	tests := []struct {
		enabled bool
		exists  bool
		want    bool
	}{
		{enabled: true, exists: true, want: false},
		{enabled: true, exists: false, want: true},
		{enabled: false, exists: true, want: true},
	}
	for _, tt := range tests {
		if got := needsSnapCapture(tt.enabled, tt.exists); got != tt.want {
			t.Fatalf("enabled=%v exists=%v got=%v want=%v", tt.enabled, tt.exists, got, tt.want)
		}
	}
}

func TestIsZLMSnapCacheDir(t *testing.T) {
	if !isZLMSnapCacheDir("705b044caa4a024df0ab807657beb208") {
		t.Fatal("md5 dir")
	}
	if isZLMSnapCacheDir("cam") || isZLMSnapCacheDir("705b044caa4a024df0ab807657beb20") {
		t.Fatal("not cache")
	}
}
func TestClassifyRelSnap(t *testing.T) {
	role, _, app, stream, date, proto := classifyRel("snap/cam/2026-08-21/161305.jpg", "161305.jpg", ".jpg")
	if role != "live_snap" || proto != "snap" || app != "" || stream != "cam" || date != "2026-08-21" {
		t.Fatalf("got role=%s proto=%s app=%s stream=%s date=%s", role, proto, app, stream, date)
	}
	role, _, app, stream, _, proto = classifyRel("snap/cam/latest.jpg", "latest.jpg", ".jpg")
	if role != "live_snap" || proto != "snap" || stream != "cam" {
		t.Fatalf("latest got role=%s proto=%s app=%s stream=%s", role, proto, app, stream)
	}
	role, _, app, stream, date, proto = classifyRel("snap/live/cam/2026-09-03/133455.jpg", "133455.jpg", ".jpg")
	if role != "live_snap" || proto != "snap" || app != "live" || stream != "cam" || date != "2026-09-03" {
		t.Fatalf("app/stream layout got role=%s proto=%s app=%s stream=%s date=%s", role, proto, app, stream, date)
	}
	role, _, app, stream, _, proto = classifyRel("/data/zlm/snap/ls_mix_proxy/2026-09-03/133455.jpg", "133455.jpg", ".jpg")
	if role != "live_snap" || stream != "ls_mix_proxy" || app != "" {
		t.Fatalf("abs snap path got role=%s app=%s stream=%s", role, app, stream)
	}
}

func TestListMediaFilesFindsSnapWhenOtherStreamDirsExist(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "live", "cam"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "live", "cam", "hls.m3u8"), []byte("#EXTM3U\n"), 0644); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(root, "snap", "cam", "2026-09-03")
	if err := os.MkdirAll(snap, 0755); err != nil {
		t.Fatal(err)
	}
	jpg := filepath.Join(snap, "133455.jpg")
	if err := os.WriteFile(jpg, []byte("jpeg"), 0644); err != nil {
		t.Fatal(err)
	}
	n := config.Node{WWW: root, MP4Save: filepath.Join(root, "mp4")}
	files, _, _, err := listMediaFiles(n, fileListOpt{App: "live", Stream: "cam", Kind: "snap"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name != "133455.jpg" || files[0].Proto != "snap" || files[0].Stream != "cam" {
		t.Fatalf("snap files=%+v", files)
	}
	groups := groupFileProtocols(files, nil)
	found := false
	for _, g := range groups {
		if asString(g["id"]) != "snap" {
			continue
		}
		found = true
		list, _ := g["files"].([]MediaFile)
		if len(list) != 1 || list[0].Name != "133455.jpg" {
			t.Fatalf("snap group=%+v", g)
		}
	}
	if !found {
		t.Fatal("records groups missing snap")
	}
}
