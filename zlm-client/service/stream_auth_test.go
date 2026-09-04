package service

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"zlm-admin/core/config"
)

func testPlayNode() config.Node {
	return config.Node{ID: "zlm-1", HTTPPort: 8090, RTSPPort: 554, RTMPPort: 1935}
}

func TestStreamAuthDisabledAllowsAnonymous(t *testing.T) {
	h := &Hub{}
	deny, _ := h.denyStreamHook("on_publish", map[string]any{"app": "live", "stream": "cam"})
	if deny {
		t.Fatal("disabled auth must allow publish")
	}
}

func TestStreamAuthRequiresTokenWhenEnabled(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if err := h.SetStreamAuthEnabled(true); err != nil {
		t.Fatal(err)
	}
	deny, msg := h.denyStreamHook("on_publish", map[string]any{"app": "live", "stream": "cam"})
	if !deny || msg != "缺少 token" {
		t.Fatalf("deny=%v msg=%q", deny, msg)
	}
	item, err := h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0)
	if err != nil || item.Token != "secret-token" {
		t.Fatalf("add: %+v %v", item, err)
	}
	deny, _ = h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "params": "token=secret-token",
	})
	if deny {
		t.Fatal("valid token must allow")
	}
	deny, _ = h.denyStreamHook("on_play", map[string]any{
		"app": "live", "stream": "cam", "params": "token=wrong",
	})
	if !deny {
		t.Fatal("wrong token must reject")
	}
}

func TestScopedTokenDoesNotRestrictOtherStreams(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	_, err = h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0)
	if err != nil {
		t.Fatal(err)
	}
	deny, _ := h.denyStreamHook("on_publish", map[string]any{"app": "live", "stream": "cam"})
	if !deny {
		t.Fatal("restricted stream must require token")
	}
	deny, _ = h.denyStreamHook("on_publish", map[string]any{"app": "live", "stream": "other"})
	if deny {
		t.Fatal("unrestricted stream must allow anonymous push")
	}
	deny, _ = h.denyStreamHook("on_play", map[string]any{
		"app": "live", "stream": "other", "params": "token=garbage",
	})
	if deny {
		t.Fatal("unrestricted stream must ignore token")
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{"path": "/live/other/hls.m3u8"})
	if deny {
		t.Fatal("unrestricted HLS must allow anonymous")
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{"path": "/live/cam/hls.m3u8"})
	if !deny {
		t.Fatal("restricted HLS playlist must require token")
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{"path": "/live/cam/hls/seg-0.ts"})
	if deny {
		t.Fatal("HLS segments must play without repeating token")
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{"path": "/live/cam/dash/seg-1.m4s"})
	if deny {
		t.Fatal("DASH segments must play without repeating token")
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{"path": "/live/cam.live.flv"})
	if !deny {
		t.Fatal("direct HTTP-FLV must still require token")
	}
}

