package service

import (
	"context"
	"fmt"
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

func jsonZLM(r *http.Request, body string) *http.Response {
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     h,
		Request:    r,
	}
}

type sourceCall struct {
	api    string
	method string
	form   url.Values
}

func newSourceTaskHub(t *testing.T, response func(string) string) (*Hub, *recordingAudit, *[]sourceCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]sourceCall, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		mu.Lock()
		calls = append(calls, sourceCall{api: api, method: r.Method, form: r.Form})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response(api)))
	}))
	t.Cleanup(srv.Close)
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &recordingAudit{}
	return &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}, audit, &calls
}

func TestSourceTasksCallsAllNineReadAPIs(t *testing.T) {
	h, _, calls := newSourceTaskHub(t, func(api string) string {
		switch api {
		case "listStreamProxy":
			return `{"code":0,"data":[{"key":"pull-1"}]}`
		case "getProxyInfo":
			return `{"code":0,"data":{"key":"pull-1","url":"rtsp://camera/live"}}`
		case "listStreamPusherProxy":
			return `{"code":0,"data":[{"key":"push-1"}]}`
		case "getProxyPusherInfo":
			return `{"code":0,"data":{"key":"push-1","dst_url":"rtmp://edge/live/cam"}}`
		case "listFFmpegSource":
			return `{"code":0,"data":[{"key":"ff-1","src_url":"file:///data/in.mp4"}]}`
		default:
			return `{"code":0}`
		}
	})

	got := h.ListSourceTasks("node-1")
	if got.Pull.Error != "" || got.Pusher.Error != "" || got.FFmpeg.Error != "" {
		t.Fatalf("unexpected section errors: %+v", got)
	}
	want := []string{
		"listStreamProxy", "getProxyInfo",
		"listStreamPusherProxy", "getProxyPusherInfo",
		"listFFmpegSource",
	}
	seen := map[string]bool{}
	for _, call := range *calls {
		seen[call.api] = true
		if call.method != http.MethodGet {
			t.Fatalf("%s method=%s", call.api, call.method)
		}
	}
	for _, api := range want {
		if !seen[api] {
			t.Fatalf("missing API call %s: %+v", api, *calls)
		}
	}
}

func TestSourceTasksNormalizeRealProxyFixtures(t *testing.T) {
	h, _, _ := newSourceTaskHub(t, func(api string) string {
		switch api {
		case "listStreamProxy":
			return `{"code":0,"data":[{"key":"__defaultVhost__/proxy/0"}]}`
		case "getProxyInfo":
			return `{"code":0,"data":{"key":"__defaultVhost__/proxy/0","src":{"schema":"rtsp","vhost":"__defaultVhost__","app":"live","stream":"camera"},"url":"rtsp://camera/live","status":1}}`
		case "listStreamPusherProxy":
			return `{"code":0,"data":[{"key":"rtmp/__defaultVhost__/live/camera/0"}]}`
		case "getProxyPusherInfo":
			return `{"code":0,"data":{"key":"rtmp/__defaultVhost__/live/camera/0","src":{"vhost":"__defaultVhost__","app":"live","stream":"camera"},"url":"rtmp://edge/live/camera"}}`
		case "listFFmpegSource":
			return `{"code":0,"data":[{"key":"d41d8cd98f00b204e9800998ecf8427e","src_url":"file:///data/input.mp4","dst_url":"rtmp://127.0.0.1/live/camera","cmd":"-re -i %s -c copy %s","ffmpeg_cmd_key":"ffmpeg.cmd"}]}`
		default:
			return `{"code":0}`
		}
	})

	got := h.ListSourceTasks("node-1")
	pull := got.Pull.Items[0]
	if pull["schema"] != "rtsp" || pull["vhost"] != "__defaultVhost__" ||
		pull["app"] != "live" || pull["stream"] != "camera" || asFloat(pull["status"]) != 1 {
		t.Fatalf("pull=%+v", pull)
	}
	pusher := got.Pusher.Items[0]
	if pusher["schema"] != "rtmp" || pusher["vhost"] != "__defaultVhost__" ||
		pusher["app"] != "live" || pusher["stream"] != "camera" ||
		pusher["dst_url"] != "rtmp://edge/live/camera" {
		t.Fatalf("pusher=%+v", pusher)
	}
	ffmpeg := got.FFmpeg.Items[0]
	if ffmpeg["key"] != "d41d8cd98f00b204e9800998ecf8427e" ||
		ffmpeg["cmd"] == "" || ffmpeg["status"] != nil {
		t.Fatalf("ffmpeg=%+v", ffmpeg)
	}
}

