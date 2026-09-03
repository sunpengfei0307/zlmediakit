package controler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/service"

	"github.com/gin-gonic/gin"
)

type Page struct{}

func hx(c *gin.Context) bool {
	return c.GetHeader("HX-Request") == "true"
}

func firstNodeID() string {
	if config.C != nil && len(config.C.Nodes) > 0 && config.C.Nodes[0].ID != "" {
		return config.C.Nodes[0].ID
	}
	return "zlm-1"
}

func hostOf(c *gin.Context) string {
	h := c.Query("host")
	if h == "" {
		h = strings.Split(c.Request.Host, ":")[0]
	}
	return h
}

func baseData(c *gin.Context, active string) gin.H {
	httpsPort := 7789
	if config.C != nil && config.C.Basic.HttpsPort > 0 {
		httpsPort = config.C.Basic.HttpsPort
	}
	httpPort := 8090
	if config.C != nil && len(config.C.Nodes) > 0 && config.C.Nodes[0].HTTPPort > 0 {
		httpPort = config.C.Nodes[0].HTTPPort
	}
	return gin.H{
		"Active":       active,
		"NodeID":       firstNodeID(),
		"HTTPSPort":    httpsPort,
		"HTTPPort":     httpPort,
		"Partial":      hx(c),
		"EnableDash":   config.C != nil && config.C.Basic.EnableDash,
		"EnableSnap":   service.SnapEnabled(),
		"SnapInterval": service.SnapInterval(),
		"FFmpegBin":    service.FFmpegPath(),
		"LoginUser":    loginUserOf(c),
	}
}

func (Page) render(c *gin.Context, active string, data gin.H) {
	base := baseData(c, active)
	for k, v := range data {
		base[k] = v
	}
	name := "layout"
	if hx(c) {
		name = "content-" + active
	}
	c.HTML(http.StatusOK, name, base)
}

func (Page) Overview(c *gin.Context) {
	ov := service.H.Overview()
	data := gin.H{"Overview": ov, "KPI": overviewKPI(ov), "Frag": c.Query("frag")}
	switch c.Query("frag") {
	case "live", "live-main":
		c.HTML(http.StatusOK, "overview-live-main", merge(baseData(c, "overview"), data))
		return
	case "live-side":
		c.HTML(http.StatusOK, "overview-live-side", merge(baseData(c, "overview"), data))
		return
	}
	Page{}.render(c, "overview", data)
}

func overviewKPI(ov map[string]any) gin.H {
	b, _ := json.Marshal(ov["nodes"])
	var nodes []map[string]any
	_ = json.Unmarshal(b, &nodes)
	var streams, sess, viewers, recording, waiting, mediaSrc int
	var inSpeed, outSpeed float64
	hook := "等待"
	protos := map[string]int{}
	for _, n := range nodes {
		streams += asI(n["streams"])
		sess += asI(n["sessions"])
		viewers += asI(n["viewers"])
		inSpeed += asF(n["in_bps"])
		outSpeed += asF(n["out_bps"])
		recording += asI(n["recording"])
		waiting += asI(n["waiting"])
		mediaSrc += asI(n["media_source"])
		if asStr(n["hook_seen"]) != "" {
			hook = "有上报"
		}
		for _, row := range asAnyMaps(n["protocols"]) {
			name := asStr(row["name"])
			if name != "" {
				protos[name] += asI(row["count"])
			}
		}
	}
	shares := service.SortedProtoShares(protos)
	protoTotal := 0
	for _, p := range shares {
		protoTotal += p.Count
	}
	plist := make([]gin.H, 0, len(shares))
	for _, p := range shares {
		pct := 0
		if protoTotal > 0 {
			pct = p.Count * 100 / protoTotal
		}
		plist = append(plist, gin.H{"Name": p.Name, "Count": p.Count, "Pct": pct})
	}
	return gin.H{
		"Streams": streams, "Sessions": sess, "Viewers": viewers,
		"Speed": outSpeed, "InSpeed": inSpeed, "OutSpeed": outSpeed,
		"Recording": recording, "Waiting": waiting, "MediaSource": mediaSrc,
		"Hook": hook, "Protocols": plist,
	}
}

func (Page) Streams(c *gin.Context) {
	id := firstNodeID()
	applyListForm(c)
	lq := parseListQuery(c, "name", "asc")
	raw, _, _ := service.H.NodeAction(id, "detail", hostOf(c), url.Values{}, nil)
	detail, _ := raw.(map[string]any)
	streams := asAnyMaps(detail["streams"])
	filtered := make([]map[string]any, 0)
	ql := strings.ToLower(lq.Q)
	for _, s := range streams {
		if ql != "" {
			blob := strings.ToLower(fmt.Sprintf("%v %v %v %v %v", s["app"], s["stream"], s["video_codec"], s["audio_codec"], s["originTypeStr"]))
			if !strings.Contains(blob, ql) {
				continue
			}
		}
		filtered = append(filtered, s)
	}
	filtered = sortStreamMaps(filtered, lq.Sort, lq.Dir)
	total := len(filtered)
	paged, page, size := paginateMaps(filtered, lq.Page, lq.Size)
	lq.Page, lq.Size = page, size
	groups := groupByApp(paged)
	expand := c.Query("expand")
	expNode, expVhost, expApp, expStream, expOK := parseStreamSID(expand)
	listQS := listQueryValues(listQuery{Q: lq.Q, Sort: lq.Sort, Dir: lq.Dir, Size: lq.Size})
	if expand != "" {
		listQS.Set("expand", expand)
	}
	notice, _ := c.Get("operation_notice")
	Page{}.render(c, "streams", gin.H{
		"Detail": detail, "Groups": groups, "Q": lq.Q, "Expand": expand,
		"ExpandOK": expOK, "ExpandNode": expNode, "ExpandVhost": expVhost,
		"ExpandApp": expApp, "ExpandStream": expStream,
		"Sort": lq.Sort, "Dir": lq.Dir, "Size": lq.Size,
		"ListQuery": listQS, "Pager": buildPager("/streams", listQS, total, page, size),
		"Error": asStr(detail["error"]), "Notice": asStr(notice),
	})
}

