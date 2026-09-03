package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"zlm-admin/core/config"
)

func TestApplyProxyCORS(t *testing.T) {
	h := http.Header{}
	applyProxyCORS(h, "http://10.191.6.5:7788")
	if h.Get("Access-Control-Allow-Origin") != "http://10.191.6.5:7788" {
		t.Fatalf("origin=%s", h.Get("Access-Control-Allow-Origin"))
	}
	if h.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatal("missing credentials")
	}
	h = http.Header{}
	applyProxyCORS(h, "")
	if h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("star=%s", h.Get("Access-Control-Allow-Origin"))
	}
}

func TestRewriteZLMCookiePath(t *testing.T) {
	prefix := zlmProxyPrefix("zlm-1")
	got := rewriteZLMCookiePath(
		"ZLM_HTTP_COOKIE=abc;expires=Fri, Aug 21 2026 02:58:43 GMT;path=/live/ls_zlm_h264_1080p/",
		prefix,
	)
	want := "ZLM_HTTP_COOKIE=abc; expires=Fri, Aug 21 2026 02:58:43 GMT; Path=/api/node/zlm-1/zlm/live/ls_zlm_h264_1080p/"
	if got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	got = rewriteZLMCookiePath("ZLM_HTTP_COOKIE=abc", prefix)
	if !strings.Contains(got, "Path=/api/node/zlm-1/zlm/") {
		t.Fatalf("missing path: %s", got)
	}
	got = rewriteZLMCookiePath("ZLM_HTTP_COOKIE=abc; Path=/api/node/zlm-1/zlm/live/cam/", prefix)
	if !strings.Contains(got, "Path=/api/node/zlm-1/zlm/live/cam/") || strings.Count(got, "/api/node/zlm-1/zlm/api/node") != 0 {
		t.Fatalf("double prefix: %s", got)
	}
}

func TestResolveLiveURI(t *testing.T) {
	got := resolveLiveURI("zlm-1", "live/cam", "seg-0.m4s")
	want := "/api/node/zlm-1/zlm/live/cam/seg-0.m4s"
	if got != want {
		t.Fatalf("rel: got %s want %s", got, want)
	}
	got = resolveLiveURI("zlm-1", "live/cam", "http://127.0.0.1:8090/live/cam/init.mp4")
	want = "/api/node/zlm-1/zlm/live/cam/init.mp4"
	if got != want {
		t.Fatalf("abs: got %s want %s", got, want)
	}
}

func TestZlmPlayPath(t *testing.T) {
	got := zlmPlayPath("zlm-1", "live/ls_zlm_h264_1080p", "hls.fmp4.m3u8")
	want := "/api/node/zlm-1/zlm/live/ls_zlm_h264_1080p/hls.fmp4.m3u8"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestPlayLinksPublicZLM(t *testing.T) {
	n := config.Node{ID: "zlm-1", HTTPPort: 8090, RTSPPort: 554, RTMPPort: 1935}
	links := playLinks("10.191.6.5", n, "__defaultVhost__", "live", "ls_zlm_h264_1080p")
	got := map[string]string{}
	for _, l := range links {
		got[l.ID] = l.URL
	}
	want := map[string]string{
		"hls":       "http://10.191.6.5:8090/live/ls_zlm_h264_1080p/hls.m3u8",
		"hls-fmp4":  "http://10.191.6.5:8090/live/ls_zlm_h264_1080p/hls.fmp4.m3u8",
		"http-fmp4": "http://10.191.6.5:8090/live/ls_zlm_h264_1080p.live.mp4",
		"http-flv":  "http://10.191.6.5:8090/live/ls_zlm_h264_1080p.live.flv",
		"http-ts":   "http://10.191.6.5:8090/live/ls_zlm_h264_1080p.live.ts",
		"ws-ts":     "ws://10.191.6.5:8090/live/ls_zlm_h264_1080p.live.ts",
		"ws-fmp4":   "ws://10.191.6.5:8090/live/ls_zlm_h264_1080p.live.mp4",
		"rtmp":      "rtmp://10.191.6.5:1935/live/ls_zlm_h264_1080p",
		"rtsp":      "rtsp://10.191.6.5:554/live/ls_zlm_h264_1080p",
		"gb28181":   "sip:10.191.6.5:5060/live/ls_zlm_h264_1080p",
		"dash":      "http://10.191.6.5:8090/live/ls_zlm_h264_1080p/dash.mpd",
	}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("%s: got %s want %s", id, got[id], w)
		}
	}
	web, native := 0, 0
	var dash PlayLink
	for _, l := range links {
		if l.WebPlay {
			web++
		} else {
			native++
		}
		if l.ID == "dash" {
			dash = l
		}
	}
	if web < 1 || native < 4 {
		t.Fatalf("web=%d native=%d", web, native)
	}
	cmd := ""
	for _, x := range dash.Extra {
		if x["label"] == "FFmpeg 命令" {
			cmd = x["url"]
		}
	}
	if !strings.Contains(cmd, " -i rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p") {
		t.Fatalf("dash cmd: %s extras=%+v", cmd, dash.Extra)
	}
	if !strings.Contains(cmd, "stream$RepresentationID$/chunk-stream") || !strings.Contains(cmd, "/stream0") {
		t.Fatalf("dash media dir: %s", cmd)
	}
}

func TestProxyZLMBlocksManagementAPIPaths(t *testing.T) {
	var upstreamHits int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	oldConfig := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "node-1", API: upstream.URL}}}
	defer func() { config.C = oldConfig }()
	h := &Hub{}

	for _, rel := range []string{
		"index/api/getServerConfig",
		"live/../index/api/getServerConfig",
		"index%2Fapi%2FgetServerConfig",
		"index%252Fapi%252FgetServerConfig",
	} {
		t.Run(rel, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/node/node-1/zlm/"+rel, nil)
			h.ProxyZLM(rec, req, "node-1", rel)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if upstreamHits != 0 {
		t.Fatalf("blocked requests reached upstream %d times", upstreamHits)
	}
}

func TestProxyZLMAllowsMediaPath(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live/camera/segment.ts" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("media"))
	}))
	defer upstream.Close()

	oldConfig := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "node-1", API: upstream.URL}}}
	defer func() { config.C = oldConfig }()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/node/node-1/zlm/live/camera/segment.ts", nil)
	(&Hub{}).ProxyZLM(rec, req, "node-1", "live/camera/segment.ts")
	if rec.Code != http.StatusOK || rec.Body.String() != "media" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