func TestSourceTasksKeepNormalizedListFieldsWhenDetailFails(t *testing.T) {
	h, _, _ := newSourceTaskHub(t, func(api string) string {
		switch api {
		case "listStreamProxy":
			return `{"code":0,"data":[{"key":"__defaultVhost__/proxy/0","src":{"schema":"rtsp","vhost":"__defaultVhost__","app":"live","stream":"camera"},"url":"rtsp://camera/live"}]}`
		case "getProxyInfo":
			return `{"code":-1,"msg":"detail unavailable"}`
		case "listStreamPusherProxy":
			return `{"code":0,"data":[{"key":"__defaultVhost__/pusher/0","src":{"schema":"rtmp","vhost":"__defaultVhost__","app":"live","stream":"camera"},"url":"rtmp://edge/live/camera"}]}`
		case "getProxyPusherInfo":
			return `{"code":-1,"msg":"detail unavailable"}`
		default:
			return `{"code":0,"data":[]}`
		}
	})
	got := h.ListSourceTasks("node-1")
	if got.Pull.Items[0]["app"] != "live" || got.Pull.Items[0]["schema"] != "rtsp" {
		t.Fatalf("pull=%+v", got.Pull.Items[0])
	}
	if got.Pusher.Items[0]["stream"] != "camera" || got.Pusher.Items[0]["dst_url"] != "rtmp://edge/live/camera" {
		t.Fatalf("pusher=%+v", got.Pusher.Items[0])
	}
}

func TestSourceTasksListFailuresStayLocalToSection(t *testing.T) {
	h, _, _ := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":-1,"msg":"pull unavailable"}`
		}
		return `{"code":0,"data":[]}`
	})
	got := h.ListSourceTasks("node-1")
	if !strings.Contains(got.Pull.Error, "pull unavailable") {
		t.Fatalf("pull error=%q", got.Pull.Error)
	}
	if got.Pusher.Error != "" || got.FFmpeg.Error != "" {
		t.Fatalf("unrelated sections failed: %+v", got)
	}
}

func TestSourceTaskSectionAndAuditHideTransportSecret(t *testing.T) {
	const secret = "source-secret-value"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network down ?secret=%s %s", secret, r.URL.String())
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid:8090", Secret: secret})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: audit}

	view := h.ListSourceTasks("node-1")
	if view.Pull.Error == "" || strings.Contains(view.Pull.Error, secret) ||
		strings.Contains(strings.ToLower(view.Pull.Error), "secret=") {
		t.Fatalf("section error leaked secret: %q", view.Pull.Error)
	}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
	}, true)
	if asFloat(got["code"]) == 0 {
		t.Fatalf("result=%+v", got)
	}
	for _, entry := range audit.List() {
		if strings.Contains(entry.Message, secret) || strings.Contains(strings.ToLower(entry.Message), "secret=") {
			t.Fatalf("audit leaked secret: %+v", entry)
		}
	}
}

func TestValidatePullSourceAcceptsHTTPLiveURLs(t *testing.T) {
	for _, src := range []string{
		"http://edge/live/cam.live.ts",
		"https://edge/live/cam.live.flv",
		"http://edge/live/cam/hls.m3u8",
		"http://edge/live/cam/hls.fmp4.m3u8",
	} {
		got, err := validatePullSource(url.Values{
			"app": {"live"}, "stream": {"cam"}, "url": {src},
		})
		if err != nil || got.Get("url") != src {
			t.Fatalf("url %s err=%v form=%v", src, err, got)
		}
	}
}

func TestValidatePullSourceAcceptsHTTPMP4(t *testing.T) {
	src := "http://10.191.6.5:8091/live/ls_cctv.mp4?token=secret"
	got, err := validatePullSource(url.Values{
		"app": {"live"}, "stream": {"camera_01"}, "url": {src},
	})
	if err != nil || got.Get("url") != src {
		t.Fatalf("url %s err=%v form=%v", src, err, got)
	}
	if !pullNeedsFFmpeg(src) {
		t.Fatal("http mp4 should use ffmpeg")
	}
	if pullNeedsFFmpeg("http://edge/live/cam.live.flv") {
		t.Fatal("http-flv should stay on addStreamProxy")
	}
}