func groupByApp(streams []map[string]any) []map[string]any {
	order := []string{}
	m := map[string][]map[string]any{}
	for _, s := range streams {
		app := asStr(s["app"])
		if app == "" {
			app = "default"
		}
		if _, ok := m[app]; !ok {
			order = append(order, app)
		}
		m[app] = append(m[app], s)
	}
	out := make([]map[string]any, 0, len(order))
	for _, app := range order {
		out = append(out, map[string]any{"App": app, "Streams": m[app], "Count": len(m[app])})
	}
	return out
}

func (Page) StreamConns(c *gin.Context) {
	id := c.DefaultQuery("node", firstNodeID())
	raw, _, _ := service.H.NodeAction(id, "stream_conns", hostOf(c), c.Request.URL.Query(), nil)
	m, _ := raw.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	notice, _ := c.Get("operation_notice")
	c.HTML(http.StatusOK, "stream-conns", streamConnsViewData(
		id, m, c.Query("sid"), c.Query("vhost"), c.Query("app"), c.Query("stream"), asStr(notice),
	))
}

func streamConnsViewData(nodeID string, data map[string]any, sid, vhost, app, stream, notice string) gin.H {
	return gin.H{
		"NodeID": nodeID, "Data": data, "SID": sid,
		"Vhost": vhost, "App": app, "Stream": stream,
		"Rows": connRows(data), "Notice": notice,
	}
}

func connRows(m map[string]any) []map[string]any {
	media, _ := m["media"].(map[string]any)
	if media == nil {
		media = map[string]any{}
	}
	srcAv := media["av_diff_ms"]
	srcAlive := asF(media["aliveSecond"])
	seen := map[string]bool{}
	out := make([]map[string]any, 0)
	add := func(s map[string]any, kind string) {
		kid := asStr(s["id"])
		if kid == "" {
			kid = asStr(s["identifier"])
		}
		if kid != "" && seen[kid] {
			return
		}
		if kid != "" {
			seen[kid] = true
		}
		row := map[string]any{}
		for k, v := range s {
			row[k] = v
		}
		row["_kind"] = kind
		row["_id"] = kid
		if kind == "拉流" {
			row["_av"] = nil
			row["_dur"] = asF(s["aliveSecond"])
		} else {
			av := s["av_diff_ms"]
			if av == nil || asStr(av) == "" {
				av = srcAv
			}
			row["_av"] = av
			d := asF(s["aliveSecond"])
			if srcAlive > d {
				d = srcAlive
			}
			row["_dur"] = d
		}
		out = append(out, row)
	}
	for _, x := range asAnyMaps(m["publishers"]) {
		add(x, "推流")
	}
	for _, x := range asAnyMaps(m["players"]) {
		add(x, "拉流")
	}
	for _, x := range asAnyMaps(m["sessions"]) {
		kind := "拉流"
		if b, ok := x["is_publisher"].(bool); ok && b {
			kind = "推流"
		}
		add(x, kind)
	}
	if len(out) == 0 && asStr(media["origin_peer"]) != "" {
		peer := asStr(media["origin_peer"])
		ip, port, _ := strings.Cut(peer, ":")
		out = append(out, map[string]any{
			"_kind": "推流", "_id": "", "_av": srcAv, "_dur": srcAlive,
			"note":    "未匹配到 session，来源 " + asStr(media["originTypeStr"]) + " " + peer,
			"peer_ip": ip, "peer_port": port,
		})
	}
	return out
}

