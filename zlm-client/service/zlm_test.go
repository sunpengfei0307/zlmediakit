package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"zlm-admin/core/config"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestZLMTransportErrorsNeverExposeSecretURL(t *testing.T) {
	const secret = "top-secret-value"
	client := &zlmClient{http: &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed secret=" + secret + " https://upstream.invalid/?secret=" + secret)
	})}}
	node := config.Node{API: "http://zlm.invalid:8090", Secret: secret}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			var err error
			if method == http.MethodGet {
				_, err = client.call(node, "listStreamProxy", nil)
			} else {
				_, err = client.callPOST(node, "addStreamProxy", url.Values{"app": {"live"}})
			}
			if err == nil {
				t.Fatal("expected transport error")
			}
			text := err.Error()
			if strings.Contains(text, secret) || strings.Contains(strings.ToLower(text), "secret=") {
				t.Fatalf("transport error leaked secret: %q", text)
			}
		})
	}
}

func TestSanitizeZLMTransportErrorHandlesArbitraryErrorText(t *testing.T) {
	err := sanitizeZLMTransportError(config.Node{Secret: "abc123"}, errors.New("failed ?secret=abc123 and abc123"))
	if err == nil || strings.Contains(err.Error(), "abc123") || strings.Contains(err.Error(), "secret=") {
		t.Fatalf("sanitized error=%v", err)
	}
}

func TestZLMCallPOSTWithTimeoutOverridesSharedClientTimeout(t *testing.T) {
	var remaining time.Duration
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("request context has no deadline")
		}
		remaining = time.Until(deadline)
		if r.Method != http.MethodPost || r.URL.Query().Get("secret") != "node-secret" {
			t.Fatalf("request=%s %s", r.Method, r.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":[]}`)),
			Header:     make(http.Header),
		}, nil
	})
	client := &zlmClient{http: &http.Client{Transport: transport, Timeout: time.Millisecond}}
	_, err := client.callPOSTWithTimeout(context.Background(), config.Node{
		API: "http://zlm.invalid", Secret: "node-secret",
	}, "searchOnvifDevice", url.Values{"timeout_ms": {"10000"}}, 11*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if remaining < 10*time.Second {
		t.Fatalf("shared client timeout truncated deadline: remaining=%s", remaining)
	}
}

func TestZLMCallPOSTWithTimeoutCancelsWithoutLongWait(t *testing.T) {
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	client := &zlmClient{http: &http.Client{Transport: transport, Timeout: 4 * time.Second}}
	start := time.Now()
	_, err := client.callPOSTWithTimeout(context.Background(), config.Node{
		API: "http://zlm.invalid", Secret: "node-secret",
	}, "searchOnvifDevice", nil, 15*time.Millisecond)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("deadline cancellation took %s", elapsed)
	}
}

func TestZLMCallsRejectHTTPAndZLMErrors(t *testing.T) {
	tests := []struct {
		name   string
		method string
		status int
		body   string
	}{
		{name: "get http error", method: http.MethodGet, status: http.StatusBadGateway, body: `{"code":0}`},
		{name: "get empty response", method: http.MethodGet, status: http.StatusOK, body: ``},
		{name: "get plain non json", method: http.MethodGet, status: http.StatusOK, body: `upstream unavailable`},
		{name: "post zlm error", method: http.MethodPost, status: http.StatusOK, body: `{"code":-1,"msg":"denied"}`},
		{name: "post invalid json", method: http.MethodPost, status: http.StatusOK, body: `<html>bad gateway</html>`},
		{name: "post http error", method: http.MethodPost, status: http.StatusBadGateway, body: `{"code":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			client := &zlmClient{http: srv.Client()}
			node := config.Node{API: srv.URL, Secret: "secret"}
			var err error
			if tt.method == http.MethodPost {
				_, err = client.callPOST(node, "testAPI", url.Values{"key": {"value"}})
			} else {
				_, err = client.call(node, "testAPI", nil)
			}
			if err == nil {
				t.Fatal("expected validated ZLM call to return an error")
			}
		})
	}
}

func TestRecordsUsesRuntimeMP4RecordEndpoint(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
	}))
	defer srv.Close()

	h := &Hub{zlm: &zlmClient{http: srv.Client()}}
	h.records(config.Node{API: srv.URL, Root: t.TempDir()}, url.Values{
		"app": {"live"}, "stream": {"camera"},
	}, "127.0.0.1")

	mu.Lock()
	defer mu.Unlock()
	for _, got := range paths {
		if got == "/index/api/getMP4RecordFile" {
			return
		}
	}
	t.Fatalf("getMP4RecordFile was not called; paths=%s", strings.Join(paths, ", "))
}

func TestSetServerConfigPropagatesHTTPErrorWithJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":0,"detail":"gateway failure"}`))
	}))
	defer srv.Close()

	oldConfig := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "node-1", API: srv.URL}}}
	defer func() { config.C = oldConfig }()
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}

	got := h.setServerConfig(config.C.Nodes[0], []byte(`{"protocol.mp4_max_second":"60"}`))
	if got["code"] != -1 {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(asString(got["msg"]), "HTTP 502") {
		t.Fatalf("missing propagated HTTP error: %+v", got)
	}
}