func TestPullHTTPMP4UsesFFmpegSource(t *testing.T) {
	h, _, calls := newSourceTaskHub(t, func(api string) string {
		switch api {
		case "listFFmpegSource", "listStreamProxy":
			return `{"code":0,"data":[]}`
		default:
			return `{"code":0,"data":{"key":"ff-key"}}`
		}
	})
	src := "http://10.191.6.5:8091/live/ls_cctv.mp4?token=secret"
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"camera_01"}, "url": {src}, "timeout_sec": {"10"},
	}, true)
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(asString(got["msg"]), "FFmpeg") {
		t.Fatalf("msg=%v", got["msg"])
	}
	var ffmpeg sourceCall
	for _, call := range *calls {
		if call.api == "addStreamProxy" {
			t.Fatalf("http mp4 should not call addStreamProxy: %+v", call)
		}
		if call.api == "addFFmpegSource" {
			ffmpeg = call
		}
	}
	if ffmpeg.api == "" || ffmpeg.form.Get("src_url") != src ||
		ffmpeg.form.Get("dst_url") != "rtmp://127.0.0.1:1935/live/camera_01" ||
		ffmpeg.form.Get("timeout_ms") != "10000" {
		t.Fatalf("ffmpeg=%+v calls=%+v", ffmpeg, *calls)
	}
}

func TestAddPullValidatesAndForwardsBoundaryParameters(t *testing.T) {
	h, audit, calls := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":0,"data":{"key":"pull-key"}}`
	})
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://user:pass@camera/live?token=secret"},
		"retry_count": {"-1"}, "timeout_sec": {"120"}, "force": {"1"}, "rtp_type": {"2"},
	}, true)
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	call := (*calls)[1]
	if call.api != "addStreamProxy" || call.method != http.MethodPost {
		t.Fatalf("call=%+v", call)
	}
	if call.form.Get("vhost") != "__defaultVhost__" || call.form.Get("retry_count") != "-1" ||
		call.form.Get("timeout_sec") != "120" || call.form.Get("force") != "1" || call.form.Get("rtp_type") != "2" {
		t.Fatalf("form=%v", call.form)
	}
	entries := audit.List()
	if len(entries) != 2 || !strings.Contains(entries[0].Message, "开始") || !entries[1].Success ||
		strings.Contains(entries[1].Target, "user") || strings.Contains(entries[1].Target, "pass") ||
		strings.Contains(entries[1].Target, "token") || strings.Contains(entries[1].Target, "?") {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestSourceAuditMessageAlsoRedactsSubmittedURLs(t *testing.T) {
	h, audit, _ := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":-1,"msg":"failed rtsp://user:pass@camera/live?token=secret"}`
	})
	_ = h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://user:pass@camera/live?token=secret"},
	}, true)
	entries := audit.List()
	if len(entries) != 2 {
		t.Fatalf("audit=%+v", entries)
	}
	entry := entries[1]
	if strings.Contains(entry.Message, "user") || strings.Contains(entry.Message, "pass") ||
		strings.Contains(entry.Message, "token") || strings.Contains(entry.Message, "?") {
		t.Fatalf("audit leaked submitted URL: %+v", entry)
	}
}

func TestSourceAuditMessageRedactsStandaloneUsernameWithoutDamagingShortOrAbsentUserinfo(t *testing.T) {
	withUser := redactSourceAuditMessage(
		"upstream rejected camera-user but host remains healthy",
		url.Values{"url": {"rtsp://camera-user:camera-pass@camera/live"}},
	)
	if strings.Contains(withUser, "camera-user") || !strings.Contains(withUser, "[REDACTED]") ||
		!strings.Contains(withUser, "host remains healthy") {
		t.Fatalf("standalone username not safely redacted: %q", withUser)
	}

	shortUserMessage := "a camera remains active"
	if got := redactSourceAuditMessage(shortUserMessage, url.Values{
		"url": {"rtsp://a:camera-pass@camera/live"},
	}); got != shortUserMessage {
		t.Fatalf("short username damaged normal message: got=%q want=%q", got, shortUserMessage)
	}

	noUserinfoMessage := "camera upstream remains active"
	if got := redactSourceAuditMessage(noUserinfoMessage, url.Values{
		"url": {"rtsp://camera/live"},
	}); got != noUserinfoMessage {
		t.Fatalf("URL without userinfo changed message: got=%q want=%q", got, noUserinfoMessage)
	}
}

