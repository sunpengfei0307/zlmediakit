package controler

import (
	"bytes"
	"html/template"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

func TestClassifyCfgGroupsItemsStringMap(t *testing.T) {
	cfg := map[string]any{
		"groups": []map[string]any{
			{"section": "api", "items": []map[string]string{
				{"key": "api.secret", "name": "secret", "value": "x"},
			}},
			{"section": "hls", "items": []map[string]string{
				{"key": "hls.segDur", "name": "segDur", "value": "2"},
			}},
			{"section": "cluster", "items": []map[string]string{
				{"key": "cluster.origin_url", "name": "origin_url", "value": ""},
			}},
			{"section": "ffmpeg", "items": []map[string]string{
				{"key": "ffmpeg.bin", "name": "bin", "value": "/usr/bin/ffmpeg"},
			}},
			{"section": "record", "items": []map[string]string{
				{"key": "record.fastStart", "name": "fastStart", "value": "1"},
			}},
		},
	}
	cats := classifyCfgGroups(cfg)
	if len(cats) != 4 {
		t.Fatalf("cats=%d", len(cats))
	}
	if cats[0].ID != "basic" || len(cats[0].Groups) != 2 || cats[0].Groups[0].Items[0].Key != "api.secret" {
		t.Fatalf("basic: %+v", cats[0])
	}
	if cats[0].Groups[1].Section != "record" {
		t.Fatalf("record should be basic: %+v", cats[0])
	}
	if cats[1].ID != "protocol" || len(cats[1].Groups) != 1 || cats[1].Groups[0].Section != "hls" {
		t.Fatalf("protocol: %+v", cats[1])
	}
	if cats[1].Groups[0].Items[0].Place == "" {
		t.Fatal("segDur should have placeholder range")
	}
	if len(cats[2].Groups) != 1 || len(cats[3].Groups) != 1 {
		t.Fatalf("cluster/plugin empty: %+v %+v", cats[2], cats[3])
	}
}

func TestConfigTemplateShowsFolds(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Hint": "",
		"Cfg":  map[string]any{"code": 0, "node": map[string]any{"Root": "/data/zlm"}},
		"Ops": map[string]string{
			"root": "/data/zlm", "bin": "", "api": "http://127.0.0.1:8090",
			"ini": "", "log_dir": "", "base": "/data/zlm", "ffmpeg": "/usr/bin/ffmpeg",
			"live_keep_sec": "600", "snap_interval": "15",
		},
		"OpsErr":     map[string]string{},
		"OpsPersist": true,
		"EnableDash": true,
		"EnableSnap": false,
		"CfgCats": classifyCfgGroups(map[string]any{
			"groups": []map[string]any{
				{"section": "api", "items": []map[string]string{
					{"key": "api.apiDebug", "name": "apiDebug", "value": "1"},
				}},
				{"section": "hls", "items": []map[string]string{
					{"key": "hls.segDur", "name": "segDur", "value": "2"},
				}},
				{"section": "record", "items": []map[string]string{
					{"key": "record.fastStart", "name": "fastStart", "value": "1"},
				}},
			},
		}),
		"FFmpegBin": "/data/sunpf/ffmpeg-builds/build/release/bin/ffmpeg",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-config", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`class="cfg-fold"`, "apiDebug", "▸", "基础配置", "协议配置", "落盘根目录", "开启 DASH", "cfg-zone-ops", "cfg-zone-zlm", "cfg-zone-advanced", "运维台", "高级操作", "id=\"cfgSearch\"", "/data/zlm/{app}/{stream}/dash.mpd", `id="opsForm"`, ">加载<", ">保存<", `id="opsSaveBtn"`, `id="zlmSaveBtn"`, `data-cfg-save="ops"`, `data-cfg-save="zlm"`, "直播切片保留秒数", "live_keep_sec", "有效范围 30-86400 秒", "开启定时截图", "snap_interval", "{stream}/{yyyy-MM-dd}/{HHmmss}.jpg"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `name="enable_vhost"`) || strings.Contains(out, "虚拟主机 enableVhost") {
		t.Fatal("operations pane still exposes virtual host setting")
	}
	if strings.Contains(out, "切换监控路径") || strings.Contains(out, "保存 DASH") || strings.Contains(out, "按协议生成") {
		t.Fatal("left pane still has extra save buttons")
	}
	if strings.Contains(out, "www/{app}") {
		t.Fatal("DASH hint still mentions www/")
	}
	iOps := strings.Index(out, "cfg-zone-ops")
	iPath := strings.Index(out, "落盘根目录")
	iDash := strings.Index(out, "开启 DASH")
	iZlm := strings.Index(out, "cfg-zone-zlm")
	iBasic := strings.Index(out, "基础配置")
	iAdv := strings.Index(out, "cfg-zone-advanced")
	if iOps < 0 || iPath < iOps || iDash < iPath || iAdv < iDash || iZlm < iAdv || iBasic < iZlm {
		t.Fatalf("zone order ops=%d path=%d dash=%d adv=%d zlm=%d basic=%d", iOps, iPath, iDash, iAdv, iZlm, iBasic)
	}
	if !strings.Contains(out, `id="opsSaveBtn"`) || !strings.Contains(out, `id="zlmSaveBtn"`) {
		t.Fatal("config save buttons need stable ids")
	}
	opsBtn := out[strings.Index(out, `id="opsSaveBtn"`):]
	if i := strings.Index(opsBtn, ">"); i > 0 {
		opsBtn = opsBtn[:i]
	}
	zlmBtn := out[strings.Index(out, `id="zlmSaveBtn"`):]
	if i := strings.Index(zlmBtn, ">"); i > 0 {
		zlmBtn = zlmBtn[:i]
	}
	if !strings.Contains(opsBtn, "disabled") || !strings.Contains(zlmBtn, "disabled") {
		t.Fatal("save buttons must start disabled until the matching pane is dirty")
	}
}

