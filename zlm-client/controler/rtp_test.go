package controler

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
	"zlm-admin/service"
)

func TestRTPControllerMapsEveryMutationToPersistentLabel(t *testing.T) {
	for _, action := range []string{
		service.RTPOpenServer, service.RTPOpenServerMultiplex, service.RTPConnectServer,
		service.RTPCloseServer, service.RTPUpdateSSRC, service.RTPPauseCheck, service.RTPResumeCheck,
		service.RTPStartSend, service.RTPStartSendPassive, service.RTPStartSendTalk, service.RTPStopSend,
	} {
		if strings.TrimSpace(rtpActionLabel(action)) == "" {
			t.Fatalf("missing label for %s", action)
		}
	}
}

func TestRTPTemplateHasSectionsFormsLabelsFeedbackAndPerMutationConfirm(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Notice": "连接失败：目标不可达",
		"RTPReady": true,
		"RTP": map[string]any{
			"Receivers": map[string]any{"Items": []map[string]any{{"vhost": "tenant-vhost", "app": "tenant-rtp", "stream_id": "recv", "port": 10000, "exist": true}}},
			"Senders": map[string]any{"Items": []map[string]any{
				{"ssrc": "123", "_stats_unavailable": true, "_stats_note": "当前ZLM API不提供逐发送器统计"},
			}},
		},
		"QueryVhost": "__defaultVhost__", "QueryApp": "live", "QueryStream": "cam",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-rtp", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"RTP接收服务", "RTP发送任务", "ENABLE_RTPPROXY", "ops-split", "proto-split", "rtp-pane", "怎么用",
		`action="/rtp/open"`, `action="/rtp/open-multiplex"`, `action="/rtp/connect"`,
		`action="/rtp/close"`, `action="/rtp/update-ssrc"`, `action="/rtp/pause-check"`,
		`action="/rtp/resume-check"`, `action="/rtp/start-send"`,
		`action="/rtp/start-send-passive"`, `action="/rtp/start-send-talk"`, `action="/rtp/stop-send"`,
		`for="rtp-open-vhost"`, `for="rtp-mux-vhost"`, `for="rtp-connect-vhost"`,
		`for="rtp-open-stream"`, `for="rtp-connect-host"`, `for="rtp-send-dst"`, `for="rtp-talk-recv"`,
		`role="status"`, "__defaultVhost__", "tenant-vhost", "tenant-rtp", "recv", "123",
		"当前ZLM API不提供逐发送器统计", `aria-label="关闭 RTP 接收服务 recv"`,
		`aria-label="更新 recv 的 SSRC"`, `aria-label="暂停 recv 的超时检查"`, `aria-label="恢复 recv 的超时检查"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in template output", want)
		}
	}
	if strings.Contains(out, "proto-stack") {
		t.Fatal("RTP receive/send panes must sit side by side, not stacked")
	}
	if got := strings.Count(out, `hx-confirm=`); got < 11 {
		t.Fatalf("every mutation form needs independent confirmation, got %d", got)
	}
	for _, action := range []string{
		"/rtp/open", "/rtp/open-multiplex", "/rtp/connect", "/rtp/close", "/rtp/update-ssrc",
		"/rtp/pause-check", "/rtp/resume-check", "/rtp/start-send", "/rtp/start-send-passive",
		"/rtp/start-send-talk", "/rtp/stop-send",
	} {
		start := strings.Index(out, `action="`+action+`"`)
		if start < 0 {
			t.Fatalf("missing form %s", action)
		}
		end := strings.Index(out[start:], "</form>")
		if end < 0 || !strings.Contains(out[start:start+end], "hx-confirm=") {
			t.Fatalf("form %s lacks own hx-confirm", action)
		}
	}
	for _, action := range []string{"/rtp/open", "/rtp/open-multiplex", "/rtp/connect"} {
		start := strings.Index(out, `action="`+action+`"`)
		end := strings.Index(out[start:], "</form>")
		form := out[start : start+end]
		if !strings.Contains(form, `name="vhost"`) || strings.Contains(form, `type="hidden" name="vhost"`) {
			t.Fatalf("%s must expose editable vhost: %s", action, form)
		}
	}
	for _, action := range []string{"/rtp/close", "/rtp/update-ssrc", "/rtp/pause-check", "/rtp/resume-check"} {
		start := strings.Index(out, `action="`+action+`"`)
		end := strings.Index(out[start:], "</form>")
		form := out[start : start+end]
		for _, identity := range []string{`name="vhost" value="tenant-vhost"`, `name="app" value="tenant-rtp"`, `name="stream_id" value="recv"`} {
			if !strings.Contains(form, identity) {
				t.Fatalf("%s missing row identity %q: %s", action, identity, form)
			}
		}
		if !strings.Contains(form, "hx-confirm=") || !strings.Contains(form, "aria-label=") {
			t.Fatalf("%s row action lacks confirm/aria: %s", action, form)
		}
	}
}

func TestProtocolMoreParamsCanExpandAndScroll(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/rtp.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	if strings.Count(html, `class="proto-more`) < 4 {
		t.Fatalf("rtp forms should keep details.proto-more, got %d", strings.Count(html, `class="proto-more`))
	}
	cssb, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(cssb)
	for _, want := range []string{
		".proto-ops",
		"overflow: auto",
		"#content.page-protocols .rtp-pane { max-height: none; height: auto; overflow: auto; }",
		"#content.page-protocols .proto-zone.source-card",
	} {
		if !strings.Contains(css, want) {
			t.Fatalf("theme.css missing expandable pane rule %q", want)
		}
	}
	if strings.Contains(css, "#content.page-protocols .rtp-pane { max-height: none; height: auto; overflow: hidden; }") {
		t.Fatal("protocol panes must scroll when 更多参数 expands")
	}
	if strings.Contains(css, "#content.page-protocols .proto-zone.source-card {\n  display: flex; flex-direction: column; height: 100%; overflow: hidden;\n}") {
		t.Fatal("protocol zone must not clip expanded forms")
	}
}

