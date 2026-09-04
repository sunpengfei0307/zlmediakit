package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"zlm-admin/core/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallJSONTransportContract(t *testing.T) {
	var query url.Values
	var contentType string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, contentType = r.URL.Query(), r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"code":0,"result":0}`)
	}))
	defer srv.Close()
	got, err := (&zlmClient{http: srv.Client()}).callJSON(config.Node{
		API: srv.URL, Secret: "top-secret",
	}, "pauseStream", map[string]any{"app": "live", "stream": "cam"})
	if err != nil || asFloat(got["result"]) != 0 {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if query.Get("secret") != "top-secret" || len(query) != 1 ||
		contentType != "application/json" || body["secret"] != nil || body["app"] != "live" {
		t.Fatalf("query=%v content-type=%q body=%v", query, contentType, body)
	}

	client := &zlmClient{http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial " + r.URL.String())
	})}}
	_, err = client.callJSON(config.Node{API: "http://127.0.0.1:1", Secret: "top-secret"}, "seekStream", map[string]any{})
	if err == nil || strings.Contains(err.Error(), "top-secret") || strings.Contains(strings.ToLower(err.Error()), "secret=") {
		t.Fatalf("secret leaked: %v", err)
	}
}

func TestCallJSONRejectsCodeAndRedactsResponseSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":-1,"msg":"rejected top-secret"}`)
	}))
	defer srv.Close()
	_, err := (&zlmClient{http: srv.Client()}).callJSON(
		config.Node{API: srv.URL, Secret: "top-secret"}, "pauseStream", map[string]any{},
	)
	if err == nil || !strings.Contains(err.Error(), "code=-1") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("code error not safely reported: %v", err)
	}
}