func asAnyMaps(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, x := range t {
			if m, ok := x.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func (Page) Sessions(c *gin.Context) {
	id := firstNodeID()
	lq := parseListQuery(c, "media", "asc")
	q := strings.ToLower(lq.Q)
	raw, _, _ := service.H.NodeAction(id, "detail", hostOf(c), url.Values{}, nil)
	detail, _ := raw.(map[string]any)
	sess := asAnyMaps(detail["sessions"])
	rows := make([]map[string]any, 0)
	for _, s := range sess {
		if q != "" {
			blob := strings.ToLower(fmt.Sprintf("%v %v %v %v %v %v %v %v", s["role"], s["name"], s["peer_ip"], s["local_ip"], s["id"], s["app"], s["stream"], s["media_key"]))
			if !strings.Contains(blob, q) {
				continue
			}
		}
		rows = append(rows, s)
	}
	rows = sortSessionMaps(rows, lq.Sort, lq.Dir)
	total := len(rows)
	paged, page, size := paginateMaps(rows, lq.Page, lq.Size)
	lq.Page, lq.Size = page, size
	listQS := mergeQuery(url.Values{}, listQueryValues(lq))
	notice, _ := c.Get("operation_notice")
	Page{}.render(c, "sessions", gin.H{
		"Sessions": paged, "Groups": groupSessionsByMedia(paged), "Q": lq.Q,
		"Error": asStr(detail["error"]), "Notice": asStr(notice),
		"Sort": lq.Sort, "Dir": lq.Dir, "Size": lq.Size, "ListQuery": listQS,
		"Pager": buildPager("/sessions", listQS, total, page, size),
	})
}

func (Page) Files(c *gin.Context) {
	id := firstNodeID()
	app, stream, proto := c.Query("app"), c.Query("stream"), c.Query("proto")
	if v, ok := c.Get("files_app"); ok {
		app = fmt.Sprint(v)
	}
	if v, ok := c.Get("files_stream"); ok {
		stream = fmt.Sprint(v)
	}
	if v, ok := c.Get("files_proto"); ok {
		proto = fmt.Sprint(v)
	}
	if proto == "" {
		proto = "record"
	}
	fetch := func(a, s string) map[string]any {
		raw, _, _ := service.H.NodeAction(id, "records", hostOf(c), url.Values{
			"app": {a}, "stream": {s},
		}, nil)
		d, _ := raw.(map[string]any)
		return d
	}
	d := fetch(app, stream)
	apps := map[string][]string{}
	switch t := d["apps"].(type) {
	case map[string][]string:
		apps = t
	case map[string]any:
		for k, v := range t {
			switch ss := v.(type) {
			case []string:
				apps[k] = ss
			case []any:
				for _, x := range ss {
					apps[k] = append(apps[k], fmt.Sprint(x))
				}
			}
		}
	}
	appNames := make([]string, 0, len(apps))
	for a := range apps {
		appNames = append(appNames, a)
	}
	sort.Strings(appNames)
	if app == "" {
		if _, ok := apps["live"]; ok {
			app = "live"
		} else if len(appNames) == 1 {
			app = appNames[0]
		}
	}
	if stream == "" && len(apps[app]) > 0 {
		stream = apps[app][0]
	} else if stream != "" && app != "" {
		ok := false
		for _, s := range apps[app] {
			if s == stream {
				ok = true
				break
			}
		}
		if !ok && len(apps[app]) > 0 {
			stream = apps[app][0]
		}
	}
	if (c.Query("app") == "" || c.Query("stream") == "") && app != "" && stream != "" {
		d = fetch(app, stream)
		switch t := d["apps"].(type) {
		case map[string][]string:
			apps = t
		case map[string]any:
			apps = map[string][]string{}
			for k, v := range t {
				switch ss := v.(type) {
				case []string:
					apps[k] = ss
				case []any:
					for _, x := range ss {
						apps[k] = append(apps[k], fmt.Sprint(x))
					}
				}
			}
		}
		appNames = appNames[:0]
		for a := range apps {
			appNames = append(appNames, a)
		}
		sort.Strings(appNames)
	}
	groups := asGroupSlice(d["groups"])
	var cur map[string]any
	for _, g := range groups {
		if asStr(g["id"]) == proto {
			cur = g
			break
		}
	}
	if cur == nil {
		cur = map[string]any{"id": proto, "enabled": true, "files": nil, "note": "暂无文件"}
	}
	lq := parseListQuery(c, "mtime", "desc")
	panel := strings.TrimSpace(c.Query("panel"))
	if v, ok := c.Get("files_panel"); ok {
		panel = asStr(v)
	}
	switch panel {
	case "record", "event", "vod":
	default:
		panel = "record"
	}
	files := filterMediaFilesByPanel(mediaFilesOf(cur["files"]), panel)
	files = filterMediaFiles(files, lq.Q)
	files = sortMediaFiles(files, lq.Sort, lq.Dir)
	total := len(files)
	paged, page, size := paginateMediaFiles(files, lq.Page, lq.Size)
	lq.Page, lq.Size = page, size
	cur["files"] = paged
	if total == 0 && lq.Q != "" {
		cur["note"] = "无匹配文件"
	} else if total == 0 {
		if proto == "snap" {
			cur["note"] = "暂无截图文件"
		} else if panel == "event" {
			cur["note"] = "暂无事件录像"
		} else if panel == "record" {
			cur["note"] = "暂无普通录制文件"
		}
	}
	segMin := 10
	recMode := "segment"
	if cfg, ok := d["record_cfg"].(map[string]any); ok {
		if asStr(cfg["mode"]) == "single" {
			recMode = "single"
		}
		if sec := asI(cfg["mp4_max_second"]); sec >= 60 {
			segMin = sec / 60
		} else if sec > 0 {
			segMin = 1
		}
	}
	recKind := "mp4"
	if proto == "hls" {
		recKind = "hls"
	}
	notice, _ := c.Get("operation_notice")
	listQS := url.Values{"app": {app}, "stream": {stream}, "proto": {proto}, "panel": {panel}}
	listQS = mergeQuery(listQS, listQueryValues(listQuery{Q: lq.Q, Sort: lq.Sort, Dir: lq.Dir, Size: lq.Size}))
	Page{}.render(c, "files", gin.H{
		"Rec": d, "App": app, "Stream": stream, "Proto": proto, "Group": cur,
		"Groups": groups, "ProtoOptions": fileProtoOptions(groups),
		"AppNames": appNames, "Apps": apps,
		"Streams": apps[app], "RecordCfg": d["record_cfg"], "Recording": d["recording"],
		"RecSegMin": segMin, "RecMode": recMode, "RecKind": recKind, "Notice": asStr(notice),
		"Panel": panel, "EventName": eventClipName(app, stream),
		"Q": lq.Q, "Sort": lq.Sort, "Dir": lq.Dir, "Size": lq.Size,
		"ListQuery": listQS, "Pager": buildPager("/files", listQS, total, page, size),
		"RecOn": recOnForProto(d["recording"], proto),
	})
}

func recOnForProto(recording any, proto string) bool {
	m, _ := recording.(map[string]any)
	if m == nil {
		return false
	}
	key := "mp4"
	if proto == "hls" {
		key = "hls"
	}
	switch t := m[key].(type) {
	case bool:
		return t
	default:
		return false
	}
}

func eventClipName(app, stream string) string {
	stamp := time.Now().Format("20060102-150405")
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" && stream == "" {
		return "event-clip-" + stamp + ".mp4"
	}
	if app == "" {
		app = "app"
	}
	if stream == "" {
		stream = "stream"
	}
	return "event-" + app + "-" + stream + "-" + stamp + ".mp4"
}

func fileProtoOptions(groups []map[string]any) []map[string]any {
	by := map[string]map[string]any{}
	for _, g := range groups {
		by[asStr(g["id"])] = g
	}
	labels := [][2]string{
		{"record", "record"}, {"ts", "http-ts"}, {"fmp4", "http-mp4"}, {"hls", "hls"}, {"dash", "dash"}, {"snap", "snap"},
	}
	out := make([]map[string]any, 0, len(labels))
	seen := map[string]bool{}
	add := func(id, label string) {
		if seen[id] {
			return
		}
		seen[id] = true
		g := by[id]
		if g == nil {
			g = map[string]any{"id": id, "enabled": true}
		}
		row := map[string]any{}
		for k, v := range g {
			row[k] = v
		}
		row["id"] = id
		row["label"] = label
		out = append(out, row)
	}
	for _, p := range labels {
		add(p[0], p[1])
	}
	return out
}

func asStringMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case map[string]string:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = val
		}
		return out
	default:
		return nil
	}
}