func TestConfigSaveGuardsAssets(t *testing.T) {
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsOut := string(js)
	for _, want := range []string{
		"function initCfgDirtyGuards(",
		"function syncCfgSaveButtons(",
		"function checkSnapIntervalInput(",
		"function checkOpsFieldInput(",
		"function looksAbsPath(",
		"checkSnapIntervalInput(",
		"initCfgDirtyGuards()",
		"须为 http(s)://host:port",
		"必须是绝对路径",
		"有效范围 5-300 秒",
	} {
		if !strings.Contains(jsOut, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	cssOut := string(css)
	for _, want := range []string{
		".cfg-err",
		".cfg-zone-actions .ghost:disabled",
	} {
		if !strings.Contains(cssOut, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
}

func TestLayoutDoesNotPollClock(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "layout", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "/ui/clock") || strings.Contains(out, `id="clock"`) {
		t.Fatal("layout still polls obsolete clock endpoint")
	}
}

func TestOverlayCfgPostedMarksErrors(t *testing.T) {
	cats := classifyCfgGroups(map[string]any{
		"groups": []map[string]any{
			{"section": "http", "items": []map[string]string{
				{"key": "http.port", "name": "port", "value": "8090"},
			}},
		},
	})
	posted := url.Values{"http.port": {"70000"}, "orig.http.port": {"8090"}}
	cats = overlayCfgPosted(cats, posted, map[string]string{"http.port": "端口须为 0-65535 的整数"})
	it := cats[0].Groups[0].Items[0]
	if it.Value != "70000" || it.Orig != "8090" || it.Err == "" || !cats[0].Groups[0].HasErr {
		t.Fatalf("%+v group=%+v", it, cats[0].Groups[0])
	}
}

func TestFileProtoOptionsNoFLV(t *testing.T) {
	opts := fileProtoOptions(nil)
	ids := make([]string, 0, len(opts))
	for _, o := range opts {
		id := asStr(o["id"])
		ids = append(ids, id)
		if id == "flv" {
			t.Fatal("http-flv should not appear")
		}
	}
	if strings.Join(ids, ",") != "record,ts,fmp4,hls,dash,snap" {
		t.Fatalf("ids=%v", ids)
	}
}

func TestEventsTemplateHasFilter(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Events": map[string]any{
			"names": []string{"on_publish", "on_play"},
			"events": []map[string]any{
				{"Time": "12:00:00", "Event": "on_publish", "Server": "zlm-1", "Body": map[string]any{"app": "live"}},
			},
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-events", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`id="qEvent"`, `id="eventKind"`, `on_publish`, `hook-row`, `pretty-scroll`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, `class="ops-tab is-on"`) || !strings.Contains(out, `href="/logs"`) {
		t.Fatal("events tab should be on when ObsTab is empty")
	}
	buf.Reset()
	data["ObsTab"] = "logs"
	data["Lv"] = "DIWE"
	data["Logs"] = map[string]any{"files": []map[string]any{}, "lines": []string{}}
	if err := tpl.ExecuteTemplate(&buf, "content-logs", data); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	if !strings.Contains(out, `href="/logs"`) || !strings.Contains(out, `class="ops-tab is-on"`) {
		t.Fatal("logs tab should be on")
	}
	if strings.Count(out, `class="ops-tab is-on"`) != 1 {
		t.Fatalf("exactly one observe tab should be on:\n%s", out)
	}
}

func TestFilesTemplateRecordHLSNotFLV(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"App": "live", "Stream": "cam", "Proto": "record", "RecKind": "mp4",
		"RecSegMin": 10, "RecMode": "segment",
		"AppNames": []string{"live"}, "Streams": []string{"cam"},
		"ProtoOptions": fileProtoOptions(nil),
		"Recording":    map[string]any{},
		"Group":        map[string]any{"note": "", "files": nil},
		"NodeID":       "zlm-1",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-files", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, `value="flv"`) {
		t.Fatal("FLV recording option should be removed")
	}
	if !strings.Contains(out, `value="hls"`) || !strings.Contains(out, `value="mp4"`) {
		t.Fatalf("want mp4+hls recording options in:\n%s", out)
	}
}

func TestFilesTemplateExposesAccessibleRecordVODControls(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		`method="post"`, `hx-post="/files/vod/loadMP4File"`, `name="file_path"`,
		"加载为点播流", `id="vodDrawer"`, "side-drawer",
		`hx-post="/files/vod/startRecordTask"`, "即时截录", "截录当前流",
		`hx-post="/files/vod/setRecordSpeed"`, `hx-post="/files/vod/seekRecordStamp"`,
		`hx-post="/files/vod/deleteRecordFile"`, "删除",
		"录像播放控制", "仅适用于由录像加载产生的流",
		"点播中", "复制播放链接", "预览点播", `data-play=`,
		`hx-confirm=`, `<label`, `role="status"`,
		`rec-topbar`, `rec-topbar-main`, `rec-topbar-right`, `rec-topbar-actions`,
		`hx-vals=`, `vod-badge`,
		`id="recSearch"`, `th-sort`, `table-pager`, `rec-foot`,
		"回溯秒数", "再录秒数", `name="back_sec"`, `name="forward_sec"`, "时长",
		`<th class="col-rec-act">操作</th>`,
		`<th class="col-idx">序号</th>`,
		`colspan="9"`,
		`.RecOn`, `disabled`, "正在录制", "停止录制",
		"GOP 缓存", "预览图片",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("files template missing %q", want)
		}
	}
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	cs := string(css)
	for _, want := range []string{
		".rec-topbar-main", ".rec-topbar-actions", ".rec-bar .rec-topbar-actions",
		"max-width: 320px", "flex: 0 1 320px",
		"min-width: max-content", "flex-wrap: nowrap",
		".rec-foot", "grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr)",
		"aspect-ratio: 16 / 9",
	} {
		if !strings.Contains(cs, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
}

func TestFilesTableIndexColumnAndThemeWidths(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	idx := strings.Index(out, `class="col-idx"`)
	check := strings.Index(out, `class="col-check"`)
	if idx < 0 || check < 0 || idx > check {
		t.Fatal("files table must put 序号 before the checkbox column")
	}
	if !strings.Contains(out, "序号") || !strings.Contains(out, "seqNo") {
		t.Fatal("files table must render a serial number column")
	}
	for _, want := range []string{`class="col-file"`, `class="col-dir"`, `class="col-type"`, `class="col-size"`, `class="col-mtime"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("files table missing column class %q", want)
		}
	}
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	jsOut := string(js)
	for _, want := range []string{
		"rec-table", "fitColumns", "col-idx", "recColSpec", "headerHozAlign",
	} {
		if !strings.Contains(jsOut, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	cs := string(css)
	for _, want := range []string{
		".rec-table .col-idx",
		".rec-table .col-dir",
		".tabulator .tabulator-frozen",
		"--crud-header-bg",
		".rec-table thead th",
		".rec-grid .tabulator-col .tabulator-col-title",
	} {
		if !strings.Contains(cs, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
}

func TestRecordStartStopButtonsSitOnRecordBar(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	bar := strings.Index(out, `id="recRecordBar"`)
	start := strings.Index(out, `hx-post="/files/record/start"`)
	stop := strings.Index(out, `hx-post="/files/record/stop"`)
	if bar < 0 || start < 0 || stop < 0 {
		t.Fatal("record bar or start/stop buttons missing")
	}
	if start < bar || stop < bar {
		t.Fatal("start/stop recording buttons must sit on the record bar row")
	}
	topbarEnd := strings.Index(out, `id="recRecordBar"`)
	if strings.Contains(out[:topbarEnd], `hx-post="/files/record/start"`) {
		t.Fatal("start recording button still in the top toolbar")
	}
}

func TestFilesTemplateLocksStartButtonWhileRecording(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"App": "live", "Stream": "cam", "Proto": "record", "Panel": "record",
		"RecKind": "mp4", "RecSegMin": 10, "RecMode": "segment", "RecOn": true,
		"AppNames": []string{"live"}, "Streams": []string{"cam"},
		"ProtoOptions": fileProtoOptions(nil),
		"Recording":    map[string]any{"mp4": true},
		"Group": map[string]any{"note": "", "files": []service.MediaFile{{
			Name: "a.mp4", Path: "a.mp4", Dir: "/data/zlm", Role: "rec_mp4", Ext: ".mp4",
			Size: 1024, ModTime: "2026-09-02T17:00:00+08:00", DurationSec: 12.5,
		}}},
		"NodeID": "zlm-1",
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "content-files", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	start := strings.Index(out, `hx-post="/files/record/start"`)
	end := strings.Index(out[start:], "</form>")
	if start < 0 || end < 0 || !strings.Contains(out[start:start+end], " disabled") {
		t.Fatal("start recording must be disabled while RecOn")
	}
	stop := strings.Index(out, `hx-post="/files/record/stop"`)
	stopEnd := strings.Index(out[stop:], "</form>")
	if stop < 0 || strings.Contains(out[stop:stop+stopEnd], " disabled") {
		t.Fatal("stop recording must stay enabled while RecOn")
	}
	if !strings.Contains(out, "正在录制") || !strings.Contains(out, "时长") || !strings.Contains(out, "12s") {
		t.Fatal("recording status and duration column missing")
	}
}

func TestRecordVODActionOnlyAllowsNamedOperations(t *testing.T) {
	for _, action := range []string{
		"loadMP4File", "startRecordTask", "setRecordSpeed", "seekRecordStamp",
		"pauseStream", "seekStream", "setStreamSpeed", "deleteRecordFile",
	} {
		if got, ok := recordVODAction(action); !ok || got != action {
			t.Fatalf("action %q rejected as %q ok=%v", action, got, ok)
		}
	}
	for _, action := range []string{"", "downloadFile", "getSnap", "close_stream"} {
		if _, ok := recordVODAction(action); ok {
			t.Fatalf("action %q unexpectedly allowed", action)
		}
	}
}

func TestFilesRecordActionOnlyAllowsStartAndStop(t *testing.T) {
	for op, want := range map[string]string{"start": "startRecord", "stop": "stopRecord"} {
		if got, ok := filesRecordAction(op); !ok || got != want {
			t.Fatalf("op=%q got=%q ok=%v", op, got, ok)
		}
	}
	for _, op := range []string{"", "delete", "startRecord", "stopRecord"} {
		if _, ok := filesRecordAction(op); ok {
			t.Fatalf("op %q unexpectedly allowed", op)
		}
	}
}

func TestEveryFilesWriteFormHasOwnConfirmation(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	noConfirm := []string{
		"/files/record/start",
		"/files/record/stop",
		"/files/vod/loadMP4File",
		"/files/vod/startRecordTask",
		"/files/vod/setRecordSpeed",
		"/files/vod/seekRecordStamp",
	}
	for _, endpoint := range noConfirm {
		re := regexp.MustCompile(`(?s)<(?:form|button)\b[^>]*hx-post="` + regexp.QuoteMeta(endpoint) + `"[^>]*>`)
		tag := re.FindString(out)
		if tag == "" {
			t.Fatalf("missing form/button for %s", endpoint)
		}
		if strings.Contains(tag, `hx-confirm="`) {
			t.Fatalf("routine control %s should not ask confirm: %s", endpoint, tag)
		}
	}
	re := regexp.MustCompile(`(?s)<(?:form|button)\b[^>]*hx-post="/files/vod/deleteRecordFile"[^>]*>`)
	for _, tag := range re.FindAllString(out, -1) {
		if !strings.Contains(tag, `hx-confirm="`) {
			t.Fatalf("delete control lacks hx-confirm: %s", tag)
		}
	}
	if !strings.Contains(out, `确认删除录像文件`) || !strings.Contains(out, `确认删除当前页勾选的录像文件`) {
		t.Fatal("delete confirms missing")
	}
}