func TestLoadMP4FileSandboxResultAndAudit(t *testing.T) {
	root := t.TempDir()
	mp4 := filepath.Join(root, "cam.mp4")
	_ = os.WriteFile(mp4, []byte("mp4"), 0o644)
	var method, api string
	var form url.Values
	reply := `{"code":0,"data":{"duration_ms":0}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, api = r.Method, r.URL.Path
		_ = r.ParseForm()
		form = r.PostForm
		_, _ = io.WriteString(w, reply)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL, MP4Save: root})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}
	got := h.RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
		"app": {"vod"}, "stream": {"cam"}, "file_path": {mp4},
		"file_repeat": {"1"}, "seek_ms": {"0"}, "speed": {"1"},
	})
	if asFloat(got["code"]) != 0 || method != http.MethodPost || api != "/index/api/loadMP4File" ||
		form.Get("vhost") != "__defaultVhost__" {
		t.Fatalf("result=%+v method=%s api=%s form=%v", got, method, api, form)
	}
	wantInfo, wantErr := os.Stat(mp4)
	gotInfo, gotErr := os.Stat(form.Get("file_path"))
	if wantErr != nil || gotErr != nil || !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("file_path=%q want=%q errors=%v/%v", form.Get("file_path"), mp4, gotErr, wantErr)
	}
	entries := audit.List()
	if len(entries) != 2 || entries[0].Phase != "intent" || entries[1].Phase != "result" ||
		entries[0].OperationID == "" || entries[0].OperationID != entries[1].OperationID ||
		!entries[1].Success || strings.Contains(entries[0].Target, filepath.Dir(root)) {
		t.Fatalf("audit=%+v", entries)
	}

	outside := filepath.Join(t.TempDir(), "outside.mp4")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	txt := filepath.Join(root, "bad.txt")
	_ = os.WriteFile(txt, []byte("x"), 0o644)
	calls := 0
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, reply)
	})
	for _, file := range []string{root, outside, txt, filepath.Join(root, "missing.mp4")} {
		got = h.RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
			"app": {"vod"}, "stream": {"cam"}, "file_path": {file},
		})
		if asFloat(got["code"]) == 0 || strings.Contains(asString(got["msg"]), filepath.Dir(outside)) {
			t.Fatalf("invalid path accepted/leaked: %+v", got)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid paths made %d calls", calls)
	}
	link := filepath.Join(root, "link.mp4")
	if err := os.Symlink(outside, link); err == nil {
		got = h.RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
			"app": {"vod"}, "stream": {"cam"}, "file_path": {link},
		})
		if asFloat(got["code"]) == 0 || calls != 0 {
			t.Fatalf("symlink escape accepted: %+v", got)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	valid := filepath.Join(root, "valid.mp4")
	_ = os.WriteFile(valid, []byte("x"), 0o644)
	reply = `{"code":0,"data":{}}`
	got = h.RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
		"app": {"vod"}, "stream": {"cam"}, "file_path": {valid},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("success without duration_ms rejected: %+v", got)
	}
}

func TestLoadMP4FileAppliesRepeatSeekAndSpeedOnZLMAPIs(t *testing.T) {
	root := t.TempDir()
	mp4 := filepath.Join(root, "cam.mp4")
	_ = os.WriteFile(mp4, []byte("mp4"), 0o644)
	var apis []string
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		apis = append(apis, r.URL.Path)
		forms = append(forms, r.PostForm)
		switch r.URL.Path {
		case "/index/api/loadMP4File":
			_, _ = io.WriteString(w, `{"code":0,"data":{"duration_ms":8000}}`)
		case "/index/api/seekRecordStamp", "/index/api/setRecordSpeed":
			_, _ = io.WriteString(w, `{"code":0,"result":0}`)
		default:
			_, _ = io.WriteString(w, `{"code":-1,"msg":"unexpected "+r.URL.Path}`)
		}
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL, MP4Save: root})
	got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}).
		RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
			"app": {"vod"}, "stream": {"clip"}, "file_path": {mp4},
			"file_repeat": {"1"}, "seek_ms": {"1500"}, "speed": {"2"},
		})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if len(apis) != 3 || apis[0] != "/index/api/loadMP4File" ||
		apis[1] != "/index/api/seekRecordStamp" || apis[2] != "/index/api/setRecordSpeed" {
		t.Fatalf("apis=%v", apis)
	}
	if forms[0].Get("file_repeat") != "1" || forms[0].Get("seek_ms") != "" || forms[0].Get("speed") != "" {
		t.Fatalf("loadMP4File must only send file_repeat, got %v", forms[0])
	}
	if forms[1].Get("stamp") != "1500" || forms[1].Get("schema") == "" ||
		forms[1].Get("app") != "vod" || forms[1].Get("stream") != "clip" {
		t.Fatalf("seek form=%v", forms[1])
	}
	if forms[2].Get("speed") != "2" || forms[2].Get("schema") == "" {
		t.Fatalf("speed form=%v", forms[2])
	}
}

func TestLoadMP4FileAcceptsListingPathRelativeToNodeRoot(t *testing.T) {
	nodeRoot := t.TempDir()
	mp4Root := filepath.Join(nodeRoot, "mp4")
	if err := os.MkdirAll(filepath.Join(mp4Root, "live"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(mp4Root, "live", "cam.mp4")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotPath = r.PostForm.Get("file_path")
		_, _ = io.WriteString(w, `{"code":0,"data":{"duration_ms":1}}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL, Root: nodeRoot, MP4Save: mp4Root})
	got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}).
		RecordVODOperation("node-1", "admin", "loadMP4File", url.Values{
			"app": {"vod"}, "stream": {"cam"}, "file_path": {"mp4/live/cam.mp4"},
		})
	wantInfo, wantErr := os.Stat(file)
	gotInfo, gotErr := os.Stat(gotPath)
	if asFloat(got["code"]) != 0 || wantErr != nil || gotErr != nil || !os.SameFile(wantInfo, gotInfo) {
		t.Fatalf("listing path not resolved: result=%+v path=%q", got, gotPath)
	}
}