func TestStartRecordStopsWhenMP4SegmentConfigFails(t *testing.T) {
	var startCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index/api/setServerConfig":
			_, _ = w.Write([]byte(`{"code":-1,"msg":"config denied"}`))
		case "/index/api/startRecord":
			startCalls++
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	oldConfig := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "node-1", API: srv.URL}}}
	defer func() { config.C = oldConfig }()
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.RecordVODOperation("node-1", "admin", "startRecord", url.Values{
		"app": {"live"}, "stream": {"camera"}, "kind": {"mp4"}, "max_second": {"60"},
	})
	if got["code"] != -1 || !strings.Contains(asString(got["msg"]), "setServerConfig") {
		t.Fatalf("result=%+v", got)
	}
	if startCalls != 0 {
		t.Fatalf("startRecord called %d times after config failure", startCalls)
	}
	if len(audit.List()) != 2 || audit.List()[1].Success {
		t.Fatalf("audit=%+v", audit.List())
	}
}

func TestRecordActionPropagatesErrorAndKeepsZLMResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"code":0,"detail":"upstream response"}`))
	}))
	defer srv.Close()

	oldConfig := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "node-1", API: srv.URL}}}
	defer func() { config.C = oldConfig }()
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.RecordVODOperation("node-1", "admin", "startRecord", url.Values{
		"app": {"live"}, "stream": {"camera"}, "kind": {"hls"},
	})
	if got["code"] != -1 || !strings.Contains(asString(got["msg"]), "startRecord") {
		t.Fatalf("result=%+v", got)
	}
	if _, ok := got["zlm_response"]; ok {
		t.Fatalf("raw upstream response must not escape audited boundary: %+v", got)
	}
	if len(audit.List()) != 2 || audit.List()[1].Success {
		t.Fatalf("audit=%+v", audit.List())
	}
}

func TestGroupMediaPrefersOriginTracksOverStaleMuxer(t *testing.T) {
	stale := []any{map[string]any{
		"codec_type": float64(0), "codec_id_name": "H265", "width": float64(1920), "height": float64(1080),
		"fps": float64(25), "ready": true,
	}}
	fresh := []any{map[string]any{
		"codec_type": float64(0), "codec_id_name": "H264", "width": float64(1280), "height": float64(720),
		"fps": float64(25), "ready": true,
	}}
	rows := []map[string]any{
		{"schema": "hls", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push", "tracks": stale},
		{"schema": "fmp4", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push", "tracks": stale},
		{"schema": "rtmp", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push", "tracks": fresh},
		{"schema": "flv", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push", "tracks": stale},
	}
	got := groupMedia(rows)
	if len(got) != 1 {
		t.Fatalf("groups=%d", len(got))
	}
	if asString(got[0]["video_codec"]) != "H264" || asFloat(got[0]["width"]) != 1280 || asFloat(got[0]["height"]) != 720 {
		t.Fatalf("stale muxer tracks won: codec=%v %vx%v", got[0]["video_codec"], got[0]["width"], got[0]["height"])
	}

	rev := []map[string]any{rows[2], rows[0], rows[1], rows[3]}
	got = groupMedia(rev)
	if asString(got[0]["video_codec"]) != "H264" || asFloat(got[0]["height"]) != 720 {
		t.Fatalf("origin first then stale muxer overwrote: %+v", got[0])
	}
}

func TestGroupMediaKeepsOriginAliveSecondAfterRepublish(t *testing.T) {
	rows := []map[string]any{
		{"schema": "hls", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push",
			"aliveSecond": 3600.0, "bytesSpeed": 100.0, "totalBytes": 999999.0},
		{"schema": "rtmp", "vhost": "__defaultVhost__", "app": "live", "stream": "cam", "originTypeStr": "rtmp_push",
			"aliveSecond": 8.0, "bytesSpeed": 5000.0, "totalBytes": 4000.0,
			"originSock": map[string]any{"peer_ip": "10.0.0.2", "peer_port": "45882", "identifier": "new-pub"}},
	}
	got := groupMedia(rows)
	if len(got) != 1 || asFloat(got[0]["aliveSecond"]) != 8 {
		t.Fatalf("must not keep lingering muxer duration: %+v", got)
	}
	if asFloat(got[0]["in_bps"]) != 5000 || asFloat(got[0]["read_size"]) != 4000 {
		t.Fatalf("must use origin io: in=%v read=%v", got[0]["in_bps"], got[0]["read_size"])
	}
	rev := []map[string]any{rows[1], rows[0]}
	got = groupMedia(rev)
	if asFloat(got[0]["aliveSecond"]) != 8 {
		t.Fatalf("origin first then stale muxer duration won: %+v", got[0])
	}
}

func TestOverlayPublisherStatsUsesCurrentPushSession(t *testing.T) {
	streams := []map[string]any{{
		"app": "live", "stream": "cam",
		"aliveSecond": 3600.0, "in_bytes": 9e6, "read_size": 9e6, "in_bps": 100.0,
		"origin_peer": "1.1.1.1:1111",
		"originSock":  map[string]any{"identifier": "new-pub"},
	}}
	sessions := []map[string]any{{
		"id": "new-pub", "identifier": "new-pub",
		"app": "live", "stream": "cam", "is_publisher": true,
		"aliveSecond": 7.0, "peer_ip": "10.0.0.2", "peer_port": "45882",
		"totalBytes": 1234.0, "bytesSpeed": 8000.0,
	}}
	overlayPublisherStats(streams, sessions)
	if asFloat(streams[0]["aliveSecond"]) != 7 {
		t.Fatalf("duration not reset to current push session: %+v", streams[0])
	}
	if asString(streams[0]["origin_peer"]) != "10.0.0.2:45882" {
		t.Fatalf("peer=%v", streams[0]["origin_peer"])
	}
	if asFloat(streams[0]["read_size"]) != 1234 || asFloat(streams[0]["in_bps"]) != 8000 {
		t.Fatalf("io not taken from current session: %+v", streams[0])
	}
}

func TestRefreshGroupedTracksReparsesFromGetMediaInfo(t *testing.T) {
	var infoHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/index/api/getMediaInfo" {
			infoHits++
			if r.URL.Query().Get("schema") != "rtmp" || r.URL.Query().Get("stream") != "cam" {
				t.Errorf("getMediaInfo query=%s", r.URL.RawQuery)
			}
			if r.Header.Get("Cache-Control") != "no-cache" {
				t.Errorf("getMediaInfo missing no-cache header")
			}
			_, _ = w.Write([]byte(`{"code":0,"tracks":[{"codec_type":0,"codec_id_name":"H264","width":1280,"height":720,"fps":25,"ready":true}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()

	g := map[string]any{
		"vhost": "__defaultVhost__", "app": "live", "stream": "cam",
		"origin_schema": "rtmp", "originTypeStr": "rtmp_push",
		"video_codec": "H264", "width": 1920.0, "height": 1080.0, "fps": 25.0,
		"schemas": []string{"hls", "rtmp", "fmp4"},
	}
	(&zlmClient{http: srv.Client()}).refreshGroupedTracks(config.Node{API: srv.URL}, []map[string]any{g})
	if infoHits == 0 {
		t.Fatal("refresh must call getMediaInfo")
	}
	if asString(g["video_codec"]) != "H264" || asFloat(g["width"]) != 1280 || asFloat(g["height"]) != 720 {
		t.Fatalf("stale list tracks kept: codec=%v %vx%v", g["video_codec"], g["width"], g["height"])
	}
}

func TestNodeActionDetailReparsesTracksFromGetMediaInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/index/api/getMediaList":
			_, _ = w.Write([]byte(`{"code":0,"data":[
				{"schema":"hls","vhost":"__defaultVhost__","app":"live","stream":"cam","originTypeStr":"rtmp_push",
				 "tracks":[{"codec_type":0,"codec_id_name":"H264","width":1920,"height":1080,"fps":25,"ready":true}]},
				{"schema":"rtmp","vhost":"__defaultVhost__","app":"live","stream":"cam","originTypeStr":"rtmp_push",
				 "tracks":[{"codec_type":0,"codec_id_name":"H264","width":1920,"height":1080,"fps":25,"ready":true}]}
			]}`))
		case "/index/api/getMediaInfo":
			_, _ = w.Write([]byte(`{"code":0,"tracks":[{"codec_type":0,"codec_id_name":"H264","width":1280,"height":720,"fps":25,"gop_interval_ms":2000,"ready":true}]}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		}
	}))
	defer srv.Close()

	old := config.C
	config.C = &config.Setup{Nodes: []config.Node{{ID: "zlm-1", API: srv.URL}}}
	defer func() { config.C = old }()

	raw, status, _ := (&Hub{zlm: &zlmClient{http: srv.Client()}, online: map[string]bool{}}).NodeAction("zlm-1", "detail", "127.0.0.1", url.Values{}, nil)
	if status != 200 {
		t.Fatalf("status=%d", status)
	}
	detail, _ := raw.(map[string]any)
	streams, _ := detail["streams"].([]map[string]any)
	if len(streams) != 1 {
		t.Fatalf("streams=%v", detail["streams"])
	}
	if asFloat(streams[0]["width"]) != 1280 || asFloat(streams[0]["height"]) != 720 {
		t.Fatalf("detail streams kept stale size: %+v", streams[0])
	}
}

func TestAnnotateSessionBindsPullFromPlayerList(t *testing.T) {
	streams := []map[string]any{{
		"app": "live", "stream": "cam", "origin_peer": "10.0.0.8:1000",
		"originSock": map[string]any{"identifier": "pub-1"},
	}}
	pub := map[string]any{"id": "pub-1", "identifier": "pub-1", "typeid": "mediakit::RtmpSession", "peer_ip": "10.0.0.8", "peer_port": "1000"}
	pull := map[string]any{"id": "ply-9", "identifier": "ply-9", "typeid": "mediakit::RtmpSession", "peer_ip": "127.0.0.1", "peer_port": "40472"}
	httpPlay := map[string]any{"id": "http-3", "identifier": "http-3", "typeid": "mediakit::HttpSession", "peer_ip": "10.0.0.9", "peer_port": "9", "local_port": 8090}
	players := map[string][2]string{
		"ident:ply-9":   {"live", "cam"},
		"peer:10.0.0.9:9": {"live", "cam"},
	}
	annotateSession(pub, streams, players)
	annotateSession(pull, streams, players)
	annotateSession(httpPlay, streams, players)
	if asString(pub["role"]) != "推流" || asString(pub["media_key"]) != "live/cam" {
		t.Fatalf("publisher: %+v", pub)
	}
	if asString(pull["role"]) != "拉流" || asString(pull["media_key"]) != "live/cam" {
		t.Fatalf("pull missing media: %+v", pull)
	}
	if asString(httpPlay["role"]) != "拉流" || asString(httpPlay["app"]) != "live" {
		t.Fatalf("http player should bind stream: %+v", httpPlay)
	}
}
