package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"zlm-admin/core/config"
)

func newAdvancedHub(t *testing.T, response func(api string, form url.Values) string) (*Hub, *recordingAudit, *[]sourceCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]sourceCall, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		mu.Lock()
		calls = append(calls, sourceCall{api: api, method: r.Method, form: cloneValues(r.PostForm)})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response(api, r.Form)))
	}))
	t.Cleanup(srv.Close)
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL, Secret: "super-secret"})
	audit := &recordingAudit{}
	return &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}, audit, &calls
}

func cloneValues(in url.Values) url.Values {
	out := url.Values{}
	for k, vs := range in {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func TestAdvancedRestartUsesPOSTAndPairedAudit(t *testing.T) {
	h, audit, calls := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":0,"msg":"MediaServer will reboot in on 1 second"}`
	})
	got := h.AdvancedOperation("node-1", "admin", AdvancedRestart, nil)
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if len(*calls) != 1 || (*calls)[0].api != "restartServer" || (*calls)[0].method != http.MethodPost {
		t.Fatalf("calls=%+v", *calls)
	}
	if (*calls)[0].form.Get("secret") != "" {
		t.Fatal("secret leaked into form body")
	}
	entries := audit.List()
	if len(entries) != 2 || entries[0].Phase != "intent" || entries[1].Phase != "result" ||
		entries[0].OperationID == "" || entries[0].OperationID != entries[1].OperationID {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestAdvancedDeleteRecordDirectoryRequiresPeriodAndRejectsTraversal(t *testing.T) {
	h, audit, calls := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":0,"path":"/data/zlm/mp4/live/cam/2026-08-24"}`
	})
	for _, q := range []url.Values{
		{"app": {"live"}, "stream": {"cam"}, "period": {"2026-08"}},
		{"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"}, "name": {"../secret.mp4"}},
		{"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"}, "customized_path": {"/etc"}},
		{"app": {"live/../x"}, "stream": {"cam"}, "period": {"2026-08-24"}},
	} {
		got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, q)
		if asFloat(got["code"]) == 0 {
			t.Fatalf("accepted invalid q=%v result=%+v", q, got)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("upstream called on invalid input: %+v", *calls)
	}
	got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, url.Values{
		"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"}, "name": {"clip.mp4"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("valid delete failed: %+v", got)
	}
	if len(*calls) != 1 || (*calls)[0].api != "deleteRecordDirectory" ||
		(*calls)[0].method != http.MethodPost ||
		(*calls)[0].form.Get("period") != "2026-08-24" ||
		(*calls)[0].form.Get("name") != "clip.mp4" ||
		(*calls)[0].form.Has("customized_path") {
		t.Fatalf("unexpected call=%+v", *calls)
	}
	blob, _ := json.Marshal(map[string]any{"result": got, "audit": audit.List()})
	if strings.Contains(string(blob), "super-secret") || strings.Contains(string(blob), "/etc") {
		t.Fatalf("sensitive data leaked: %s", blob)
	}
}

func TestAdvancedDeleteRecordDirectoryAcceptsZLMBoolAndNumericCode(t *testing.T) {
	h, _, _ := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":true,"path":"/data/zlm/mp4/live/cam/2026-08-24"}`
	})
	got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, url.Values{
		"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("bool true should succeed: %+v", got)
	}

	h, _, _ = newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":1,"path":"/data/zlm/mp4/live/cam/2026-08-24"}`
	})
	got = h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, url.Values{
		"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("numeric 1 with path should succeed: %+v", got)
	}

	h, _, calls := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":false,"path":"/data/zlm/mp4/live/cam/2026-08-24"}`
	})
	got = h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, url.Values{
		"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"},
	})
	if asFloat(got["code"]) == 0 || len(*calls) != 1 {
		t.Fatalf("bool false should fail: %+v calls=%+v", got, *calls)
	}
}