func TestRecordVODAPIsValidationTransportAndNoEffect(t *testing.T) {
	tests := []struct {
		action   string
		q        url.Values
		jsonBody bool
		reply    string
		ok       bool
	}{
		{"startRecordTask", url.Values{"app": {"live"}, "stream": {"cam"}, "path": {"event/cam.mp4"}, "back_ms": {"1"}, "forward_ms": {"0"}}, false, `{"code":0,"data":{"path":"event/cam.mp4"}}`, true},
		{"startRecordTask", url.Values{"app": {"live"}, "stream": {"cam"}, "path": {"../bad.mp4"}, "back_ms": {"1"}}, false, `{"code":0,"data":{"path":"x"}}`, false},
		{"setRecordSpeed", url.Values{"schema": {"rtmp"}, "app": {"live"}, "stream": {"cam"}, "speed": {"4"}}, false, `{"code":0,"result":0}`, true},
		{"setRecordSpeed", url.Values{"schema": {"srt"}, "app": {"live"}, "stream": {"cam"}, "speed": {"1"}}, false, `{"code":0,"result":0}`, false},
		{"seekRecordStamp", url.Values{"schema": {"rtsp"}, "app": {"live"}, "stream": {"cam"}, "stamp": {"86400000"}}, false, `{"code":0,"result":0}`, true},
		{"pauseStream", url.Values{"app": {"proxy"}, "stream": {"cam"}}, true, `{"code":0,"result":0}`, true},
		{"seekStream", url.Values{"app": {"proxy"}, "stream": {"cam"}, "position": {"86400001"}}, true, `{"code":0,"result":0}`, false},
		{"setStreamSpeed", url.Values{"app": {"proxy"}, "stream": {"cam"}, "speed": {"0.249"}}, true, `{"code":0,"result":0}`, false},
		{"setStreamSpeed", url.Values{"app": {"proxy"}, "stream": {"cam"}, "speed": {"1"}}, true, `{"code":0,"result":-1}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.action+"-"+asString(tt.ok), func(t *testing.T) {
			calls := 0
			var gotJSON bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				gotJSON = strings.HasPrefix(r.Header.Get("Content-Type"), "application/json")
				if r.URL.Path != "/index/api/"+tt.action {
					t.Errorf("path=%s", r.URL.Path)
				}
				_, _ = io.WriteString(w, tt.reply)
			}))
			defer srv.Close()
			withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
			audit := &recordingAudit{}
			got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}).
				RecordVODOperation("node-1", "operator", tt.action, tt.q)
			if (asFloat(got["code"]) == 0) != tt.ok {
				t.Fatalf("result=%+v", got)
			}
			wantCalls := 0
			if tt.ok || strings.Contains(tt.reply, `"result":-1`) {
				wantCalls = 1
			}
			if calls != wantCalls || calls == 1 && gotJSON != tt.jsonBody {
				t.Fatalf("calls=%d json=%v", calls, gotJSON)
			}
			if len(audit.List()) != 2 || audit.List()[1].Success != tt.ok {
				t.Fatalf("audit=%+v", audit.List())
			}
		})
	}
}

func TestRecordVODRejectsBadNamesBoundsAndAuditPrewrite(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"code":0,"result":0}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	for _, tt := range []struct {
		action string
		q      url.Values
	}{
		{"pauseStream", url.Values{"app": {"bad\nname"}, "stream": {"cam"}}},
		{"pauseStream", url.Values{"app": {"../live"}, "stream": {"cam"}}},
		{"setRecordSpeed", url.Values{"schema": {"rtmp"}, "app": {"live"}, "stream": {"cam"}, "speed": {"4.01"}}},
		{"seekRecordStamp", url.Values{"schema": {"rtmp"}, "app": {"live"}, "stream": {"cam"}, "stamp": {"-1"}}},
		{"startRecordTask", url.Values{"app": {"live"}, "stream": {"cam"}, "path": {"x.mp4"}, "back_ms": {"0"}, "forward_ms": {"0"}}},
		{"startRecordTask", url.Values{"app": {"live"}, "stream": {"cam"}, "path": {"x.mp4"}, "back_ms": {"600001"}}},
	} {
		got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}).
			RecordVODOperation("node-1", "admin", tt.action, tt.q)
		if asFloat(got["code"]) == 0 {
			t.Fatalf("%s accepted %+v", tt.action, tt.q)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid inputs made %d calls", calls)
	}
	got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &failingAudit{failOn: 1}}).
		RecordVODOperation("node-1", "admin", "pauseStream", url.Values{"app": {"proxy"}, "stream": {"cam"}})
	if asFloat(got["code"]) == 0 || calls != 0 || !strings.Contains(asString(got["msg"]), "审计") {
		t.Fatalf("fail-open result=%+v calls=%d", got, calls)
	}
}

func TestParseEventRecordMSPrefersSeconds(t *testing.T) {
	ms, err := parseEventRecordMS(url.Values{"back_sec": {"10"}, "back_ms": {"1"}}, "back")
	if err != nil || ms != 10000 {
		t.Fatalf("sec override: %d %v", ms, err)
	}
	ms, err = parseEventRecordMS(url.Values{"back_ms": {"2500"}}, "back")
	if err != nil || ms != 2500 {
		t.Fatalf("ms fallback: %d %v", ms, err)
	}
}

func TestZLMRecordingOnReadsStatusField(t *testing.T) {
	if !zlmRecordingOn(map[string]any{"code": 0, "status": true}) {
		t.Fatal("status true should count as recording")
	}
	if zlmRecordingOn(map[string]any{"code": 0, "status": false, "data": false}) {
		t.Fatal("status false should not count as recording")
	}
	if !zlmRecordingOn(map[string]any{"data": true}) {
		t.Fatal("data true still supported")
	}
}

func TestStartRecordTaskSendsSecondsAsMilliseconds(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.PostForm
		_, _ = io.WriteString(w, `{"code":0,"data":{"path":"event/cam.mp4"}}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}).
		RecordVODOperation("node-1", "admin", "startRecordTask", url.Values{
			"app": {"live"}, "stream": {"cam"}, "path": {"event/cam.mp4"},
			"back_sec": {"10"}, "forward_sec": {"8"},
		})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if form.Get("back_ms") != "10000" || form.Get("forward_ms") != "8000" {
		t.Fatalf("form=%v", form)
	}
}