func TestAddPullRejectsSchemesNumbersAndUnsafeNames(t *testing.T) {
	tests := []url.Values{
		{"app": {"live"}, "stream": {"cam"}, "url": {"ftp://host/file"}},
		{"app": {"live"}, "stream": {"cam"}, "url": {"relative/path"}},
		{"app": {"../live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}},
		{"app": {"live"}, "stream": {"cam\nbad"}, "url": {"rtsp://host/live"}},
		{"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}, "retry_count": {"101"}},
		{"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}, "retry_count": {"-2"}},
		{"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}, "timeout_sec": {"0"}},
		{"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}, "timeout_sec": {"121"}},
	}
	for i, q := range tests {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			h, audit, calls := newSourceTaskHub(t, func(string) string { return `{"code":0,"data":{"key":"x"}}` })
			got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, q, true)
			if asFloat(got["code"]) == 0 || len(*calls) != 0 {
				t.Fatalf("result=%+v calls=%+v", got, *calls)
			}
			if len(audit.List()) != 1 || audit.List()[0].Success {
				t.Fatalf("audit=%+v", audit.List())
			}
		})
	}
}

func TestPusherRejectsHTTPAndForwardsAllowedParameters(t *testing.T) {
	h, _, calls := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamPusherProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":0,"data":{"key":"push-key"}}`
	})
	missingVhost := h.SourceTaskOperation("node-1", "admin", SourceTaskPusherAdd, url.Values{
		"schema": {"rtmp"}, "app": {"live"}, "stream": {"cam"}, "dst_url": {"rtmp://edge/live/cam"},
	}, true)
	if asFloat(missingVhost["code"]) == 0 || len(*calls) != 0 {
		t.Fatalf("missing vhost result=%+v calls=%+v", missingVhost, *calls)
	}
	bad := h.SourceTaskOperation("node-1", "admin", SourceTaskPusherAdd, url.Values{
		"schema": {"rtmp"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"},
		"dst_url": {"https://edge/upload"},
	}, true)
	if asFloat(bad["code"]) == 0 || len(*calls) != 0 {
		t.Fatalf("bad result=%+v calls=%+v", bad, *calls)
	}
	ok := h.SourceTaskOperation("node-1", "admin", SourceTaskPusherAdd, url.Values{
		"schema": {"srt"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "dst_url": {"srt://edge:9000"},
		"retry_count": {"100"}, "rtp_type": {"1"}, "timeout_sec": {"1"},
	}, true)
	if asFloat(ok["code"]) != 0 {
		t.Fatalf("ok=%+v", ok)
	}
	call := (*calls)[1]
	if call.api != "addStreamPusherProxy" || call.form.Get("schema") != "srt" ||
		call.form.Get("retry_count") != "100" || call.form.Get("timeout_sec") != "1" {
		t.Fatalf("call=%+v", call)
	}
}

func TestFFmpegValidationAndLocalFileRestriction(t *testing.T) {
	h, _, calls := newSourceTaskHub(t, func(api string) string {
		if api == "listFFmpegSource" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":0,"data":{"key":"ff-key"}}`
	})
	base := url.Values{
		"src_url": {"file:///data/input.mp4"}, "dst_url": {"rtmp://127.0.0.1/live/cam"},
		"timeout_ms": {"1000"}, "enable_hls": {"1"}, "enable_mp4": {"0"},
		"ffmpeg_cmd_key": {"ffmpeg.cmd_copy"},
	}
	if got := h.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, base, false); asFloat(got["code"]) == 0 {
		t.Fatalf("remote file source accepted: %+v", got)
	}
	if len(*calls) != 0 {
		t.Fatalf("remote file source called upstream: %+v", *calls)
	}
	if got := h.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, base, true); asFloat(got["code"]) != 0 {
		t.Fatalf("local file source rejected: %+v", got)
	}
	call := (*calls)[1]
	if call.api != "addFFmpegSource" || call.form.Get("timeout_ms") != "1000" ||
		call.form.Get("enable_hls") != "1" || call.form.Get("enable_mp4") != "0" ||
		call.form.Get("ffmpeg_cmd_key") != "" {
		t.Fatalf("call=%+v", call)
	}
	for _, q := range []url.Values{
		{"src_url": {"file://relative.mp4"}, "dst_url": {"rtmp://host/live/cam"}, "timeout_ms": {"1000"}},
		{"src_url": {"rtsp://host/live"}, "dst_url": {"https://host/upload"}, "timeout_ms": {"1000"}},
		{"src_url": {"rtsp://host/live"}, "dst_url": {"rtmp://host/live/cam"}, "timeout_ms": {"999"}},
		{"src_url": {"rtsp://host/live"}, "dst_url": {"rtmp://host/live/cam"}, "timeout_ms": {"120001"}},
		{"src_url": {"rtsp://host/live"}, "dst_url": {"rtmp://host/live/cam"}, "timeout_ms": {"1000"}, "enable_hls": {"true"}},
	} {
		h2, _, calls2 := newSourceTaskHub(t, func(string) string { return `{"code":0,"data":{"key":"x"}}` })
		if got := h2.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, q, true); asFloat(got["code"]) == 0 || len(*calls2) != 0 {
			t.Fatalf("invalid FFmpeg accepted q=%v result=%+v calls=%+v", q, got, *calls2)
		}
	}
}

