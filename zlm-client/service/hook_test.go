package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"zlm-admin/core/config"
)

func TestHookReplyAuthPass(t *testing.T) {
	r := hookReply("on_play")
	if r["code"] != 0 {
		t.Fatalf("%v", r)
	}
	r = hookReply("on_stream_none_reader")
	if r["close"] != false {
		t.Fatal("none_reader should not close")
	}
	r = hookReply("on_http_access")
	if r["err"] != "" || r["second"] != 600 {
		t.Fatalf("http_access %+v", r)
	}
	r = hookReply("on_rtsp_realm")
	if r["realm"] != "" {
		t.Fatal("empty realm")
	}
}

func TestHookEventNamesComplete(t *testing.T) {
	need := []string{
		"on_publish", "on_play", "on_stream_changed", "on_stream_not_found",
		"on_stream_none_reader", "on_send_rtp_stopped", "on_rtp_server_timeout",
		"on_flow_report", "on_record_mp4", "on_record_ts", "on_http_access",
		"on_rtsp_auth", "on_rtsp_realm", "on_shell_login",
		"on_server_started", "on_server_exited", "on_server_keepalive",
	}
	got := map[string]bool{}
	for _, n := range hookEventNames {
		got[n] = true
	}
	for _, n := range need {
		if !got[n] {
			t.Fatalf("missing %s", n)
		}
	}
}

func TestHookStreamAndStore(t *testing.T) {
	if hookStream(map[string]any{"app": "live", "stream": "cam"}) != "live/cam" {
		t.Fatal("stream")
	}
	if hookStream(map[string]any{"stream_id": "gb001"}) != "gb001" {
		t.Fatal("stream_id")
	}
	if hookShouldStore("on_server_keepalive") || hookShouldStore("on_http_access") {
		t.Fatal("noisy hooks should not fill UI ring")
	}
	if !hookShouldStore("on_stream_changed") {
		t.Fatal("lifecycle should store")
	}
}

func TestOurHookURL(t *testing.T) {
	if !ourHookURL("", "http://127.0.0.1:7788/hook/on_play") {
		t.Fatal("empty should fill")
	}
	if !ourHookURL("http://127.0.0.1:7788/hook/on_play", "http://127.0.0.1:7788/hook/on_play") {
		t.Fatal("ours")
	}
	if ourHookURL("http://10.0.0.1:9000/auth", "http://127.0.0.1:7788/hook/on_play") {
		t.Fatal("must not overwrite third-party")
	}
}

func TestPublisherFlowEnded(t *testing.T) {
	if publisherFlowEnded(nil) || publisherFlowEnded(map[string]any{"app": "live"}) {
		t.Fatal("missing player must not close")
	}
	if !publisherFlowEnded(map[string]any{"player": false}) || !publisherFlowEnded(map[string]any{"player": "false"}) {
		t.Fatal("publisher disconnect must close")
	}
	if publisherFlowEnded(map[string]any{"player": true}) || publisherFlowEnded(map[string]any{"player": "1"}) {
		t.Fatal("player disconnect must not close")
	}
}

func TestHubHookLifecycleNotKeepalive(t *testing.T) {
	h := &Hub{}
	got := h.Hook("on_stream_changed", []byte(`{"app":"live","stream":"cam","schema":"rtmp","regist":true}`))
	if got["code"] != 0 {
		t.Fatalf("%v", got)
	}
	if len(h.hooks) != 1 || h.hooks[0].Event != "on_stream_changed" {
		t.Fatalf("stored=%+v", h.hooks)
	}
	h.Hook("on_server_keepalive", []byte(`{"mediaServerId":"zlm"}`))
	if len(h.hooks) != 1 {
		t.Fatal("keepalive should not fill event list")
	}
	nf := h.Hook("on_stream_not_found", []byte(`{"app":"live","stream":"missing","schema":"rtmp"}`))
	if nf["code"] != 0 {
		t.Fatal(nf)
	}
}

func TestOnFlowReportPublisherClosesStreamImmediately(t *testing.T) {
	audit := &recordingAudit{}
	formCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/close_streams") {
			_ = r.ParseForm()
			formCh <- r.Form.Encode()
			_, _ = w.Write([]byte(`{"code":0,"count_closed":3}`))
			return
		}
		t.Errorf("unexpected api %s", r.URL.Path)
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "zlm-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}

	got := h.Hook("on_flow_report", []byte(`{"app":"live","duration":130,"id":"1764-179","ip":"10.62.89.161","mediaServerId":"zlm-1","player":false,"port":47828,"protocol":"rtmp","schema":"rtmp","stream":"ls_zlm_h264_1080p","vhost":"__defaultVhost__"}`))
	if got["code"] != 0 {
		t.Fatalf("hook must reply first: %+v", got)
	}
	select {
	case form := <-formCh:
		if !strings.Contains(form, "app=live") || !strings.Contains(form, "stream=ls_zlm_h264_1080p") ||
			!strings.Contains(form, "vhost=__defaultVhost__") || !strings.Contains(form, "force=1") {
			t.Fatalf("close_streams form=%s", form)
		}
		if strings.Contains(form, "schema=") {
			t.Fatalf("must close all schemas, got %s", form)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publisher on_flow_report must immediately close_streams")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var user string
		for _, e := range audit.List() {
			if e.Action == "close_streams" && e.Phase == "result" {
				user = e.User
			}
		}
		if user == "hook" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto close must be audited as hook, got %+v", audit.List())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOnFlowReportPlayerDoesNotCloseStream(t *testing.T) {
	called := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- r.URL.Path
		_, _ = w.Write([]byte(`{"code":0}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "zlm-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: &recordingAudit{}}

	got := h.Hook("on_flow_report", []byte(`{"app":"live","mediaServerId":"zlm-1","player":true,"stream":"ls_zlm_h264_1080p","vhost":"__defaultVhost__"}`))
	if got["code"] != 0 {
		t.Fatalf("%+v", got)
	}
	select {
	case path := <-called:
		t.Fatalf("player disconnect must not close stream, called %s", path)
	case <-time.After(200 * time.Millisecond):
	}
}