func TestStartStopRecordUsePOSTAndPairedAudit(t *testing.T) {
	type request struct {
		api    string
		method string
		form   url.Values
	}
	var requests []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		requests = append(requests, request{api: r.URL.Path, method: r.Method, form: r.PostForm})
		_, _ = io.WriteString(w, `{"code":0,"result":true}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	start := h.RecordVODOperation("node-1", "operator", "startRecord", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
		"kind": {"mp4"}, "max_second": {"600"},
	})
	stop := h.RecordVODOperation("node-1", "operator", "stopRecord", url.Values{
		"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
	})
	if asFloat(start["code"]) != 0 || asFloat(stop["code"]) != 0 {
		t.Fatalf("start=%+v stop=%+v", start, stop)
	}
	if len(requests) != 4 {
		t.Fatalf("requests=%+v", requests)
	}
	wantAPIs := []string{
		"/index/api/setServerConfig", "/index/api/startRecord",
		"/index/api/stopRecord", "/index/api/stopRecord",
	}
	for i, req := range requests {
		if req.method != http.MethodPost || req.api != wantAPIs[i] {
			t.Fatalf("request[%d]=%+v", i, req)
		}
	}
	if requests[0].form.Get("protocol.mp4_max_second") != "600" ||
		requests[1].form.Get("type") != "1" || requests[1].form.Get("app") != "live" ||
		requests[2].form.Get("type") != "0" || requests[3].form.Get("type") != "1" {
		t.Fatalf("forms=%+v", requests)
	}
	entries := audit.List()
	if len(entries) != 4 {
		t.Fatalf("audit=%+v", entries)
	}
	for i := 0; i < len(entries); i += 2 {
		if entries[i].Phase != "intent" || entries[i+1].Phase != "result" ||
			entries[i].OperationID == "" || entries[i].OperationID != entries[i+1].OperationID ||
			!entries[i+1].Success {
			t.Fatalf("audit pair=%+v", entries[i:i+2])
		}
	}
}

func TestSetRecordPrefWritesMp4MaxSecondWithoutStarting(t *testing.T) {
	var requests []string
	var last url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		requests = append(requests, r.URL.Path)
		last = r.PostForm
		_, _ = io.WriteString(w, `{"code":0,"changed":1}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}
	got := h.RecordVODOperation("node-1", "operator", "setRecordPref", url.Values{
		"max_second": {"3540"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("pref=%+v", got)
	}
	if len(requests) != 1 || requests[0] != "/index/api/setServerConfig" {
		t.Fatalf("requests=%v", requests)
	}
	if last.Get("protocol.mp4_max_second") != "3540" {
		t.Fatalf("form=%v", last)
	}
}

func TestStartStopRecordAuditPrewriteFailurePreventsZLM(t *testing.T) {
	for _, action := range []string{"startRecord", "stopRecord"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_, _ = io.WriteString(w, `{"code":0}`)
			}))
			defer srv.Close()
			withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
			got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: &failingAudit{failOn: 1}}).
				RecordVODOperation("node-1", "operator", action, url.Values{
					"app": {"live"}, "stream": {"cam"}, "kind": {"mp4"}, "max_second": {"600"},
				})
			if asFloat(got["code"]) == 0 || calls != 0 || !strings.Contains(asString(got["msg"]), "审计预写失败") {
				t.Fatalf("action=%s result=%+v calls=%d", action, got, calls)
			}
		})
	}
}