func TestAdvancedDeleteSnapDirectoryValidatesFileName(t *testing.T) {
	h, _, calls := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":0}`
	})
	for _, file := range []string{"../a.jpg", "/tmp/a.jpg", "a.png", "a.jpg/../b.jpg"} {
		got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteSnapDir, url.Values{
			"app": {"live"}, "stream": {"cam"}, "file": {file},
		})
		if asFloat(got["code"]) == 0 {
			t.Fatalf("accepted file=%q result=%+v", file, got)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("upstream called: %+v", *calls)
	}
	got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteSnapDir, url.Values{
		"app": {"live"}, "stream": {"cam"}, "file": {"latest.jpg"},
	})
	if asFloat(got["code"]) != 0 || (*calls)[0].api != "deleteSnapDirectory" ||
		(*calls)[0].form.Get("file") != "latest.jpg" {
		t.Fatalf("valid snap delete failed result=%+v calls=%+v", got, *calls)
	}
}

func TestAdvancedBroadcastIsRemoved(t *testing.T) {
	h, _, calls := newAdvancedHub(t, func(string, url.Values) string {
		return `{"code":0}`
	})
	got := h.AdvancedOperation("node-1", "admin", AdvancedBroadcast, url.Values{
		"schema": {"rtmp"}, "app": {"live"}, "stream": {"cam"}, "template": {"maintenance"},
	})
	if asFloat(got["code"]) == 0 || len(*calls) != 0 {
		t.Fatalf("broadcast still allowed: %+v calls=%+v", got, *calls)
	}
}

func TestAdvancedOperationsFailClosedAndRejectUnknownOrDownloadAPIs(t *testing.T) {
	h := &Hub{zlm: &zlmClient{http: http.DefaultClient}}
	for _, action := range []string{AdvancedRestart, AdvancedDeleteRecordDir, AdvancedDeleteSnapDir} {
		got := h.AdvancedOperation("node-1", "admin", action, url.Values{
			"app": {"live"}, "stream": {"cam"}, "period": {"2026-08-24"},
			"schema": {"rtmp"}, "template": {"maintenance"},
		})
		if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "审计") {
			t.Fatalf("audit fail-open action=%s result=%+v", action, got)
		}
	}
	h, _, calls := newAdvancedHub(t, func(string, url.Values) string { return `{"code":0}` })
	for _, action := range []string{"downloadFile", "downloadBin", "getSnap", "restart"} {
		got := h.AdvancedOperation("node-1", "admin", action, nil)
		if asFloat(got["code"]) == 0 {
			t.Fatalf("action %s unexpectedly allowed: %+v", action, got)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("forbidden APIs reached ZLM: %+v", *calls)
	}
}

func TestAdvancedUnknownNodeAndResultAuditFailure(t *testing.T) {
	var calls []sourceCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		calls = append(calls, sourceCall{api: strings.TrimPrefix(r.URL.Path, "/index/api/")})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"MediaServer will reboot in on 1 second"}`))
	}))
	t.Cleanup(srv.Close)
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	fail := &failingAudit{failOn: 2}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: fail}
	got := h.AdvancedOperation("node-1", "admin", AdvancedRestart, nil)
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "上游可能已执行") {
		t.Fatalf("result audit failure not reported: %+v", got)
	}
	if len(calls) != 1 {
		t.Fatalf("upstream retried or skipped: %+v", calls)
	}

	h = &Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}
	got = h.AdvancedOperation("missing", "admin", AdvancedRestart, nil)
	if asFloat(got["code"]) == 0 || len(calls) != 1 {
		t.Fatalf("unknown node executed upstream: %+v calls=%+v", got, calls)
	}
}

func TestNodeActionDoesNotExposeAdvancedOrDownloadAPIs(t *testing.T) {
	h, _, calls := newAdvancedHub(t, func(string, url.Values) string { return `{"code":0}` })
	for _, action := range []string{
		"restartServer", "deleteRecordDirectory", "deleteSnapDirectory",
		"broadcastMessage", "downloadFile", "downloadBin",
	} {
		data, status, _ := h.NodeAction("node-1", action, "127.0.0.1", url.Values{}, nil)
		m, _ := data.(map[string]any)
		if status != http.StatusNotFound || asFloat(m["code"]) == 0 {
			t.Fatalf("NodeAction exposed %s data=%+v status=%d", action, data, status)
		}
	}
	if len(*calls) != 0 {
		t.Fatalf("NodeAction reached ZLM: %+v", *calls)
	}
}
