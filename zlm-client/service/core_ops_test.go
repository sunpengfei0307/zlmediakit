package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"zlm-admin/core/config"
)

type recordingAudit struct {
	mu      sync.Mutex
	entries []AuditEntry
}

type failingAudit struct {
	mu      sync.Mutex
	failOn  int
	calls   int
	entries []AuditEntry
}

func (a *failingAudit) Record(entry AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.calls == a.failOn {
		return errors.New("audit disk unavailable")
	}
	a.entries = append(a.entries, entry)
	return nil
}

func (a *failingAudit) List() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEntry(nil), a.entries...)
}

func (a *recordingAudit) Record(entry AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.entries = append(a.entries, entry)
	return nil
}

func (a *recordingAudit) List() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEntry(nil), a.entries...)
}

func withTestNode(t *testing.T, node config.Node) {
	t.Helper()
	old := config.C
	config.C = &config.Setup{Nodes: []config.Node{node}}
	t.Cleanup(func() { config.C = old })
}

func TestOverviewMapsVersionAndCachesIt(t *testing.T) {
	var mu sync.Mutex
	versionCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/index/api/version":
			mu.Lock()
			versionCalls++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{"buildTime":"2026-08-20","branchName":"main","commitHash":"abc123"}}`))
		case "/index/api/getStatistic":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		}
	}))
	defer srv.Close()

	node := config.Node{ID: "node-1", Name: "ZLM", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, online: map[string]bool{}}

	for i := 0; i < 2; i++ {
		overview := h.Overview()
		nodes := overview["nodes"].([]nodeOut)
		if len(nodes) != 1 {
			t.Fatalf("nodes=%d", len(nodes))
		}
		got := nodes[0]
		if !got.Online {
			t.Fatalf("node unexpectedly offline: %+v", got)
		}
		if got.BuildTime != "2026-08-20" || got.BranchName != "main" || got.CommitHash != "abc123" {
			t.Fatalf("version mapping=%+v", got)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if versionCalls != 1 {
		t.Fatalf("version calls=%d want=1", versionCalls)
	}
}

func TestOverviewVersionFailureDoesNotMarkNodeOffline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/index/api/version" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":0}`))
			return
		}
		if r.URL.Path == "/index/api/getStatistic" {
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
	}))
	defer srv.Close()

	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, online: map[string]bool{}}
	got := h.Overview()["nodes"].([]nodeOut)[0]
	if !got.Online {
		t.Fatalf("version failure changed online state: %+v", got)
	}
	if got.VersionError == "" {
		t.Fatal("version error should be visible")
	}
}

func TestApplyLiveStatsFromGroupedAndStatistic(t *testing.T) {
	out := nodeOut{Statistic: map[string]any{
		"MediaSource": 8, "MultiMediaSourceMuxer": 2, "TcpSession": 5,
		"UdpSession": 1, "Socket": 9, "Buffer": 20, "Frame": 3,
		"RtpPacket": 4, "RtmpPacket": 6, "TcpServer": 3, "UdpServer": 2,
	}}
	applyLiveStats(&out, []map[string]any{
		{
			"totalReaderCount": 3.0, "in_bps": 1000.0, "out_bps": 3000.0, "bytesSpeed": 1000.0,
			"isRecordingMP4": true, "status": "active", "schemas": []string{"rtmp", "rtsp"},
		},
		{
			"totalReaderCount": 0.0, "in_bps": 500.0, "out_bps": 0.0, "bytesSpeed": 500.0,
			"isRecordingHLS": false, "status": "wait", "schemas": []string{"rtmp"},
		},
	})
	if out.Viewers != 3 || out.InBps != 1500 || out.OutBps != 3000 || out.Recording != 1 || out.Waiting != 1 {
		t.Fatalf("live stats=%+v", out)
	}
	if out.MediaSource != 8 || out.TcpSession != 5 || out.RtpPacket != 4 || out.Muxer != 2 {
		t.Fatalf("object stats=%+v", out)
	}
	if len(out.Protocols) != 2 || out.Protocols[0].Name != "rtmp" || out.Protocols[0].Count != 2 {
		t.Fatalf("protocols=%v", out.Protocols)
	}
}