func TestProtocolsSplitOccupiesRemainingHeight(t *testing.T) {
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	cs := string(css)
	for _, want := range []string{
		".proto-split",
		"grid-template-columns: minmax(0, 1fr) minmax(0, 1fr)",
		"align-items: stretch",
		".proto-col",
		"#content.page-protocols .proto-zone.source-card",
		"height: 100%",
		"#content.page-protocols .proto-lock",
	} {
		if !strings.Contains(cs, want) {
			t.Fatalf("theme.css missing protocol split rule %q", want)
		}
	}
	if strings.Contains(cs, ".ops-split.proto-stack") || strings.Contains(cs, ".proto-stack {") {
		t.Fatal("protocol panes must not force a vertical stack")
	}
}

func TestLayoutLinksAndRendersRTPPage(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{`href="/protocols"`, "协议管理", `eq .Active "protocols"`, `template "content-protocols"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("layout missing %q", want)
		}
	}
}

func TestPickProtocolTabRespectsExplicitAndEnablement(t *testing.T) {
	layout := service.ProtocolLayout{RTP: true, ONVIF: true, WebRTC: true}
	if got := pickProtocolTab("", layout); got != "onvif" {
		t.Fatalf("default tab=%s", got)
	}
	if got := pickProtocolTab("rtp", layout); got != "rtp" {
		t.Fatalf("explicit rtp tab=%s", got)
	}
	onlyRTP := service.ProtocolLayout{RTP: true}
	if got := pickProtocolTab("", onlyRTP); got != "rtp" {
		t.Fatalf("rtp-only default tab=%s", got)
	}
}

func TestProtocolsTabsPutGB28181Last(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/protocols.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	onvifAt := strings.Index(out, `href="/protocols?tab=onvif"`)
	webrtcAt := strings.Index(out, `href="/protocols?tab=webrtc"`)
	rtpAt := strings.Index(out, `href="/protocols?tab=rtp"`)
	if onvifAt < 0 || webrtcAt < 0 || rtpAt < 0 || !(onvifAt < webrtcAt && webrtcAt < rtpAt) {
		t.Fatalf("protocol tabs must be ONVIF, WebRTC, RTP/GB28181 last: onvif=%d webrtc=%d rtp=%d", onvifAt, webrtcAt, rtpAt)
	}
}

func TestProtocolsTemplateGatesDisabledModules(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Tab": "rtp", "EnableRTP": false, "EnableONVIF": true, "EnableWebRTC": true,
		"Notice": "", "RTP": map[string]any{"Receivers": map[string]any{"Items": []map[string]any{}}, "Senders": map[string]any{"Items": []map[string]any{}}},
		"RTC": map[string]any{"Rooms": map[string]any{"Items": []map[string]any{}}, "Keepers": map[string]any{"Items": []map[string]any{}}, "Player": map[string]any{}},
		"Devices": []map[string]any{}, "QueryVhost": "__defaultVhost__",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-protocols", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "协议管理") || !strings.Contains(out, "rtp_proxy.port") {
		t.Fatalf("disabled RTP should explain enablement: %s", out)
	}
	if !strings.Contains(out, "怎么用") {
		t.Fatal("disabled RTP should still explain how to use the page")
	}
	if !strings.Contains(out, `action="/rtp/open"`) {
		t.Fatal("disabled RTP should keep forms visible")
	}
	if !strings.Contains(out, `class="proto-lock"`) || !strings.Contains(out, " disabled") {
		t.Fatal("disabled RTP forms must be locked")
	}
}

func TestProtocolsWebRTCTemplateExplainsUsageAndLocksWhenNotReady(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Tab": "webrtc", "EnableRTP": true, "EnableONVIF": true, "EnableWebRTC": true,
		"RTPReady": true, "ONVIFReady": true, "WebRTCReady": false,
		"Notice": "", "RTP": map[string]any{"Receivers": map[string]any{"Items": []map[string]any{}}, "Senders": map[string]any{"Items": []map[string]any{}}},
		"RTC": map[string]any{
			"Rooms":   map[string]any{"Items": []map[string]any{}, "Error": "ENABLE_WEBRTC unavailable"},
			"Keepers": map[string]any{"Items": []map[string]any{}},
			"Player":  map[string]any{},
		},
		"Devices": []map[string]any{}, "QueryVhost": "__defaultVhost__",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-protocols", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"怎么用", "直播管理", "RoomKeeper", "信令", "WHIP", "WHEP",
		`action="/onvif-webrtc/keeper/add"`, `class="proto-lock"`, " disabled",
		"rtc.signalingPort", "ENABLE_WEBRTC",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in locked WebRTC page", want)
		}
	}
	if strings.Contains(out, "暂无 Room，或当前 ZLM 未启用 ENABLE_WEBRTC") {
		t.Fatal("empty room list must not pretend the compile flag is off")
	}
}