func asGroupSlice(v any) []map[string]any {
	switch t := v.(type) {
	case []map[string]any:
		return t
	case []map[string]string:
		out := make([]map[string]any, 0, len(t))
		for _, m := range t {
			out = append(out, asStringMap(m))
		}
		return out
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, x := range t {
			if m := asStringMap(x); m != nil {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func filesRecordAction(op string) (string, bool) {
	switch strings.TrimSpace(op) {
	case "start":
		return "startRecord", true
	case "stop":
		return "stopRecord", true
	default:
		return "", false
	}
}

func (Page) FilesRecord(c *gin.Context) {
	action, ok := filesRecordAction(c.Param("op"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"code": -1, "msg": "unknown operation"})
		return
	}
	maxSec := strings.TrimSpace(c.PostForm("max_second"))
	if maxSec == "" {
		if c.PostForm("mode") == "single" {
			maxSec = "31536000"
		} else {
			min := asI(c.PostForm("seg_min"))
			if min < 1 {
				min = 10
			}
			maxSec = fmt.Sprintf("%d", min*60)
		}
	}
	q := url.Values{
		"vhost": {c.PostForm("vhost")},
		"app":   {c.PostForm("app")}, "stream": {c.PostForm("stream")},
		"kind": {c.PostForm("kind")}, "type": {c.PostForm("kind")},
		"max_second": {maxSec},
	}
	result := service.H.RecordVODOperation(firstNodeID(), loginUserOf(c), action, q)
	success := "已开始录制"
	if action == "stopRecord" {
		success = "已停止录制"
	}
	msg := operationPageMessage(result, success, "录制操作失败")
	setToast(c, msg)
	c.Set("operation_notice", msg)
	applyListForm(c)
	app, stream := c.PostForm("app"), c.PostForm("stream")
	kind := strings.ToLower(strings.TrimSpace(c.PostForm("kind")))
	nextProto := c.PostForm("proto")
	if action == "startRecord" {
		if kind == "hls" {
			nextProto = "hls"
		} else {
			nextProto = "record"
		}
	}
	if nextProto == "" {
		nextProto = "record"
	}
	c.Set("files_app", app)
	c.Set("files_stream", stream)
	c.Set("files_proto", nextProto)
	c.Set("files_panel", "record")
	next := url.Values{"app": {app}, "stream": {stream}, "proto": {nextProto}, "panel": {"record"}}
	next = mergeQuery(next, listQueryValues(parseListQuery(c, "mtime", "desc")))
	c.Request.URL.RawQuery = next.Encode()
	c.Header("HX-Push-Url", "/files?"+next.Encode())
	c.Request.Method = http.MethodGet
	Page{}.Files(c)
}

func recordVODAction(raw string) (string, bool) {
	action := strings.TrimSpace(raw)
	switch action {
	case "loadMP4File", "startRecordTask", "setRecordSpeed", "seekRecordStamp",
		"pauseStream", "seekStream", "setStreamSpeed", "deleteRecordFile":
		return action, true
	default:
		return "", false
	}
}

func (Page) FilesVOD(c *gin.Context) {
	action, ok := recordVODAction(c.Param("op"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"code": -1, "msg": "unknown operation"})
		return
	}
	q := url.Values{}
	for _, key := range []string{
		"schema", "vhost", "app", "stream", "file_path", "file_repeat", "seek_ms",
		"speed", "path", "back_ms", "forward_ms", "back_sec", "forward_sec", "stamp", "position",
	} {
		if value, exists := c.GetPostForm(key); exists {
			q.Set(key, value)
			continue
		}
		if value := strings.TrimSpace(c.Query(key)); value != "" {
			q.Set(key, value)
		}
	}
	var result map[string]any
	var msg string
	if action == "deleteRecordFile" {
		paths := uniqueNonEmpty(c.PostFormArray("file_path"))
		if len(paths) == 0 && q.Get("file_path") != "" {
			paths = []string{q.Get("file_path")}
		}
		if len(paths) > 1 {
			ok, fail := 0, 0
			for _, p := range paths {
				r := service.H.RecordVODOperation(firstNodeID(), loginUserOf(c), action, url.Values{"file_path": {p}})
				if asI(r["code"]) == 0 {
					ok++
				} else {
					fail++
				}
			}
			msg = batchResultMessage(ok, fail, "删除")
			code := 0
			if ok == 0 {
				code = -1
			}
			result = map[string]any{"code": code, "msg": msg}
		} else {
			if len(paths) == 1 {
				q.Set("file_path", paths[0])
			}
			result = service.H.RecordVODOperation(firstNodeID(), loginUserOf(c), action, q)
			msg = operationPageMessage(result, "已删除录像文件", "操作失败")
		}
	} else {
		result = service.H.RecordVODOperation(firstNodeID(), loginUserOf(c), action, q)
		success := map[string]string{
			"loadMP4File":      "已加载为点播流",
			"startRecordTask":  "已截录当前流",
			"deleteRecordFile": "已删除录像文件",
			"setRecordSpeed":   "录像播放速度已设置",
			"seekRecordStamp":  "录像播放位置已调整",
			"pauseStream":      "已暂停 ZLM 代理流",
			"seekStream":       "代理流位置已调整",
			"setStreamSpeed":   "代理流速度已设置",
		}[action]
		msg = operationPageMessage(result, success, "操作失败")
	}
	_ = result
	setToast(c, msg)
	c.Set("operation_notice", msg)
	applyListForm(c)
	c.Set("files_app", strings.TrimSpace(c.PostForm("view_app")))
	c.Set("files_stream", strings.TrimSpace(c.PostForm("view_stream")))
	c.Set("files_proto", strings.TrimSpace(c.PostForm("view_proto")))
	panel := strings.TrimSpace(c.PostForm("view_panel"))
	if panel == "" {
		switch action {
		case "startRecordTask":
			panel = "event"
		case "deleteRecordFile":
			if panel == "" {
				panel = "record"
			}
		case "setRecordSpeed", "seekRecordStamp", "pauseStream", "seekStream", "setStreamSpeed":
			panel = "vod"
		default:
			panel = "record"
		}
	}
	c.Set("files_panel", panel)
	next := url.Values{
		"app": {asStr(c.MustGet("files_app"))}, "stream": {asStr(c.MustGet("files_stream"))},
		"proto": {asStr(c.MustGet("files_proto"))}, "panel": {panel},
	}
	next = mergeQuery(next, listQueryValues(parseListQuery(c, "mtime", "desc")))
	c.Request.URL.RawQuery = next.Encode()
	c.Header("HX-Push-Url", "/files?"+next.Encode())
	c.Request.Method = http.MethodGet
	Page{}.Files(c)
}

func (Page) Config(c *gin.Context) {
	Page{}.renderConfig(c, c.Query("hint"), nil, nil, nil)
}

func cfgNode(cfg map[string]any) config.Node {
	if cfg == nil {
		return config.Node{}
	}
	if n, ok := cfg["node"].(config.Node); ok {
		return n
	}
	return config.Node{}
}

func opsFromNode(n config.Node, ffmpeg string) map[string]string {
	base := "/data/zlm"
	if p := strings.ReplaceAll(n.MP4Save, "\\", "/"); strings.HasSuffix(p, "/mp4") {
		base = strings.TrimSuffix(p, "/mp4")
	}
	keep := n.LiveKeepSec
	if keep <= 0 {
		keep = 600
	}
	return map[string]string{
		"root": n.Root, "bin": n.Bin, "api": n.API, "ini": n.INI, "log_dir": n.LogDir,
		"base": base, "ffmpeg": ffmpeg, "live_keep_sec": strconv.Itoa(keep),
		"snap_interval": strconv.Itoa(service.SnapInterval()),
	}
}

func cfgValue(cats []cfgCatView, key string) string {
	for _, c := range cats {
		for _, g := range c.Groups {
			for _, it := range g.Items {
				if it.Key == key {
					return it.Value
				}
			}
		}
	}
	return ""
}

func overlayCfgPosted(cats []cfgCatView, posted url.Values, errBy map[string]string) []cfgCatView {
	for i := range cats {
		for j := range cats[i].Groups {
			has := false
			for k, it := range cats[i].Groups[j].Items {
				if orig := posted.Get("orig." + it.Key); orig != "" {
					cats[i].Groups[j].Items[k].Orig = orig
				}
				if _, ok := posted[it.Key]; ok {
					cats[i].Groups[j].Items[k].Value = posted.Get(it.Key)
				}
				if msg := errBy[it.Key]; msg != "" {
					cats[i].Groups[j].Items[k].Err = msg
					has = true
				}
			}
			cats[i].Groups[j].HasErr = has
		}
	}
	return cats
}

func (Page) renderConfig(c *gin.Context, hint string, ops map[string]string, opsErr map[string]string, cats []cfgCatView) {
	id := firstNodeID()
	raw, _, _ := service.H.NodeAction(id, "config", hostOf(c), url.Values{}, nil)
	cfg, _ := raw.(map[string]any)
	if cfg == nil {
		cfg = map[string]any{"code": -1, "msg": "无法读取配置"}
	}
	if cats == nil {
		cats = classifyCfgGroups(cfg)
	}
	n := cfgNode(cfg)
	if ops == nil {
		ops = opsFromNode(n, service.FFmpegPath())
	}
	if opsErr == nil {
		opsErr = map[string]string{}
	}
	persist := true
	if v, ok := ops["persist"]; ok {
		persist = v != "0" && v != "false"
	}
	dash := config.C != nil && config.C.Basic.EnableDash
	if _, ok := ops["enable_dash"]; ok {
		dash = ops["enable_dash"] == "1" || ops["enable_dash"] == "on"
	}
	snapOn := service.SnapEnabled()
	if _, ok := ops["enable_snap"]; ok {
		snapOn = ops["enable_snap"] == "1" || ops["enable_snap"] == "on"
	}
	if _, ok := ops["snap_interval"]; !ok || strings.TrimSpace(ops["snap_interval"]) == "" {
		ops["snap_interval"] = strconv.Itoa(service.SnapInterval())
	}
	if _, ok := ops["live_keep_sec"]; !ok || strings.TrimSpace(ops["live_keep_sec"]) == "" {
		keep := n.LiveKeepSec
		if v := cfgValue(cats, "hls.deleteDelaySec"); v != "" {
			if sec, err := strconv.Atoi(v); err == nil {
				keep = sec
			}
		}
		ops["live_keep_sec"] = strconv.Itoa(service.ClampLiveKeepSec(keep))
	}
	Page{}.render(c, "config", gin.H{
		"Cfg": cfg, "Hint": hint, "Notice": hint, "CfgCats": cats,
		"Ops": ops, "OpsErr": opsErr, "OpsPersist": persist, "EnableDash": dash,
		"EnableSnap": snapOn, "FFmpegBin": service.FFmpegPath(),
	})
}

type cfgItemView struct {
	Key, Name, Value, Orig, Err, Place string
}
type cfgGroupView struct {
	Section string
	Items   []cfgItemView
	HasErr  bool
}
type cfgCatView struct {
	ID, Title, Hint string
	Groups          []cfgGroupView
}

func classifyCfgSection(sec string) string {
	switch strings.ToLower(sec) {
	case "general", "api", "http", "hook", "shell", "record":
		return "basic"
	case "protocol", "rtmp", "rtsp", "rtc", "rtp", "rtp_proxy", "hls", "srt", "multicast":
		return "protocol"
	case "cluster":
		return "cluster"
	default:
		return "plugin"
	}
}

func classifyCfgGroups(cfg map[string]any) []cfgCatView {
	cats := []cfgCatView{
		{ID: "basic", Title: "基础配置", Hint: "API / HTTP / Hook / record"},
		{ID: "protocol", Title: "协议配置", Hint: "RTMP / RTSP / WebRTC / HLS"},
		{ID: "cluster", Title: "集群配置", Hint: "origin / edge 集群与转推"},
		{ID: "plugin", Title: "插件配置", Hint: "FFmpeg 源、日志及其它插件项"},
	}
	idx := map[string]int{"basic": 0, "protocol": 1, "cluster": 2, "plugin": 3}
	for _, g := range cfgGroupMaps(cfg) {
		sec := asStr(g["section"])
		i := idx[classifyCfgSection(sec)]
		row := cfgGroupView{Section: sec}
		for _, it := range asGroupSlice(g["items"]) {
			row.Items = append(row.Items, cfgItemView{
				Key: asStr(it["key"]), Name: asStr(it["name"]), Value: asStr(it["value"]), Orig: asStr(it["value"]),
				Place: service.CfgPlaceholder(asStr(it["key"])),
			})
		}
		cats[i].Groups = append(cats[i].Groups, row)
	}
	return cats
}

func cfgGroupMaps(cfg map[string]any) []map[string]any {
	if cfg == nil {
		return nil
	}
	if g := asGroupSlice(cfg["groups"]); len(g) > 0 {
		return g
	}
	flat := map[string]string{}
	switch t := cfg["raw"].(type) {
	case []any:
		if len(t) > 0 {
			if m, ok := t[0].(map[string]any); ok {
				for k, v := range m {
					flat[k] = fmt.Sprint(v)
				}
			}
		}
	case map[string]any:
		for k, v := range t {
			flat[k] = fmt.Sprint(v)
		}
	}
	if len(flat) == 0 {
		return nil
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	order := []string{}
	by := map[string][]map[string]any{}
	for _, k := range keys {
		cat, name := k, k
		if i := strings.Index(k, "."); i > 0 {
			cat, name = k[:i], k[i+1:]
		}
		if _, ok := by[cat]; !ok {
			order = append(order, cat)
		}
		by[cat] = append(by[cat], map[string]any{"key": k, "name": name, "value": flat[k]})
	}
	out := make([]map[string]any, 0, len(order))
	for _, cat := range order {
		out = append(out, map[string]any{"section": cat, "items": by[cat]})
	}
	return out
}

func (Page) ConfigSave(c *gin.Context) {
	_ = c.Request.ParseForm()
	changed := map[string]string{}
	for k, vs := range c.Request.PostForm {
		if strings.HasPrefix(k, "orig.") || k == "" || k == "secret" || k == "api.secret" {
			continue
		}
		cur := ""
		if len(vs) > 0 {
			cur = vs[0]
		}
		orig := c.PostForm("orig." + k)
		if cur != orig {
			changed[k] = cur
		}
	}
	if len(changed) == 0 {
		setToast(c, "没有修改")
		Page{}.renderConfig(c, "没有修改", nil, nil, nil)
		return
	}
	issues := service.ValidateZLMConfig(changed)
	if service.HasFatalCfgIssue(issues) {
		msg := service.FormatCfgIssues(issues)
		setToast(c, msg)
		id := firstNodeID()
		raw, _, _ := service.H.NodeAction(id, "config", hostOf(c), url.Values{}, nil)
		cfg, _ := raw.(map[string]any)
		cats := overlayCfgPosted(classifyCfgGroups(cfg), c.Request.PostForm, service.IssueByKey(issues))
		Page{}.renderConfig(c, msg, nil, nil, cats)
		return
	}
	id := firstNodeID()
	body, _ := json.Marshal(changed)
	raw, _, _ := service.H.NodeAction(id, "set_config", hostOf(c), url.Values{}, body)
	msg := fmt.Sprintf("已保存 %d 项并即时生效", len(changed))
	if m, ok := raw.(map[string]any); ok {
		code := fmt.Sprint(m["code"])
		if code != "0" && m["code"] != nil && code != "" {
			msg = asStr(m["msg"])
			if msg == "" {
				msg = "保存失败"
			}
			setToast(c, msg)
			fresh, _, _ := service.H.NodeAction(id, "config", hostOf(c), url.Values{}, nil)
			cfg, _ := fresh.(map[string]any)
			errBy := map[string]string{}
			if mm, ok := m["issues"].([]service.CfgIssue); ok {
				errBy = service.IssueByKey(mm)
			}
			cats := overlayCfgPosted(classifyCfgGroups(cfg), c.Request.PostForm, errBy)
			Page{}.renderConfig(c, msg, nil, nil, cats)
			return
		}
		if rk, ok := m["restart_keys"].([]string); ok && len(rk) > 0 {
			msg += " · 需重启 MediaServer: " + strings.Join(rk, ", ")
		}
		if warns, ok := m["warnings"].([]string); ok && len(warns) > 0 {
			msg += " · " + strings.Join(warns, "；")
		}
	}
	setToast(c, msg)
	Page{}.renderConfig(c, msg, nil, nil, nil)
}

func (Page) ConfigOpsSave(c *gin.Context) {
	ops := map[string]string{
		"root": c.PostForm("root"), "bin": c.PostForm("bin"), "api": c.PostForm("api"),
		"ini": c.PostForm("ini"), "log_dir": c.PostForm("log_dir"), "base": c.PostForm("base"),
		"ffmpeg": c.PostForm("ffmpeg"), "live_keep_sec": c.PostForm("live_keep_sec"),
		"snap_interval": c.PostForm("snap_interval"),
	}
	persist := c.PostForm("persist") == "on" || c.PostForm("persist") == "1"
	if persist {
		ops["persist"] = "1"
	} else {
		ops["persist"] = "0"
	}
	dash := c.PostForm("enable_dash") == "on" || c.PostForm("enable_dash") == "1"
	if dash {
		ops["enable_dash"] = "1"
	} else {
		ops["enable_dash"] = "0"
	}
	snapOn := c.PostForm("enable_snap") == "on" || c.PostForm("enable_snap") == "1"
	if snapOn {
		ops["enable_snap"] = "1"
	} else {
		ops["enable_snap"] = "0"
	}
	issues := service.ValidateOpsConfig(service.OpsConfig{
		Root: ops["root"], Bin: ops["bin"], API: ops["api"], INI: ops["ini"], LogDir: ops["log_dir"],
		Base: ops["base"], FFmpeg: ops["ffmpeg"], Persist: persist, EnableDash: dash,
		EnableSnap: snapOn, SnapIntervalRaw: ops["snap_interval"], CheckSnap: true,
		LiveKeepRaw: ops["live_keep_sec"], CheckLiveKeep: true,
	})
	if service.HasFatalCfgIssue(issues) {
		msg := service.FormatCfgIssues(issues)
		setToast(c, msg)
		Page{}.renderConfig(c, msg, ops, service.IssueByKey(issues), nil)
		return
	}
	keep, _ := service.ParseLiveKeepSecStrict(ops["live_keep_sec"])
	id := firstNodeID()
	body, _ := json.Marshal(map[string]any{
		"root": ops["root"], "bin": ops["bin"], "api": ops["api"],
		"ini": ops["ini"], "log_dir": ops["log_dir"], "persist": persist,
	})
	raw, _, _ := service.H.NodeAction(id, "set_monitor", hostOf(c), url.Values{}, body)
	if m, ok := raw.(map[string]any); ok && fmt.Sprint(m["code"]) != "0" && asStr(m["msg"]) != "" {
		setToast(c, asStr(m["msg"]))
		Page{}.renderConfig(c, asStr(m["msg"]), ops, service.IssueByKey(issues), nil)
		return
	}
	pathBody, _ := json.Marshal(map[string]any{"base": ops["base"], "live_keep_sec": keep})
	praw, _, _ := service.H.NodeAction(id, "media_paths", hostOf(c), url.Values{}, pathBody)
	if m, ok := praw.(map[string]any); ok && fmt.Sprint(m["code"]) != "0" && asStr(m["msg"]) != "" {
		setToast(c, asStr(m["msg"]))
		Page{}.renderConfig(c, asStr(m["msg"]), ops, service.IssueByKey(issues), nil)
		return
	}
	msg := "运维台配置已保存并即时生效"
	if err := service.ApplyDashSettings(dash, ops["ffmpeg"]); err != nil {
		msg = "监控与落盘已保存，DASH 失败: " + err.Error()
	}
	interval, _ := strconv.Atoi(strings.TrimSpace(ops["snap_interval"]))
	if err := service.ApplySnapSettings(snapOn, interval); err != nil {
		msg += " · 截图配置未写入: " + err.Error()
	}
	if warns := []string{}; true {
		if m, ok := raw.(map[string]any); ok {
			if ws, ok := m["warns"].([]string); ok {
				warns = append(warns, ws...)
			}
		}
		for _, it := range issues {
			if !it.Fatal {
				warns = append(warns, it.String())
			}
		}
		if len(warns) > 0 {
			msg += " · " + strings.Join(warns, "；")
		}
	}
	setToast(c, msg)
	Page{}.renderConfig(c, msg, nil, nil, nil)
}

func (Page) Push(c *gin.Context) {
	Page{}.render(c, "push", gin.H{})
}

func (Page) Events(c *gin.Context) {
	tab := "events"
	if c.Query("tab") == "logs" {
		tab = "logs"
	}
	Page{}.render(c, "events", observePayload(c, tab))
}

func (Page) Logs(c *gin.Context) {
	Page{}.render(c, "logs", observePayload(c, "logs"))
}

func observePayload(c *gin.Context, tab string) gin.H {
	id := firstNodeID()
	file := c.Query("file")
	lv := c.DefaultQuery("lv", "DIWE")
	q := c.Query("q")
	var events any
	var logs any
	if service.H != nil {
		events = service.H.Events()
		logs = service.H.Logs(id, file, lv, 1200)
	} else {
		events = map[string]any{}
		logs = map[string]any{}
	}
	return gin.H{"Events": events, "Logs": logs, "File": file, "Lv": lv, "Q": q, "ObsTab": tab}
}

func (Page) Kick(c *gin.Context) {
	id := c.DefaultPostForm("node", firstNodeID())
	sid := strings.TrimSpace(c.PostForm("id"))
	raw := service.H.CoreOperation(id, loginUserOf(c), "kick_session", url.Values{"id": {sid}})
	msg := "已踢掉"
	if asStr(raw["msg"]) != "" {
		msg = asStr(raw["msg"])
	}
	if asI(raw["code"]) != 0 && !strings.Contains(msg, "失败") {
		msg = "踢掉失败: " + msg
	}
	setToast(c, msg)
	c.Set("operation_notice", msg)
	if c.PostForm("app") != "" && c.PostForm("stream") != "" {
		c.Request.Method = http.MethodGet
		c.Request.URL.RawQuery = url.Values{
			"node": {id}, "vhost": {c.PostForm("vhost")},
			"app": {c.PostForm("app")}, "stream": {c.PostForm("stream")},
			"sid": {c.PostForm("sid")}, "id": {sid},
		}.Encode()
		Page{}.StreamConns(c)
		return
	}
	if c.PostForm("from") == "sessions" || strings.Contains(c.GetHeader("HX-Current-URL"), "/sessions") {
		applyListForm(c)
		c.Request.Method = http.MethodGet
		c.Request.URL.RawQuery = listQueryValues(parseListQuery(c, "id", "asc")).Encode()
		Page{}.Sessions(c)
		return
	}
	c.Request.Method = http.MethodGet
	Page{}.Streams(c)
}

func (Page) CloseStreams(c *gin.Context) {
	id := c.DefaultPostForm("node", firstNodeID())
	result := service.H.CoreOperation(id, loginUserOf(c), "close_streams", url.Values{
		"vhost": {c.PostForm("vhost")}, "app": {c.PostForm("app")}, "stream": {c.PostForm("stream")},
	})
	msg := operationPageMessage(result, "已关闭流", "关闭流失败")
	setToast(c, msg)
	c.Set("operation_notice", msg)
	applyListForm(c)
	c.Request.Method = http.MethodGet
	next := listQueryValues(parseListQuery(c, "name", "asc"))
	if exp := c.PostForm("view_expand"); exp != "" {
		next.Set("expand", exp)
	}
	c.Request.URL.RawQuery = next.Encode()
	Page{}.Streams(c)
}

func (Page) KickSessions(c *gin.Context) {
	id := c.DefaultPostForm("node", firstNodeID())
	result := service.H.CoreOperation(id, loginUserOf(c), "kick_sessions", url.Values{
		"peer_ip": {c.PostForm("peer_ip")}, "local_port": {c.PostForm("local_port")},
	})
	msg := operationPageMessage(result, "已执行批量踢出", "批量踢出失败")
	setToast(c, msg)
	c.Set("operation_notice", msg)
	applyListForm(c)
	c.Request.Method = http.MethodGet
	c.Request.URL.RawQuery = listQueryValues(parseListQuery(c, "id", "asc")).Encode()
	Page{}.Sessions(c)
}

func (Page) KickSelected(c *gin.Context) {
	id := c.DefaultPostForm("node", firstNodeID())
	ids := uniqueNonEmpty(c.PostFormArray("id"))
	ok, fail := 0, 0
	for _, sid := range ids {
		raw := service.H.CoreOperation(id, loginUserOf(c), "kick_session", url.Values{"id": {sid}})
		if asI(raw["code"]) == 0 {
			ok++
		} else {
			fail++
		}
	}
	msg := batchResultMessage(ok, fail, "踢出")
	setToast(c, msg)
	c.Set("operation_notice", msg)
	applyListForm(c)
	c.Request.Method = http.MethodGet
	c.Request.URL.RawQuery = listQueryValues(parseListQuery(c, "id", "asc")).Encode()
	Page{}.Sessions(c)
}

func operationPageMessage(result map[string]any, success, failure string) string {
	if msg := strings.TrimSpace(asStr(result["msg"])); msg != "" {
		return msg
	}
	if asI(result["code"]) == 0 {
		return success
	}
	return failure
}

func setToast(c *gin.Context, msg string) {
	b, _ := json.Marshal(map[string]string{"toast": msg})
	c.Header("HX-Trigger", jsonForHTTPHeader(b))
}

// jsonForHTTPHeader 把 UTF-8 JSON 转成 ASCII \uXXXX，避免 HX-Trigger 被浏览器按 Latin-1 解码成乱码。
func jsonForHTTPHeader(b []byte) string {
	var buf strings.Builder
	buf.Grow(len(b) + 32)
	for _, r := range string(b) {
		if r < 0x80 {
			buf.WriteRune(r)
			continue
		}
		if r > 0xFFFF {
			r -= 0x10000
			fmt.Fprintf(&buf, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
			continue
		}
		fmt.Fprintf(&buf, `\u%04x`, r)
	}
	return buf.String()
}

func merge(a, b gin.H) gin.H {
	out := gin.H{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