func TestStreamConnsCallsMediaStatusWithGroupedOriginSchema(t *testing.T) {
	var mu sync.Mutex
	params := map[string]url.Values{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/index/api/getMediaList":
			_, _ = w.Write([]byte(`{"code":0,"data":[{"schema":"rtmp","vhost":"__defaultVhost__","app":"live","stream":"cam","originTypeStr":"rtmp_push","tracks":[]}]}`))
		case "/index/api/getStatistic":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/index/api/isMediaOnline":
			mu.Lock()
			params[r.URL.Path] = r.Form
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"online":true}`))
		case "/index/api/getMediaInfo":
			mu.Lock()
			params[r.URL.Path] = r.Form
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"schema":"rtmp","vhost":"__defaultVhost__","app":"live","stream":"cam","readerCount":2,"totalReaderCount":3,"bytesSpeed":4096,"aliveSecond":12,"msg":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		}
	}))
	defer srv.Close()

	h := &Hub{zlm: &zlmClient{http: srv.Client()}, online: map[string]bool{}}
	got := h.streamConns(config.Node{ID: "node-1", API: srv.URL}, "__defaultVhost__", "live", "cam")
	if online, ok := got["media_online"].(bool); !ok || !online || got["media_online_known"] != true {
		t.Fatalf("missing media detail: %+v", got)
	}
	info, ok := got["media_info"].(map[string]any)
	if !ok || info["schema"] != "rtmp" || asFloat(info["readerCount"]) != 2 ||
		info["code"] != nil || info["msg"] != nil {
		t.Fatalf("media_info=%+v", got["media_info"])
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range []string{"/index/api/isMediaOnline", "/index/api/getMediaInfo"} {
		q := params[path]
		if q.Get("schema") != "rtmp" || q.Get("vhost") != "__defaultVhost__" || q.Get("app") != "live" || q.Get("stream") != "cam" {
			t.Fatalf("%s params=%v", path, q)
		}
	}
}

func TestCloseStreamsRecordsAudit(t *testing.T) {
	audit := &recordingAudit{}
	var method string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = r.ParseForm()
		form = r.Form
		_, _ = w.Write([]byte(`{"code":0,"count_closed":2}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) != 0 || method != http.MethodPost {
		t.Fatalf("result=%+v method=%s", got, method)
	}
	if form.Get("vhost") != "__defaultVhost__" || form.Get("app") != "live" || form.Get("stream") != "cam" || form.Get("force") != "1" {
		t.Fatalf("form=%v", form)
	}
	entries := audit.List()
	if len(entries) != 2 || !strings.Contains(entries[0].Message, "开始") ||
		entries[1].Action != "close_streams" || entries[1].User != "admin" ||
		entries[1].Node != "node-1" || !entries[1].Success || entries[1].Timestamp.IsZero() {
		t.Fatalf("audit=%+v", entries)
	}
	if !strings.Contains(entries[1].Target, "live/cam") {
		t.Fatalf("target=%q", entries[1].Target)
	}
}

func TestCloseStreamStandardSuccessRecordsSuccessfulAudit(t *testing.T) {
	audit := &recordingAudit{}
	var method string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_ = r.ParseForm()
		form = r.Form
		_, _ = w.Write([]byte(`{"code":0,"result":0,"msg":"success"}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "close_stream", url.Values{
		"schema": {"rtmp"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) != 0 || asFloat(got["result"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if method != http.MethodPost || form.Get("schema") != "rtmp" || form.Get("force") != "1" {
		t.Fatalf("method=%s form=%v", method, form)
	}
	entries := audit.List()
	if len(entries) != 2 || !strings.Contains(entries[0].Message, "开始") ||
		entries[1].Action != "close_stream" || !entries[1].Success {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestKickSessionsRejectsEmptyFilters(t *testing.T) {
	audit := &recordingAudit{}
	h := &Hub{audit: audit}
	withTestNode(t, config.Node{ID: "node-1"})

	got := h.CoreOperation("node-1", "admin", "kick_sessions", url.Values{})
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "过滤") {
		t.Fatalf("result=%+v", got)
	}
	if len(audit.List()) != 2 || audit.List()[1].Success {
		t.Fatalf("rejected operation audit=%+v", audit.List())
	}
}

func TestKickSessionsSuccessRecordsAudit(t *testing.T) {
	audit := &recordingAudit{}
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.Form
		_, _ = w.Write([]byte(`{"code":0,"count_hit":3}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "operator", "kick_sessions", url.Values{
		"peer_ip": {"10.0.0.8"}, "local_port": {"1935"},
	})
	if asFloat(got["code"]) != 0 || form.Get("peer_ip") != "10.0.0.8" || form.Get("local_port") != "1935" {
		t.Fatalf("result=%+v form=%v", got, form)
	}
	entries := audit.List()
	if len(entries) != 2 || !strings.Contains(entries[0].Message, "开始") ||
		entries[1].Action != "kick_sessions" || entries[1].User != "operator" || !entries[1].Success {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestKickSessionRecordsAudit(t *testing.T) {
	audit := &recordingAudit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "kick_session", url.Values{"id": {"session-1"}})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	entries := audit.List()
	if len(entries) != 2 || !strings.Contains(entries[0].Message, "开始") ||
		entries[1].Action != "kick_session" || entries[1].Target != "session-1" || !entries[1].Success {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestKickSessionsRejectsInvalidLocalPortWithoutCallingZLM(t *testing.T) {
	tests := []string{"abc", "0", "-1", "65536"}
	for _, port := range tests {
		t.Run(port, func(t *testing.T) {
			audit := &recordingAudit{}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = w.Write([]byte(`{"code":0,"count_hit":1}`))
			}))
			defer srv.Close()
			node := config.Node{ID: "node-1", API: srv.URL}
			withTestNode(t, node)
			h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

			got := h.CoreOperation("node-1", "admin", "kick_sessions", url.Values{"local_port": {port}})
			if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "1..65535") {
				t.Fatalf("port=%q result=%+v", port, got)
			}
			if calls != 0 {
				t.Fatalf("port=%q upstream calls=%d", port, calls)
			}
			entries := audit.List()
			if len(entries) != 2 || entries[1].Success {
				t.Fatalf("port=%q audit=%+v", port, entries)
			}
		})
	}
}

