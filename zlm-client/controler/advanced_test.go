package controler

import (
	"bytes"
	"html/template"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestAdvancedActionOnlyAllowsNamedOperations(t *testing.T) {
	for op, want := range map[string]string{
		"restart":           "restartServer",
		"delete-record-dir": "deleteRecordDirectory",
		"delete-snap-dir":   "deleteSnapDirectory",
	} {
		if got, ok := advancedAction(op); !ok || got != want {
			t.Fatalf("op=%q got=%q ok=%v", op, got, ok)
		}
	}
	for _, op := range []string{"", "downloadFile", "downloadBin", "getSnap", "restartServer", "broadcast"} {
		if _, ok := advancedAction(op); ok {
			t.Fatalf("op %q unexpectedly allowed", op)
		}
	}
}

func TestConfigAdvancedZoneHasGuardedForms(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-config", map[string]any{
		"Cfg": map[string]any{"code": 0},
		"Ops": map[string]string{}, "OpsErr": map[string]string{},
		"Notice": "已请求重启 MediaServer",
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"cfg-zone-advanced", "高级操作", "二次确认",
		"不开放任意文件/二进制下载", "downloadFile", "downloadBin",
		`action="/config/advanced/restart"`,
		`action="/config/advanced/delete-record-dir"`,
		`action="/config/advanced/delete-snap-dir"`,
		`name="period"`, `name="name"`, `name="file"`,
		`for="adv-record-app"`, `for="adv-record-period"`,
		`for="adv-snap-file"`,
		"YYYY-MM-DD", "删除受限录像目录",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	raw, err := os.ReadFile("../web/templates/config.html")
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, endpoint := range []string{
		"/config/advanced/restart",
		"/config/advanced/delete-record-dir",
		"/config/advanced/delete-snap-dir",
	} {
		re := regexp.MustCompile(`(?s)<form\b[^>]*hx-post="` + regexp.QuoteMeta(endpoint) + `"[^>]*>`)
		tag := re.FindString(body)
		if tag == "" || !strings.Contains(tag, `hx-confirm="`) {
			t.Fatalf("form %s lacks independent hx-confirm: %s", endpoint, tag)
		}
	}
}