func TestStartRecordValidatesExistingParametersBeforeZLM(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	for _, q := range []url.Values{
		{"app": {"../live"}, "stream": {"cam"}, "kind": {"mp4"}, "max_second": {"600"}},
		{"app": {"live"}, "stream": {"cam"}, "kind": {"flv"}, "max_second": {"600"}},
		{"app": {"live"}, "stream": {"cam"}, "kind": {"mp4"}, "max_second": {"0"}},
		{"app": {"live"}, "stream": {"cam"}, "kind": {"mp4"}, "max_second": {"31536001"}},
	} {
		audit := &recordingAudit{}
		got := (&Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}).
			RecordVODOperation("node-1", "operator", "startRecord", q)
		if asFloat(got["code"]) == 0 || len(audit.List()) != 2 || audit.List()[1].Success {
			t.Fatalf("accepted invalid params=%v result=%+v audit=%+v", q, got, audit.List())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid parameters made %d calls", calls)
	}
}

func TestNodeActionDoesNotExposeUnauditedStartStopRecord(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}
	for _, action := range []string{"start_record", "stop_record"} {
		raw, status, _ := h.NodeAction("node-1", action, "", url.Values{
			"app": {"live"}, "stream": {"cam"}, "kind": {"mp4"},
		}, nil)
		got := raw.(map[string]any)
		if status != http.StatusNotFound || asFloat(got["code"]) == 0 {
			t.Fatalf("action=%s status=%d result=%+v", action, status, got)
		}
	}
	if calls != 0 {
		t.Fatalf("unaudited NodeAction made %d calls", calls)
	}
}

func TestAttachVODMarksUsesRegistryAndOrigin(t *testing.T) {
	h := &Hub{}
	n := config.Node{ID: "node-1", HTTPPort: 8090}
	files := []MediaFile{
		{Path: "mp4/live/cam.mp4", Name: "cam.mp4"},
		{Path: "mp4/live/other.mp4", Name: "other.mp4"},
		{Path: "mp4/live/from-origin.mp4", Name: "from-origin.mp4"},
	}
	h.rememberVODLoad("node-1", "mp4/live/cam.mp4", "__defaultVhost__", "vod", "cam")
	h.rememberVODLoad("node-1", "mp4/live/other.mp4", "__defaultVhost__", "vod", "gone")
	got := attachVODMarks(h, n, "10.0.0.8", files, []vodLiveStream{
		{Vhost: "__defaultVhost__", App: "vod", Stream: "cam", OriginType: 5, OriginURL: "/data/zlm/mp4/live/cam.mp4"},
		{Vhost: "__defaultVhost__", App: "vod", Stream: "from-origin", OriginType: 5, OriginURL: "/data/zlm/mp4/live/from-origin.mp4"},
	})
	if !got[0].VodLoaded || got[0].VodApp != "vod" || got[0].VodStream != "cam" ||
		!strings.Contains(got[0].PlayURL, "vod/cam.live.flv") ||
		got[0].PlaySID != "node-1|__defaultVhost__|vod|cam" {
		t.Fatalf("registry mark: %+v", got[0])
	}
	if got[1].VodLoaded {
		t.Fatalf("stale load still marked: %+v", got[1])
	}
	if !got[2].VodLoaded || got[2].VodStream != "from-origin" || !strings.Contains(got[2].PlayURL, "from-origin.live.flv") {
		t.Fatalf("origin mark: %+v", got[2])
	}
}