func TestKickSessionsAllowsPeerIPWithoutLocalPort(t *testing.T) {
	audit := &recordingAudit{}
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.Form
		_, _ = w.Write([]byte(`{"code":0,"count_hit":1}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "kick_sessions", url.Values{"peer_ip": {"10.0.0.8"}})
	if asFloat(got["code"]) != 0 || form.Get("peer_ip") != "10.0.0.8" || form.Get("local_port") != "" {
		t.Fatalf("result=%+v form=%v", got, form)
	}
}

func TestCoreOperationWithoutAuditDoesNotCallZLM(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"count_closed":1}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}

	got := h.CoreOperation("node-1", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "审计") {
		t.Fatalf("result=%+v", got)
	}
	if calls != 0 {
		t.Fatalf("upstream calls=%d", calls)
	}
}

func TestUnknownNodeOperationRecordsFailedAudit(t *testing.T) {
	audit := &recordingAudit{}
	withTestNode(t, config.Node{ID: "node-1"})
	h := &Hub{audit: audit}

	got := h.CoreOperation("missing", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) == 0 {
		t.Fatalf("result=%+v", got)
	}
	entries := audit.List()
	if len(entries) != 1 {
		t.Fatalf("audit=%+v", entries)
	}
	entry := entries[0]
	if entry.Node != "missing" || entry.User != "admin" || entry.Action != "close_streams" ||
		!strings.Contains(entry.Target, "live/cam") || entry.Success || entry.Message == "" || entry.Timestamp.IsZero() {
		t.Fatalf("audit=%+v", entry)
	}
}

func TestBulkOperationsTreatZeroHitsAsFailure(t *testing.T) {
	audit := &recordingAudit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/close_streams") {
			_, _ = w.Write([]byte(`{"code":0,"count_closed":0}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"count_hit":0}`))
	}))
	defer srv.Close()
	node := config.Node{ID: "node-1", API: srv.URL}
	withTestNode(t, node)
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	closeResult := h.CoreOperation("node-1", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	kickResult := h.CoreOperation("node-1", "admin", "kick_sessions", url.Values{"peer_ip": {"10.0.0.8"}})
	for action, got := range map[string]map[string]any{"close_streams": closeResult, "kick_sessions": kickResult} {
		if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "未命中") {
			t.Fatalf("%s result=%+v", action, got)
		}
	}
	entries := audit.List()
	if len(entries) != 4 || entries[1].Success || entries[3].Success {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestCoreOperationAuditPrewriteFailurePreventsUpstreamMutation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"count_closed":1}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &failingAudit{failOn: 1}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "审计") || calls != 0 {
		t.Fatalf("result=%+v calls=%d audit=%+v", got, calls, audit.List())
	}
}

func TestCoreOperationFinalAuditFailureReportsUncertainExecution(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"count_closed":1}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &failingAudit{failOn: 2}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.CoreOperation("node-1", "admin", "close_streams", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "上游可能已执行") || calls != 1 {
		t.Fatalf("result=%+v calls=%d audit=%+v", got, calls, audit.List())
	}
	entries := audit.List()
	if len(entries) != 1 || !strings.Contains(entries[0].Message, "开始") {
		t.Fatalf("prewrite intent not retained: %+v", entries)
	}
}