func TestSourceWritesFailClosedAndNoEffectResponsesFail(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"data":{"key":"x"}}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"},
	}, true)
	if asFloat(got["code"]) == 0 || calls != 0 || !strings.Contains(asString(got["msg"]), "审计") {
		t.Fatalf("result=%+v calls=%d", got, calls)
	}

	for name, response := range map[string]string{
		"add missing key": `{"code":0,"data":{}}`,
		"delete false":    `{"code":0,"data":{"flag":false}}`,
	} {
		t.Run(name, func(t *testing.T) {
			h2, audit, _ := newSourceTaskHub(t, func(api string) string {
				if api == "listStreamProxy" {
					return `{"code":0,"data":[]}`
				}
				return response
			})
			action := SourceTaskPullAdd
			q := url.Values{"app": {"live"}, "stream": {"cam"}, "url": {"rtsp://host/live"}}
			if strings.Contains(name, "delete") {
				action = SourceTaskPullDelete
				q = url.Values{"key": {"__defaultVhost__/proxy/0"}}
			}
			result := h2.SourceTaskOperation("node-1", "admin", action, q, true)
			if asFloat(result["code"]) == 0 || len(audit.List()) != 2 || audit.List()[1].Success {
				t.Fatalf("result=%+v audit=%+v", result, audit.List())
			}
		})
	}
}

func TestDeleteSourceTypesUseExactAPIsAndKey(t *testing.T) {
	for action, api := range map[string]string{
		SourceTaskPullDelete:   "delStreamProxy",
		SourceTaskPusherDelete: "delStreamPusherProxy",
		SourceTaskFFmpegDelete: "delFFmpegSource",
	} {
		t.Run(api, func(t *testing.T) {
			h, audit, calls := newSourceTaskHub(t, func(string) string { return `{"code":0,"data":{"flag":true}}` })
			key := "__defaultVhost__/proxy/0"
			got := h.SourceTaskOperation("node-1", "admin", action, url.Values{"key": {key}}, true)
			if asFloat(got["code"]) != 0 {
				t.Fatalf("result=%+v", got)
			}
			if len(*calls) != 1 || (*calls)[0].api != api || (*calls)[0].form.Get("key") != key {
				t.Fatalf("calls=%+v", *calls)
			}
			if len(audit.List()) != 2 || !audit.List()[1].Success {
				t.Fatalf("audit=%+v", audit.List())
			}
		})
	}
}

func TestDeleteSourceKeyAllowsRealSlashKeyAndRejectsTraversal(t *testing.T) {
	for _, key := range []string{"", "../proxy/0", "__defaultVhost__/../proxy/0", `__defaultVhost__\proxy\0`, "proxy/\n/0", strings.Repeat("x", 1025)} {
		h, audit, calls := newSourceTaskHub(t, func(string) string { return `{"code":0,"data":{"flag":true}}` })
		got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullDelete, url.Values{"key": {key}}, true)
		if asFloat(got["code"]) == 0 || len(*calls) != 0 {
			t.Fatalf("key=%q result=%+v calls=%+v", key, got, *calls)
		}
		if len(audit.List()) != 1 || audit.List()[0].Success {
			t.Fatalf("key=%q audit=%+v", key, audit.List())
		}
	}
}