func TestVODLoadRegistrySurvivesClientRestart(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h1 := &Hub{kv: kv}
	h1.rememberVODLoad("node-1", "mp4/record/live/cam/2026-09-04/a.mp4", "__defaultVhost__", "vod", "a")
	h2 := &Hub{kv: kv}
	h2.restoreVODLoads()
	load, ok := h2.lookupVODLoad("node-1", "mp4/record/live/cam/2026-09-04/a.mp4")
	if !ok || load.App != "vod" || load.Stream != "a" {
		t.Fatalf("exact path after restart: ok=%v load=%+v", ok, load)
	}
	if _, ok := h2.lookupVODLoad("node-1", "record/live/cam/2026-09-04/a.mp4"); !ok {
		t.Fatal("relative path variant must still find the persisted VOD load")
	}
	h2.forgetVODLoad("node-1", "mp4/record/live/cam/2026-09-04/a.mp4")
	h3 := &Hub{kv: kv}
	h3.restoreVODLoads()
	if _, ok := h3.lookupVODLoad("node-1", "mp4/record/live/cam/2026-09-04/a.mp4"); ok {
		t.Fatal("forget must drop persisted VOD load")
	}
}

func TestAttachVODMarksReloadsMissingStreamAfterRestart(t *testing.T) {
	root := t.TempDir()
	rel := "record/live/cam/a.mp4"
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	var apis []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apis = append(apis, r.URL.Path)
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer srv.Close()
	n := config.Node{ID: "node-1", API: srv.URL, HTTPPort: 8090, MP4Save: root, Root: root}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}
	h.rememberVODLoad(n.ID, "mp4/"+rel, "__defaultVhost__", "vod", "clip")
	got := attachVODMarks(h, n, "10.0.0.8", []MediaFile{{Path: "mp4/" + rel, Name: "a.mp4"}}, []vodLiveStream{
		{Vhost: "__defaultVhost__", App: "live", Stream: "cam", OriginType: 0, OriginURL: "rtmp://src"},
	})
	if !got[0].VodLoaded || got[0].VodStream != "clip" {
		t.Fatalf("missing stream was not restored: %+v", got[0])
	}
	if len(apis) == 0 || !strings.Contains(strings.Join(apis, ","), "/index/api/loadMP4File") {
		t.Fatalf("expected reload loadMP4File, apis=%v", apis)
	}
}

func TestAttachVODMarksKeepsRegistryWhenMediaListEmpty(t *testing.T) {
	h := &Hub{}
	n := config.Node{ID: "node-1", HTTPPort: 8090}
	files := []MediaFile{{Path: "mp4/live/cam.mp4", Name: "cam.mp4"}}
	h.rememberVODLoad("node-1", "mp4/live/cam.mp4", "__defaultVhost__", "vod", "cam")
	got := attachVODMarks(h, n, "10.0.0.8", files, nil)
	if !got[0].VodLoaded || got[0].VodApp != "vod" || got[0].VodStream != "cam" {
		t.Fatalf("empty media list dropped VOD badge: %+v", got[0])
	}
}

func TestDeleteRecordFileRemovesSandboxedMP4(t *testing.T) {
	root := t.TempDir()
	mp4 := filepath.Join(root, "keep-me.mp4")
	if err := os.WriteFile(mp4, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	withTestNode(t, config.Node{ID: "node-1", Root: root, MP4Save: root})
	h := &Hub{audit: &recordingAudit{}}
	got := h.RecordVODOperation("node-1", "admin", "deleteRecordFile", url.Values{"file_path": {"keep-me.mp4"}})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("delete rejected: %+v", got)
	}
	if _, err := os.Stat(mp4); !os.IsNotExist(err) {
		t.Fatalf("file still exists: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "escape.mp4")
	_ = os.WriteFile(outside, []byte("x"), 0o644)
	got = h.RecordVODOperation("node-1", "admin", "deleteRecordFile", url.Values{"file_path": {outside}})
	if asFloat(got["code"]) == 0 {
		t.Fatalf("outside path deleted: %+v", got)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file damaged: %v", err)
	}
}
