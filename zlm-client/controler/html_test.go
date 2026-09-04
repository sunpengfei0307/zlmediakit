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
		"配置管理", "推流管理", "鉴权管理", "事件日志",
		`class="nav-ico"`, `#nav-overview`, `#nav-events`, `#nav-auth`,
		`data-theme-set="light"`, `data-theme-set="dark"`, `zlm-theme`, `theme-switch`,
		`data-nav-set="header"`, `data-nav-set="sidebar"`, `zlm-nav`, `nav-switch`, `header-tools`,
		`data-http-port=`, `class="tagsbar"`, `id="pageTabs"`, `aria-label="已打开的页面"`,
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
	start := strings.Index(out, `id="pageTabs"`)
	end := strings.Index(out[start:], "</nav>")
	if start < 0 || end < 0 {
		t.Fatal("missing opened tabs bar")
	}
	if strings.Contains(out[start:start+end], "hx-get") {
		t.Fatal("opened tabs bar must start empty and only render visited pages")
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
		`html[data-nav="sidebar"]`,
		`html[data-nav="sidebar"] body > header`,
		".theme-switch",
		".header-tools",
		".nav-switch",
		"--on-accent",
		"--surface-2",
		"--el-bg-color-page: #f2f3f5",
		"--el-text-color-primary: #141418",
		"--el-text-color-secondary: #4b5058",
		".tagsbar",
		".tab-close",
		"html[data-nav=\"sidebar\"] .user-menu-panel",
		"bottom: calc(100% + 8px)",
		".file-name-cell .file-jump",
		"#content.page-sessions .peer-acl",
		"margin-left: auto; flex: 0 0 auto;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
	if strings.Contains(out, "html[data-nav=\"sidebar\"] header {") {
		t.Fatal("sidebar header styles must target body > header, not every <header>")
	}
	if !strings.Contains(out, "html[data-nav=\"sidebar\"] body > header nav a") {
		t.Fatal("sidebar nav items must target body > header nav a")
	}
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsOut := string(js)
	for _, want := range []string{"THEME_KEY", "NAV_KEY", "OPEN_TABS_KEY", "function applyTheme(", "function initThemeToggle(", "function applyNavLayout(", "function initNavLayout(", "function renderPageTabs(", "function closeOpenTab(", "zlm-theme", "zlm-nav", "header nav a", "function syncAuthIPDirDefaults("} {
		if !strings.Contains(jsOut, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestAuthPageUsesRecordTableStyle(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/auth.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"ops-page", "rec-table", "rec-bar", "act-row", "role-chip",
		"auth-switch-form", "ghost-primary", "col-rec-act", "auth-token",
		"auth-ip-mode", "auth-ip-list", "/auth/ip/add", "/auth/ip/toggle", "黑名单", "白名单", "auth-ip-form",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth.html missing %q", want)
		}
	}
	if strings.Contains(out, "source-card") {
		t.Fatal("auth page still uses source-card layout")
	}
	if strings.Contains(out, "{{if .Notice}}") || strings.Contains(out, "已有 Token，但鉴权开关") {
		t.Fatal("auth page must not render top notice banners")
	}
}

func TestAuthMutationsUseToast(t *testing.T) {
	raw, err := os.ReadFile("page.go")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "func authDone(") || !strings.Contains(out, "setToast(c, msg)") {
		t.Fatal("auth mutations must toast via authDone")
	}
	for _, fn := range []string{"AuthEnable", "AuthAdd", "AuthDelete", "AuthToggle", "AuthIPMode", "AuthIPToggle", "AuthIPDelete"} {
		i := strings.Index(out, "func (Page) "+fn+"(")
		if i < 0 {
			t.Fatalf("missing %s", fn)
		}
		chunk := out[i:]
		if j := strings.Index(chunk[1:], "\nfunc "); j > 0 {
			chunk = chunk[:j+1]
		}
		if strings.Contains(chunk, `c.Set("operation_notice"`) {
			t.Fatalf("%s still sets operation_notice banner", fn)
		}
		if !strings.Contains(chunk, "authDone(") {
			t.Fatalf("%s must call authDone", fn)
		}
	}
}

func TestAuthDeleteDisabledWhileTokenEnabled(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/auth.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "请先停用再删除") || !strings.Contains(out, "disabled") {
		t.Fatal("enabled token delete button must be disabled")
	}
}

func TestPlayPreviewForwardsAuthToken(t *testing.T) {
	raw, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"function playUrlToken(",
		"function withPlayToken(",
		"function hlsXhrSetup(",
		"hlsXhrSetup(url)",
		"xhr.open('GET', next, true)",
		"modifyRequestURL",
	} {
		if !strings.Contains(out, want) {
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
	for _, want := range []string{`page === 'auth'`, "hozAlign: 'center'", "width: 148"} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing auth grid align rule %q", want)
		}
	}
}
