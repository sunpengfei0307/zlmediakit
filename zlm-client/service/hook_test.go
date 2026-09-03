package service

import (
	"testing"
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
