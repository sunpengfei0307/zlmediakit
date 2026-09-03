package controler

import (
	"bytes"
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestSourcesTemplateHasThreeFormsAccessibleLabelsConfirmAndFeedback(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Notice": "拉流代理新增失败：目标不可达",
		"Tasks": map[string]any{
			"Pull":   map[string]any{"Items": []map[string]any{{"key": "__defaultVhost__/proxy/0", "app": "live", "stream": "cam"}}},
			"Pusher": map[string]any{"Items": []map[string]any{{"key": "__defaultVhost__/pusher/0", "schema": "rtmp", "dst_url": "rtmp://edge/live/cam"}}},
			"FFmpeg": map[string]any{"Items": []map[string]any{{"key": "d41d8cd98f00b204e9800998ecf8427e", "cmd": "%s -re -i %s -c:v libx264 -f flv %s"}}},
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-sources", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"拉流代理", "推流代理", "FFmpeg 源",
		`action="/sources/pull/add"`, `action="/sources/pusher/add"`, `action="/sources/ffmpeg/add"`,
		`hx-post="/sources/pull/delete"`, `hx-post="/sources/pusher/delete"`, `hx-post="/sources/ffmpeg/delete"`,
		`hx-confirm=`,
		`for="pull-app"`, `for="pull-stream"`, `for="pull-url"`,
		`for="pusher-schema"`, `for="pusher-dst-url"`,
		`for="ffmpeg-src-url"`, `for="ffmpeg-dst-url"`, `for="ffmpeg-timeout-ms"`,
		`type="submit"`, "__defaultVhost__/proxy/0", "rtmp://edge/live/cam",
		"%s -re -i %s -c:v libx264 -f flv %s", "d41d8cd98f00b204e9800998ecf8427e",
		"信号转发", "HTTP-TS", "HTTP-FLV", "默认模板", ".m3u8", "HTTP-MP4",
		`class="col-act"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{
		"代理流控制", "ZLM 代理流控制", `hx-post="/files/vod/pauseStream"`,
		`hx-post="/files/vod/seekStream"`, `hx-post="/files/vod/setStreamSpeed"`,
		`<th>超时</th>`, `<th>HLS / MP4</th>`, `index . "timeout_ms"`, `index . "enable_hls"`, `index . "enable_mp4"`, `index . "status"`,
		`name="ffmpeg_cmd_key"`, `ffmpeg-cmd-key`, "命令模板 Key", "ffmpeg.cmd_copy",
	} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("unexpected %q in:\n%s", unwanted, out)
		}
	}
}

func TestSourcesTemplateRestoresFailedPullInputs(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	src := "http://10.191.6.5:8091/live/ls_cctv.mp4?token=secret"
	data := map[string]any{
		"Tab": "pull", "Notice": "拉流代理新增失败：目标不可达",
		"Tasks": map[string]any{
			"Pull":   map[string]any{"Items": []map[string]any{}},
			"Pusher": map[string]any{"Items": []map[string]any{}},
			"FFmpeg": map[string]any{"Items": []map[string]any{}},
		},
		"Form": map[string]string{
			"app": "live", "stream": "camera_01", "url": src, "timeout_sec": "10",
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-sources", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`value="live"`, `value="camera_01"`, src} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing restored %q in:\n%s", want, out)
		}
	}
}

func TestLayoutLinksAndRendersSourcesPage(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{`href="/sources"`, `eq .Active "sources"`, `template "content-sources"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("layout missing %q", want)
		}
	}
}
