package controler

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestOnvifWebRTCTemplateExplainsBoundariesAndConfirmsEveryMutation(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Notice": "扫描完成",
		"ONVIFReady": true, "WebRTCReady": true,
		"Devices": []map[string]any{{
			"onvif_url": "http://10.0.0.2/onvif/device_service",
			"location":  "Lab", "name": "Camera A", "hardware": "IPC-X",
		}},
		"RTC": map[string]any{
			"Rooms":   map[string]any{"Items": []map[string]any{{"room_id": "room-a"}}},
			"Keepers": map[string]any{"Items": []map[string]any{{"room_key": "keeper-a", "room_id": "room-a"}}},
			"Player":  map[string]any{"Item": map[string]any{"stream_key": "proxy-a"}},
		},
		"PlayerKey": "proxy-a",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-onvif-webrtc", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"ONVIF发现", "WebRTC Rooms", "RoomKeepers", "Proxy Player详情",
		"proto-split", "proto-col",
		"怎么用", "直播管理", "设备服务 URL",
		"设备服务 URL", "不一定是 RTSP", "不会自动导入",
		"WHIP", "WHEP", "信令", "delete_webrtc", "一次性 token", "DELETE", "不接入",
		`action="/onvif-webrtc/scan"`, `action="/onvif-webrtc/keeper/add"`,
		`action="/onvif-webrtc/keeper/delete"`, `action="/onvif-webrtc/import-pull"`,
		`name="timeout_ms"`, `name="subnet_prefix"`, `name="server_host"`,
		`name="server_port"`, `name="room_id"`, `name="ssl"`, `name="room_key"`,
		`name="url"`, `name="app"`, `name="stream"`,
		`for="onvif-timeout"`, `for="onvif-subnet"`, `for="keeper-host"`,
		`for="import-url"`, `aria-label=`, "Lab", "Camera A", "IPC-X",
		"三段 IPv4 前缀", `placeholder="例如 192.168.1`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in template output", want)
		}
	}
	raw, err := os.ReadFile("../web/templates/onvif-webrtc.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, stale := range []string{`index . "manufacturer"`, `index . "model"`} {
		if strings.Contains(string(raw), stale) {
			t.Fatalf("template still uses non-contract field %q", stale)
		}
	}
	for _, action := range []string{
		"/onvif-webrtc/scan", "/onvif-webrtc/keeper/add",
		"/onvif-webrtc/keeper/delete", "/onvif-webrtc/import-pull",
	} {
		start := strings.Index(out, `action="`+action+`"`)
		end := strings.Index(out[start:], "</form>")
		if start < 0 || end < 0 || !strings.Contains(out[start:start+end], "hx-confirm=") {
			t.Fatalf("form %s lacks independent confirmation", action)
		}
	}
}

func TestLayoutLinksAndRendersOnvifWebRTCPage(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		`href="/protocols"`, "协议管理",
		`eq .Active "protocols"`, `template "content-protocols"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("layout missing %q", want)
		}
	}
}
