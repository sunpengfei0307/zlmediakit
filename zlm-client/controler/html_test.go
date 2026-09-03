package controler

import (
	"html/template"
	"os"
	"strings"
	"testing"
)

func TestParseHTMLTemplates(t *testing.T) {
	_, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
}

func TestLayoutShowsRenamedNav(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"运维后台", "系统概览", "直播管理", "连接管理", "信号转发", "协议管理", "录制管理",
		"配置管理", "推流管理（RTC）", "事件 / 日志",
		`data-theme-set="light"`, `data-theme-set="dark"`, `zlm-theme`, `theme-switch`,
		`data-http-port=`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(out, "ZLM <b>运维台</b>") {
		t.Fatal("logo still 运维台")
	}
	if !strings.Contains(out, "LoginUser") || !strings.Contains(out, "user-menu") {
		t.Fatal("layout missing user menu")
	}
	if !strings.Contains(out, `href="/logout"`) || !strings.Contains(out, "退出") {
		t.Fatal("user menu missing logout")
	}
	if strings.Contains(out, "<select") {
		t.Fatal("native select leftover")
	}
	refreshAt := strings.Index(out, `id="btnRefresh"`)
	userAt := strings.Index(out, `id="userMenu"`)
	if strings.Contains(out, `id="clock"`) || strings.Contains(out, "/ui/clock") {
		t.Fatal("obsolete clock polling remains in layout")
	}
	if refreshAt < 0 || userAt < 0 || userAt < refreshAt {
		t.Fatal("user menu must be at the far right after refresh")
	}
}

func TestThemeSwitchMarkupAndTokens(t *testing.T) {
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	out := string(css)
	for _, want := range []string{
		`html[data-theme="light"]`,
		".theme-switch",
		"--on-accent",
		"--surface-2",
		"--el-bg-color-page: #f2f3f5",
		"--el-text-color-primary: #141418",
		"--el-text-color-secondary: #4b5058",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsOut := string(js)
	for _, want := range []string{"THEME_KEY", "function applyTheme(", "function initThemeToggle(", "zlm-theme", "header nav a"} {
		if !strings.Contains(jsOut, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestLoginPageTemplate(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/login.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{"立即登录", `action="/login"`, `name="user"`, `name="pass"`, `data-theme-set="light"`, "theme-switch"} {
		if !strings.Contains(out, want) {
			t.Fatalf("login missing %q", want)
		}
	}
}

func TestLegacyIndexHTMLRemoved(t *testing.T) {
	if _, err := os.Stat("../web/index.html"); err == nil {
		t.Fatal("web/index.html is unused SPA; templates under web/templates are the UI")
	}
}

func TestKickFormAvoidsPipeCSSSelector(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/stream-conns.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, `hx-target="#conns-`) {
		t.Fatal("kick hx-target must not use #id with pipe characters")
	}
	if !strings.Contains(out, `hx-target="closest .subtbl-wrap"`) {
		t.Fatal("kick should target closest .subtbl-wrap")
	}
}

func TestVendorChartsAndTablesReduceCustomWidgets(t *testing.T) {
	layout, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(layout)
	for _, want := range []string{
		"echarts@5.6.0", "tabulator-tables@6.3.1", "tabulator.min.css", "echarts.min.js",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("layout missing vendor lib %q", want)
		}
	}
	ov, err := os.ReadFile("../web/templates/overview.html")
	if err != nil {
		t.Fatal(err)
	}
	ovs := string(ov)
	if !strings.Contains(ovs, `class="chart-box"`) || strings.Contains(ovs, "<canvas") {
		t.Fatal("overview charts must use ECharts containers, not custom canvas")
	}
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsOut := string(js)
	for _, want := range []string{"echarts.init", "function renderChart(", "function initOpsTables()", "new Tabulator("} {
		if !strings.Contains(jsOut, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	for _, dead := range []string{"function drawChart(", "function paintChart(", "function bindChartHover("} {
		if strings.Contains(jsOut, dead) {
			t.Fatalf("custom chart painter still present: %s", dead)
		}
	}
	files, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files), "js-grid") {
		t.Fatal("files table must opt into Tabulator")
	}
}

func TestOverviewChartsUseTimeAxisAndFilledRange(t *testing.T) {
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(js)
	for _, want := range []string{"type: 'time'", "fmtChartAxis(", "minInterval", "fromMs", "containLabel: true", "kbps", "Mbps", "* 8"} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing chart axis rule %q", want)
		}
	}
}

func TestFmtBpsUsesSiBitUnits(t *testing.T) {
	if got := fmtBps(125000); got != "1.00 Mbps" { // 125000 B/s = 1 Mbps
		t.Fatalf("1 Mbps got %q", got)
	}
	if got := fmtBps(12500); got != "100.0 kbps" { // 12500 B/s = 100 kbps
		t.Fatalf("100 kbps got %q", got)
	}
	if strings.Contains(fmtBps(125000), "MB") || strings.Contains(fmtBps(125000), "Kb/s") {
		t.Fatal("media rate must use kbps/Mbps, not MBps or Kb/s")
	}
}

func TestOpsTablesKeepDeleteColumnNarrow(t *testing.T) {
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(js)
	for _, want := range []string{"function opsColSpec(", "width: 88", "widthGrow: 0", "maxWidth: 100"} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing narrow action column rule %q", want)
		}
	}
	if strings.Contains(out, "fitDataStretch") {
		t.Fatal("ops tables must not stretch the last column")
	}
}