func TestBatchResultMessage(t *testing.T) {
	if got := batchResultMessage(3, 0, "踢出"); got != "已踢出 3 项" {
		t.Fatalf("all ok: %s", got)
	}
	if got := batchResultMessage(2, 1, "删除"); got != "删除成功 2 / 失败 1" {
		t.Fatalf("partial: %s", got)
	}
	if got := batchResultMessage(0, 0, "踢出"); got != "请先勾选要操作的行" {
		t.Fatalf("empty: %s", got)
	}
}

func TestSessionGroupRowsCollapseWithoutStreamTable(t *testing.T) {
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(js)
	if !strings.Contains(out, `closest('tr.app-row')`) {
		t.Fatal("app.js must toggle session/stream group rows")
	}
	if strings.Contains(out, `appRow && e.target.closest('table.stream-table')`) {
		t.Fatal("session groups are not stream-table; collapse must not require that class")
	}
}

func TestSessionsAndFilesSupportCurrentPageBatchSelect(t *testing.T) {
	sess, err := os.ReadFile("../web/templates/sessions.html")
	if err != nil {
		t.Fatal(err)
	}
	s := string(sess)
	for _, want := range []string{
		`class="row-check"`, `id="batchCheckAll"`, `data-batch-root="sessions"`,
		`hx-post="/ui/sessions/kick-selected"`, "踢出选中", `class="batch-bar"`,
		`{{template "table-pager" .Pager}}`, "关联流", "app-row", ".Groups",
		`sortURL "/sessions" .ListQuery "media"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("sessions missing %q", want)
		}
	}
	files, err := os.ReadFile("../web/templates/files.html")
	if err != nil {
		t.Fatal(err)
	}
	f := string(files)
	for _, want := range []string{
		`class="row-check"`, `id="batchCheckAll"`, `data-batch-root="files"`,
		"下载选中", "删除选中", `data-batch-download`, `class="batch-bar"`, "js-grid",
	} {
		if !strings.Contains(f, want) {
			t.Fatalf("files missing %q", want)
		}
	}
	if strings.Contains(f, "</table>") && strings.Contains(f, `{{template "table-pager" .Pager}}`) {
		tableAt := strings.LastIndex(f, "</table>")
		pagerAt := strings.Index(f, `{{template "table-pager" .Pager}}`)
		between := f[tableAt:pagerAt]
		if !strings.Contains(between, "</div>") {
			t.Fatal("files pager must sit outside .table-wrap so it stays pinned")
		}
	}
	js, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(js)
	for _, want := range []string{
		"function initBatchSelect(",
		"function selectedRowChecks(",
		"data-batch-download",
		"kick-selected",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestTablePagerPinnedToContentBottom(t *testing.T) {
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	out := string(css)
	for _, want := range []string{
		".table-pager",
		"margin-top: auto",
		"position: sticky",
		"bottom: 0",
		"justify-content: center",
		".batch-bar",
		".rec-foot",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("theme.css missing %q", want)
		}
	}
	streams, err := os.ReadFile("../web/templates/streams.html")
	if err != nil {
		t.Fatal(err)
	}
	st := string(streams)
	toolbarPager := strings.Index(st, `<div class="toolbar">`)
	pager := strings.Index(st, `{{template "table-pager" .Pager}}`)
	frame := strings.Index(st, `stream-table-frame`)
	if toolbarPager < 0 || pager < 0 || frame < 0 || pager < frame {
		t.Fatal("streams pager must be after the table frame, not inside the toolbar")
	}
}

func TestPreviewPlayerUsesFixedStage(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Count(out, `class="player-video-wrap"`) < 2 {
		t.Fatal("live and file preview must share a fixed player stage")
	}
	if !strings.Contains(out, `id="fileVideo"`) || !strings.Contains(out, `id="video"`) {
		t.Fatal("preview videos missing")
	}
	if !strings.Contains(out, `id="fileImage"`) || !strings.Contains(out, `id="fileStage"`) {
		t.Fatal("file preview must include an image viewer for snapshots")
	}
	css, err := os.ReadFile("../web/static/theme.css")
	if err != nil {
		t.Fatal(err)
	}
	theme := string(css)
	if !strings.Contains(theme, ".player-video-wrap.is-image") || !strings.Contains(theme, "#fileImage") {
		t.Fatal("theme.css must style snapshot image preview")
	}
}

func TestCoreOperationTemplatesUsePostConfirmAndVisibleControls(t *testing.T) {
	tests := []struct {
		file  string
		wants []string
	}{
		{
			file: "../web/templates/streams.html",
			wants: []string{
				`method="post"`, `hx-post="/ui/streams/close"`, `hx-confirm=`,
				`name="vhost"`, `name="app"`, `name="stream"`, "关闭该流",
			},
		},
		{
			file: "../web/templates/sessions.html",
			wants: []string{
				`method="post"`, `hx-post="/ui/sessions/kick"`, `hx-confirm=`,
				`name="peer_ip"`, `name="local_port"`, "批量踢出",
			},
		},
		{
			file:  "../web/templates/stream-conns.html",
			wants: []string{`method="post"`, `hx-confirm=`, "踢掉"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			raw, err := os.ReadFile(tt.file)
			if err != nil {
				t.Fatal(err)
			}
			out := string(raw)
			for _, want := range tt.wants {
				if !strings.Contains(out, want) {
					t.Fatalf("%s missing %q", tt.file, want)
				}
			}
		})
	}
}

func TestStreamsTemplateHasMiddleColumnsScrollbar(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/streams.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"stream-table-frame", "stream-x-scroll", "stream-x-scroll-space", `class="col-pull"`,
		"th-sort", "table-pager", `name="q"`,
		"stream-split", "stream-detail-pane", `id="streamDetail"`, "推拉会话",
		"stream-split-handle", "is-closed", `withQuery "/streams" .ListQuery "expand" ""`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("streams template missing %q", want)
		}
	}
	if strings.Contains(out, "expand-row") {
		t.Fatal("stream details must sit in the right pane, not an expand-row")
	}
	if strings.Contains(out, "every 15s") || strings.Contains(out, "streamListPoll") || strings.Contains(out, "每 15 秒") {
		t.Fatal("live stream list must not auto-refresh")
	}
}

func TestPreviewUsesDisplayedPublicPlayUrl(t *testing.T) {
	raw, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, `const $ = (id) => document.getElementById(id)`) {
		t.Fatal("app.js missing $ helper")
	}
	if strings.Contains(out, "function zlmBrowserUrl(") {
		t.Fatal("preview must not rewrite public URLs through zlmBrowserUrl")
	}
	if strings.Contains(out, "stun:stun.l.google.com") {
		t.Fatal("WebRTC preview must not wait on public STUN")
	}
	for _, want := range []string{
		"function assertPublicPlayUrl(",
		"function webrtcSignalUrl(",
		"function displayedMediaUrl(",
		"function newRtcPeer(",
		"playHls(url,",
		"index/api/webrtc",
		"iceServers: []",
		"playoutDelayHint",
		"jitterBufferTarget",
		"typ relay",
		"当前浏览器无法解码 H.265 HLS",
		"等待出画",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
}

func TestFilesPreviewUsesDisplayedPublicHLSUrl(t *testing.T) {
	raw, err := os.ReadFile("../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if strings.Contains(out, "正在通过 ZLM 播放直播 HLS") {
		t.Fatal("file preview must not hide behind admin ZLM proxy")
	}
	if strings.Contains(out, "改用 HTTP-FLV（HLS init.mp4 不可用）") {
		t.Fatal("file HLS preview must not fall back to FLV")
	}
	if !strings.Contains(out, "noMseFallback") || !strings.Contains(out, "canplay") {
		t.Fatal("MP4 file preview must keep native Range playback and not wait to download the whole file")
	}
	if strings.Contains(out, "fetch(url, { signal: ac.signal, credentials: 'include' })") &&
		!strings.Contains(out, "noMseFallback: true") {
		t.Fatal("recorded MP4 preview must not fall back to full-file MSE download")
	}
	for _, want := range []string{
		"function displayedMediaUrl(",
		"function isLiveHlsPreview(",
		"function isImagePreview(",
		"function showFileImage(",
		"xhrSetup: (xhr) => { xhr.withCredentials = true; }",
		"displayedMediaUrl(m3u8)",
		"credentials: 'omit'",
		"/hls_init?",
		"noMseFallback: true",
		"kind === 'snap'",
		"live_snap",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("app.js missing %q", want)
		}
	}
	if !strings.Contains(out, "isImagePreview(path, kind, role)") {
		t.Fatal("jpg/snap preview must branch to image viewer before the video player")
	}
}

func TestStreamsAssetsInitializeMiddleColumnsScrollbar(t *testing.T) {
	checks := map[string][]string{
		"../web/static/app.js": {
			"function initStreamTableScroll()",
			"function initStreamSplit()",
			"if (page === 'streams') { initStreamTableScroll(); initStreamSplit(); }",
			"minmax(",
			"1fr",
			"1 / 2",
			"if (!$('content'))",
			"--stream-viewport",
		},
		"../web/static/theme.css": {
			"--stream-fixed-left: 300px",
			"--stream-fixed-right: 356px",
			"--stream-viewport",
			".stream-x-scroll",
			".subtbl .col-kick",
			".subtbl .col-id",
			".expand-detail",
			"padding: 0 16px",
			".stream-split",
			".stream-detail-pane",
			".stream-split-handle",
			".stream-split.is-closed",
			"minmax(0, 50%)",
			"position: sticky; right: 0",
		},
	}
	for file, wants := range checks {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		out := string(raw)
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Fatalf("%s missing %q", file, want)
			}
		}
	}
}

func TestOverviewTemplateShowsZLMVersion(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/overview.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"BuildTime", "BranchName", "CommitHash", "VersionError", "版本不可用",
		`hx-get="/streams"`, `hx-get="/sessions"`, `hx-get="/events"`, `hx-get="/files"`,
		"kpi-link", `hx-swap="innerHTML"`, `hx-disinherit`,
		"ov-page", "ov-left", "ov-side", "入口码率", "出口码率", "录制中",
		"MediaSource", "TcpSession", "RtpPacket", "KPI.InSpeed", "KPI.OutSpeed",
		"chartBitrate", "chartNet", "协议构成", "frag=live-main", "frag=live-side",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("overview missing %q", want)
		}
	}
}

func TestStreamDetailTemplateShowsMediaInfoErrorsLocally(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/stream-conns.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{"media_error", "media_online", "media_info", "expand-summary", "expand-detail", `class="col-kick"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("stream detail missing %q", want)
		}
	}
}