func TestDeleteEnabledTokenRejectedAndLastDeleteDisablesAuth(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	item, err := h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteStreamAuthToken(item.ID); err == nil {
		t.Fatal("enabled token must not delete")
	}
	if enabled, _ := h.StreamAuthView()["enabled"].(bool); !enabled {
		t.Fatal("failed delete must keep auth on")
	}
	if err := h.ToggleStreamAuthToken(item.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := h.DeleteStreamAuthToken(item.ID); err != nil {
		t.Fatal(err)
	}
	view := h.StreamAuthView()
	if enabled, _ := view["enabled"].(bool); enabled {
		t.Fatal("deleting last token must turn auth off")
	}
	if n, _ := view["count"].(int); n != 0 {
		t.Fatalf("tokens left: %v", view["count"])
	}
}

func TestPlayLinksAppendTokenOnlyForRestrictedStream(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	prev := H
	H = h
	t.Cleanup(func() { H = prev })
	_, err = h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0)
	if err != nil {
		t.Fatal(err)
	}
	n := testPlayNode()
	cam := playLinks("10.191.6.5", n, "__defaultVhost__", "live", "cam")
	other := playLinks("10.191.6.5", n, "__defaultVhost__", "live", "other")
	hlsCam, hlsOther, rtcAPI, rtcURL, srtURL, dashURL := "", "", "", "", "", ""
	for _, l := range cam {
		switch l.ID {
		case "hls":
			hlsCam = l.URL
		case "webrtc":
			rtcURL = l.URL
			for _, x := range l.Extra {
				if x["label"] == "HTTP 信令" {
					rtcAPI = x["url"]
				}
			}
		case "srt":
			srtURL = l.URL
		case "dash":
			dashURL = l.URL
		}
	}
	for _, l := range other {
		if l.ID == "hls" {
			hlsOther = l.URL
		}
	}
	if hlsCam != "http://10.191.6.5:8090/live/cam/hls.m3u8?token=secret-token" {
		t.Fatalf("restricted HLS url=%s", hlsCam)
	}
	if hlsOther != "http://10.191.6.5:8090/live/other/hls.m3u8" {
		t.Fatalf("unrestricted HLS url=%s", hlsOther)
	}
	u, err := url.Parse(rtcAPI)
	if err != nil || u.Path != "/index/api/webrtc" {
		t.Fatalf("webrtc signaling url=%s", rtcAPI)
	}
	q := u.Query()
	if q.Get("app") != "live" || q.Get("stream") != "cam" || q.Get("type") != "play" || q.Get("token") != "secret-token" {
		t.Fatalf("webrtc signaling missing token: %s", rtcAPI)
	}
	if strings.Contains(rtcURL, "token=") {
		t.Fatalf("webrtc:// must not carry query token: %s", rtcURL)
	}
	if !strings.Contains(srtURL, "streamid=#!::r=live/cam,m=request,token=secret-token") || strings.Contains(srtURL, "&token=") {
		t.Fatalf("srt token must live in streamid: %s", srtURL)
	}
	if dashURL != "http://10.191.6.5:8090/live/cam/dash.mpd?token=secret-token" {
		t.Fatalf("restricted DASH url=%s", dashURL)
	}
}

func TestStreamAuthPushOnlyToken(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	_ = h.SetStreamAuthEnabled(true)
	_, err = h.AddStreamAuthToken("push", "push-only", true, false, "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	deny, _ := h.denyStreamHook("on_publish", map[string]any{"params": "token=push-only", "app": "live", "stream": "a"})
	if deny {
		t.Fatal("push-only must publish")
	}
	deny, msg := h.denyStreamHook("on_play", map[string]any{"params": "token=push-only", "app": "live", "stream": "a"})
	if !deny || msg != "该 token 不允许播放" {
		t.Fatalf("push-only play: deny=%v msg=%q", deny, msg)
	}
}

func TestHubHookPublishDeniedWhenAuthOn(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	_ = h.SetStreamAuthEnabled(true)
	got := h.Hook("on_publish", []byte(`{"app":"live","stream":"cam","schema":"rtmp"}`))
	if asFloat(got["code"]) == 0 {
		t.Fatalf("must reject: %+v", got)
	}
	_, _ = h.AddStreamAuthToken("ok", "abc", true, true, "", "", 0)
	ok := h.Hook("on_publish", []byte(`{"app":"live","stream":"cam","params":"token=abc"}`))
	if asFloat(ok["code"]) != 0 {
		t.Fatalf("must allow: %+v", ok)
	}
}

func TestAddTokenAutoEnablesAuthAndLogsHTTPAccess(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if h.StreamAuthView()["enabled"] != false && h.StreamAuthView()["enabled"] != nil {
		view := h.StreamAuthView()
		if enabled, _ := view["enabled"].(bool); enabled {
			t.Fatal("expected auth off")
		}
	}
	_, err = h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0)
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := h.StreamAuthView()["enabled"].(bool); !enabled {
		t.Fatal("adding token must turn auth on")
	}
	deny, msg := h.denyStreamHook("on_http_access", map[string]any{"path": "/live/cam.live.flv"})
	if !deny || msg != "缺少 token" {
		t.Fatalf("http deny=%v msg=%q", deny, msg)
	}
	deny, _ = h.denyStreamHook("on_http_access", map[string]any{
		"path": "/live/cam.live.flv", "params": "token=secret-token",
	})
	if deny {
		t.Fatal("valid token must allow http-flv")
	}
	got := h.Hook("on_http_access", []byte(`{"path":"/live/cam/hls.m3u8"}`))
	if asFloat(got["code"]) == 0 || asString(got["err"]) == "" {
		t.Fatalf("hls must be rejected: %+v", got)
	}
}