func TestSourceAddRejectsDuplicateReturnedKey(t *testing.T) {
	h, audit, calls := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[{"key":"__defaultVhost__/proxy/0"}]}`
		}
		return `{"code":0,"data":{"key":"__defaultVhost__/proxy/0"}}`
	})
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
	}, true)
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "已存在") {
		t.Fatalf("result=%+v", got)
	}
	if len(*calls) != 2 || (*calls)[0].api != "listStreamProxy" || (*calls)[1].api != "addStreamProxy" {
		t.Fatalf("calls=%+v", *calls)
	}
	if len(audit.List()) != 2 || audit.List()[1].Success {
		t.Fatalf("audit=%+v", audit.List())
	}
}

func TestSourceAddListFailurePreventsUpstreamAdd(t *testing.T) {
	h, audit, calls := newSourceTaskHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":-1,"msg":"list unavailable"}`
		}
		return `{"code":0,"data":{"key":"new-key"}}`
	})
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
	}, true)
	if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), "列表") {
		t.Fatalf("result=%+v", got)
	}
	if len(*calls) != 1 || (*calls)[0].api != "listStreamProxy" {
		t.Fatalf("calls=%+v", *calls)
	}
	if len(audit.List()) != 2 || !strings.Contains(audit.List()[0].Message, "开始") || audit.List()[1].Success {
		t.Fatalf("audit=%+v", audit.List())
	}
}

func TestSourceMutationAuditFailuresAreFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failOn     int
		wantCalls  int
		wantWrites int
		wantMsg    string
	}{
		{name: "prewrite", failOn: 1, wantCalls: 0, wantWrites: 0, wantMsg: "审计"},
		{name: "final", failOn: 2, wantCalls: 2, wantWrites: 1, wantMsg: "上游可能已执行"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			writes := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				api := strings.TrimPrefix(r.URL.Path, "/index/api/")
				if api == "listStreamProxy" {
					_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
					return
				}
				writes++
				_, _ = w.Write([]byte(`{"code":0,"data":{"key":"new-key"}}`))
			}))
			defer srv.Close()
			withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
			audit := &failingAudit{failOn: tc.failOn}
			h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

			got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
				"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
			}, true)
			if asFloat(got["code"]) == 0 || !strings.Contains(asString(got["msg"]), tc.wantMsg) ||
				calls != tc.wantCalls || writes != tc.wantWrites {
				t.Fatalf("result=%+v calls=%d writes=%d audit=%+v", got, calls, writes, audit.List())
			}
			if tc.failOn == 2 && (len(audit.List()) != 1 || !strings.Contains(audit.List()[0].Message, "开始")) {
				t.Fatalf("intent not retained: %+v", audit.List())
			}
		})
	}
}

func TestConcurrentIdenticalPullAddsAreSerialized(t *testing.T) {
	assertConcurrentSameSourceAddSerialized(t, SourceTaskPullAdd, "listStreamProxy", "addStreamProxy", url.Values{
		"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
	})
}

func TestConcurrentIdenticalPusherAddsAreSerialized(t *testing.T) {
	assertConcurrentSameSourceAddSerialized(t, SourceTaskPusherAdd, "listStreamPusherProxy", "addStreamPusherProxy", url.Values{
		"schema": {"rtmp"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"camera"},
		"dst_url": {"rtmp://edge/live/camera"},
	})
}