func TestStreamConnsViewCarriesPersistentOperationNotice(t *testing.T) {
	data := streamConnsViewData(
		"node-1",
		map[string]any{"media_online": true, "media_online_known": true},
		"sid-1", "__defaultVhost__", "live", "cam", "踢出失败",
	)
	if data["Notice"] != "踢出失败" {
		t.Fatalf("notice=%v", data["Notice"])
	}
}

func TestStreamConnsTemplateRendersStructuredMediaStatus(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"Notice": "踢出失败：会话不存在",
		"Data": map[string]any{
			"media_online_known": true,
			"media_online":       false,
			"media_info": map[string]any{
				"schema": "rtmp", "vhost": "__defaultVhost__", "app": "live", "stream": "cam",
				"readerCount": 2.0, "totalReaderCount": 3.0, "bytesSpeed": 4096.0, "aliveSecond": 12.0,
			},
		},
		"Rows": []map[string]any{},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "stream-conns", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"离线", "rtmp", "live/cam", "2 / 3"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "map[") {
		t.Fatalf("template printed Go map: %s", out)
	}
}

func TestOverviewKPIAggregatesZLMMetrics(t *testing.T) {
	kpi := overviewKPI(map[string]any{
		"nodes": []map[string]any{
			{
				"streams": 2, "sessions": 5, "viewers": 7,
				"in_bps": 1000.0, "out_bps": 4000.0, "bytes_speed": 1000.0,
				"recording": 1, "waiting": 1, "media_source": 6,
				"hook_seen": "2026-09-03",
				"protocols": []map[string]any{{"name": "rtmp", "count": 2}, {"name": "rtsp", "count": 1}},
			},
			{
				"streams": 1, "sessions": 2, "viewers": 1,
				"in_bps": 500.0, "out_bps": 500.0,
				"recording": 0, "waiting": 0, "media_source": 3,
				"protocols": []map[string]any{{"name": "rtmp", "count": 1}},
			},
		},
	})
	if kpi["Streams"] != 3 || kpi["Sessions"] != 7 || kpi["Viewers"] != 8 {
		t.Fatalf("basic kpi=%v", kpi)
	}
	if kpi["InSpeed"] != 1500.0 || kpi["OutSpeed"] != 4500.0 || kpi["Speed"] != 4500.0 {
		t.Fatalf("speed kpi=%v", kpi)
	}
	if kpi["Recording"] != 1 || kpi["Waiting"] != 1 || kpi["MediaSource"] != 9 || kpi["Hook"] != "有上报" {
		t.Fatalf("extra kpi=%v", kpi)
	}
	protos, _ := kpi["Protocols"].([]gin.H)
	if len(protos) != 2 || protos[0]["Name"] != "rtmp" || protos[0]["Count"] != 3 {
		t.Fatalf("protocols=%v", kpi["Protocols"])
	}
}

func TestOverviewLiveTemplateRendersNewMetrics(t *testing.T) {
	tpl, err := template.New("").Funcs(tmplFuncs()).ParseGlob("../web/templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	data := map[string]any{
		"KPI": map[string]any{
			"Streams": 2, "Viewers": 4, "Sessions": 6,
			"InSpeed": 1024.0, "OutSpeed": 4096.0, "Recording": 1,
			"MediaSource": 8, "Waiting": 0, "Hook": "有上报",
			"Protocols": []map[string]any{{"Name": "rtmp", "Count": 2, "Pct": 100}},
		},
		"Overview": map[string]any{
			"nodes": []map[string]any{
				{
					"Name": "zlm-1", "ID": "zlm-1", "API": "http://127.0.0.1:80",
					"Online": true, "BuildTime": "2026-08-20", "BranchName": "main", "CommitHash": "abc123",
					"Root": "/data/zlm", "Bin": "/data/zlm/MediaServer",
					"ThreadAvg": 12.5, "MediaSource": 8, "Muxer": 2,
					"TcpSession": 5, "UdpSession": 1, "Socket": 9, "Buffer": 20, "Frame": 3,
					"RtpPacket": 4, "RtmpPacket": 6, "TcpServer": 3, "UdpServer": 2,
				},
			},
		},
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "overview-live-main", data); err != nil {
		t.Fatal(err)
	}
	if err := tpl.ExecuteTemplate(&buf, "overview-live-side", data); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"直播流", "入口码率", "录制中", "MediaSource", "TcpSession", "zlm-1", "rtmp"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestGlobalToastIsAccessibleStatusRegion(t *testing.T) {
	raw, err := os.ReadFile("../web/templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, `id="toast"`) ||
		!strings.Contains(out, `role="status"`) ||
		!strings.Contains(out, `aria-live="polite"`) {
		t.Fatal("global toast must be an accessible polite status region")
	}
}