func assertConcurrentSameSourceAddSerialized(t *testing.T, action, listAPI, addAPI string, q url.Values) {
	t.Helper()
	var mu sync.Mutex
	exists := false
	addCalls := 0
	const key = "__defaultVhost__/proxy/0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		switch api {
		case listAPI:
			mu.Lock()
			present := exists
			mu.Unlock()
			if present {
				_, _ = w.Write([]byte(`{"code":0,"data":[{"key":"` + key + `"}]}`))
			} else {
				_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
			}
		case addAPI:
			mu.Lock()
			addCalls++
			mu.Unlock()
			time.Sleep(80 * time.Millisecond)
			mu.Lock()
			exists = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{"key":"` + key + `"}}`))
		default:
			t.Errorf("unexpected API %s", api)
			_, _ = w.Write([]byte(`{"code":-1}`))
		}
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	start := make(chan struct{})
	results := make(chan map[string]any, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			results <- h.SourceTaskOperation("node-1", "admin", action, q, true)
		}()
	}
	close(start)
	first, second := <-results, <-results
	successes := 0
	duplicates := 0
	for _, result := range []map[string]any{first, second} {
		if asFloat(result["code"]) == 0 {
			successes++
		}
		if strings.Contains(asString(result["msg"]), "已存在") {
			duplicates++
		}
	}
	if successes != 1 || duplicates != 1 || addCalls != 2 {
		t.Fatalf("first=%+v second=%+v addCalls=%d", first, second, addCalls)
	}
	byID := map[string]map[string]bool{}
	for _, entry := range audit.List() {
		if entry.OperationID == "" || entry.Phase == "" {
			t.Fatalf("audit entry missing pairing fields: %+v", entry)
		}
		if byID[entry.OperationID] == nil {
			byID[entry.OperationID] = map[string]bool{}
		}
		byID[entry.OperationID][entry.Phase] = true
	}
	if len(byID) != 2 {
		t.Fatalf("audit operation groups=%+v", byID)
	}
	for id, phases := range byID {
		if !phases["intent"] || !phases["result"] {
			t.Fatalf("operation %s phases=%v", id, phases)
		}
	}
}

func TestConcurrentAddDeleteShareSourceMutationCriticalSection(t *testing.T) {
	var mu sync.Mutex
	exists := true
	recreated := false
	listed := make(chan struct{})
	deleted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		switch api {
		case "listStreamProxy":
			close(listed)
			_, _ = w.Write([]byte(`{"code":0,"data":[{"key":"__defaultVhost__/proxy/0"}]}`))
		case "delStreamProxy":
			mu.Lock()
			exists = false
			mu.Unlock()
			close(deleted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"flag":true}}`))
		case "addStreamProxy":
			select {
			case <-deleted:
			case <-time.After(120 * time.Millisecond):
			}
			mu.Lock()
			if !exists {
				recreated = true
			}
			exists = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{"key":"__defaultVhost__/proxy/0"}}`))
		default:
			t.Errorf("unexpected API %s", api)
			_, _ = w.Write([]byte(`{"code":-1}`))
		}
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}

	addResult := make(chan map[string]any, 1)
	go func() {
		addResult <- h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
			"app": {"live"}, "stream": {"camera"}, "url": {"rtsp://camera/live"},
		}, true)
	}()
	<-listed
	deleteResult := make(chan map[string]any, 1)
	go func() {
		deleteResult <- h.SourceTaskOperation("node-1", "admin", SourceTaskPullDelete, url.Values{
			"key": {"__defaultVhost__/proxy/0"},
		}, true)
	}()

	addGot, deleteGot := <-addResult, <-deleteResult
	mu.Lock()
	finalExists, wasRecreated := exists, recreated
	mu.Unlock()
	if asFloat(addGot["code"]) == 0 || !strings.Contains(asString(addGot["msg"]), "已存在") {
		t.Fatalf("add result=%+v", addGot)
	}
	if asFloat(deleteGot["code"]) != 0 {
		t.Fatalf("delete result=%+v", deleteGot)
	}
	if wasRecreated || finalExists {
		t.Fatalf("delete interleaved with add: recreated=%v exists=%v", wasRecreated, finalExists)
	}
}

func TestIsZLMTimeoutErr(t *testing.T) {
	if !isZLMTimeoutErr(context.DeadlineExceeded) {
		t.Fatal("deadline should be timeout")
	}
	if !isZLMTimeoutErr(fmt.Errorf("Post x: context deadline exceeded (Client.Timeout exceeded while awaiting headers)")) {
		t.Fatal("client timeout string should match")
	}
	if isZLMTimeoutErr(fmt.Errorf("connection refused")) {
		t.Fatal("refused is not timeout")
	}
}

func TestSourceTaskWriteTimeoutCoversFFmpegAndPull(t *testing.T) {
	ff := sourceTaskWriteTimeout(SourceTaskFFmpegAdd, url.Values{"timeout_ms": {"10000"}})
	if ff != 18*time.Second {
		t.Fatalf("ffmpeg timeout=%s", ff)
	}
	if got := sourceTaskWriteTimeout(SourceTaskFFmpegAdd, url.Values{"timeout_ms": {"120000"}}); got != 90*time.Second {
		t.Fatalf("ffmpeg cap=%s", got)
	}
	pull := sourceTaskWriteTimeout(SourceTaskPullAdd, url.Values{"timeout_sec": {"10"}})
	if pull != 18*time.Second {
		t.Fatalf("pull timeout=%s", pull)
	}
	if got := sourceTaskWriteTimeout(SourceTaskPullDelete, nil); got != 15*time.Second {
		t.Fatalf("delete timeout=%s", got)
	}
}

func TestFFmpegAddTimeoutIsSuccessIfListShowsNewTask(t *testing.T) {
	var mu sync.Mutex
	listed := 0
	src := "http://10.191.6.5:8091/live/ls_cctv.mp4?token=secret"
	dst := "rtmp://10.191.6.5:1935/live/ls_cctv_ffmpeg"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		switch api {
		case "addFFmpegSource":
			return nil, context.DeadlineExceeded
		case "listFFmpegSource":
			mu.Lock()
			listed++
			n := listed
			mu.Unlock()
			body := `{"code":0,"data":[]}`
			if n >= 2 {
				body = `{"code":0,"data":[{"key":"ff-new","src_url":"` + src + `","dst_url":"` + dst + `"}]}`
			}
			return jsonZLM(r, body), nil
		default:
			return jsonZLM(r, `{"code":0}`), nil
		}
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid:8090"})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: audit}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, url.Values{
		"src_url": {src}, "dst_url": {dst},
		"timeout_ms": {"1000"}, "enable_hls": {"0"}, "enable_mp4": {"0"},
	}, true)
	if asFloat(got["code"]) != 0 || asString(got["key"]) != "ff-new" {
		t.Fatalf("result=%+v", got)
	}
	if !strings.Contains(asString(got["msg"]), "列表确认") {
		t.Fatalf("msg=%v", got["msg"])
	}
	if entries := audit.List(); len(entries) != 2 || !entries[1].Success {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestFFmpegAddTimeoutStaysFailureIfListUnchanged(t *testing.T) {
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "addFFmpegSource") {
			return nil, context.DeadlineExceeded
		}
		return jsonZLM(r, `{"code":0,"data":[]}`), nil
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid:8090"})
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: &recordingAudit{}}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, url.Values{
		"src_url": {"http://cam/a.mp4"}, "dst_url": {"rtmp://127.0.0.1:1935/live/cam"},
		"timeout_ms": {"1000"}, "enable_hls": {"0"}, "enable_mp4": {"0"},
	}, true)
	if asFloat(got["code"]) == 0 {
		t.Fatalf("empty list after timeout should fail: %+v", got)
	}
}

func TestFFmpegAddTimeoutDoesNotGuessWhenMultipleNewKeys(t *testing.T) {
	var listed int
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		if api == "addFFmpegSource" {
			return nil, context.DeadlineExceeded
		}
		if api == "listFFmpegSource" {
			listed++
			body := `{"code":0,"data":[]}`
			if listed >= 2 {
				body = `{"code":0,"data":[{"key":"a","src_url":"http://other/a.mp4"},{"key":"b","src_url":"http://other/b.mp4"}]}`
			}
			return jsonZLM(r, body), nil
		}
		return jsonZLM(r, `{"code":0}`), nil
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid:8090"})
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: &recordingAudit{}}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskFFmpegAdd, url.Values{
		"src_url": {"http://cam/a.mp4"}, "dst_url": {"rtmp://127.0.0.1:1935/live/cam"},
		"timeout_ms": {"1000"}, "enable_hls": {"0"}, "enable_mp4": {"0"},
	}, true)
	if asFloat(got["code"]) == 0 {
		t.Fatalf("unrelated new keys should not recover: %+v", got)
	}
}

func TestPullAddTimeoutIsSuccessIfListShowsNewTask(t *testing.T) {
	var listed int
	src := "rtsp://camera/live"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		switch api {
		case "addStreamProxy":
			return nil, context.DeadlineExceeded
		case "listStreamProxy":
			listed++
			body := `{"code":0,"data":[]}`
			if listed >= 2 {
				body = `{"code":0,"data":[{"key":"pull-new","url":"` + src + `","stream":"cam"}]}`
			}
			return jsonZLM(r, body), nil
		default:
			return jsonZLM(r, `{"code":0}`), nil
		}
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid:8090"})
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: &recordingAudit{}}
	got := h.SourceTaskOperation("node-1", "admin", SourceTaskPullAdd, url.Values{
		"app": {"live"}, "stream": {"cam"}, "url": {src}, "timeout_sec": {"1"},
	}, true)
	if asFloat(got["code"]) != 0 || asString(got["key"]) != "pull-new" {
		t.Fatalf("result=%+v", got)
	}
}
