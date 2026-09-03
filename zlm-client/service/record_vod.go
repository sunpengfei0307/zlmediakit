package service

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"zlm-admin/core/config"
)

var recordSchemas = map[string]bool{
	"rtmp": true, "rtsp": true, "fmp4": true, "ts": true,
	"hls": true, "hls.fmp4": true, "rtc": true,
}

func invalidRecordName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`) {
		return true
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validateRecordIdentity(q url.Values, requireSchema bool) (url.Values, error) {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
	if invalidRecordName(vhost) || invalidRecordName(app) || invalidRecordName(stream) {
		return nil, fmt.Errorf("vhost/app/stream 必填且不得包含控制字符或路径穿越")
	}
	out := url.Values{"vhost": {vhost}, "app": {app}, "stream": {stream}}
	if requireSchema {
		schema := strings.ToLower(strings.TrimSpace(q.Get("schema")))
		if !recordSchemas[schema] {
			return nil, fmt.Errorf("schema 必须是 rtmp/rtsp/fmp4/ts/hls/hls.fmp4/rtc")
		}
		out.Set("schema", schema)
	}
	return out, nil
}

func parseEventRecordMS(q url.Values, prefix string) (int64, error) {
	if q == nil {
		return 0, nil
	}
	if sec := strings.TrimSpace(q.Get(prefix + "_sec")); sec != "" {
		n, err := parseRecordInt(sec, prefix+"_sec", 0, 600, 0)
		if err != nil {
			return 0, err
		}
		return n * 1000, nil
	}
	return parseRecordInt(q.Get(prefix+"_ms"), prefix+"_ms", 0, 600000, 0)
}

func zlmRecordingOn(resp map[string]any) bool {
	if resp == nil {
		return false
	}
	return asTruthy(resp["status"]) || asTruthy(resp["data"]) || asTruthy(resp["result"])
}

func parseRecordInt(raw, name string, minValue, maxValue int64, defaultValue int64) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minValue || value > maxValue {
		return 0, fmt.Errorf("%s 必须是 %d..%d 的整数", name, minValue, maxValue)
	}
	return value, nil
}

func parseRecordSpeed(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.25 || value > 4 {
		return 0, fmt.Errorf("speed 必须是 0.25..4")
	}
	return value, nil
}

func resolveRecordMP4(n config.Node, raw string) (string, string, error) {
	ApplyZLMIni(&n)
	root := strings.TrimSpace(n.MP4Save)
	if root == "" {
		return "", "", fmt.Errorf("节点未配置 MP4Save")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("MP4Save 无效")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", fmt.Errorf("MP4Save 不可访问")
	}
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", "", fmt.Errorf("file_path 必填")
	}
	candidates := []string{candidate}
	if !filepath.IsAbs(candidate) {
		candidates = []string{filepath.Join(root, candidate)}
		slashCandidate := filepath.ToSlash(filepath.Clean(candidate))
		rootBase := filepath.Base(root)
		if strings.EqualFold(slashCandidate, rootBase) {
			candidates = append(candidates, root)
		} else if len(slashCandidate) > len(rootBase) &&
			strings.EqualFold(slashCandidate[:len(rootBase)+1], rootBase+"/") {
			candidates = append(candidates, filepath.Join(root, slashCandidate[len(rootBase)+1:]))
		}
		if n.Root != "" {
			if rootRel, relErr := filepath.Rel(n.Root, root); relErr == nil {
				rootRel = filepath.ToSlash(filepath.Clean(rootRel))
				if rootRel != "." && strings.HasPrefix(slashCandidate, rootRel+"/") {
					candidates = append(candidates, filepath.Join(root, strings.TrimPrefix(slashCandidate, rootRel+"/")))
				}
			}
		}
	}
	for _, candidate = range candidates {
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			continue
		}
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr != nil || !insideRoot(root, resolved) {
			continue
		}
		info, statErr := os.Stat(resolved)
		if statErr != nil || !info.Mode().IsRegular() {
			continue
		}
		if !strings.EqualFold(filepath.Ext(resolved), ".mp4") {
			return "", "", fmt.Errorf("file_path 必须是 .mp4 文件")
		}
		rel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || rel == "." || strings.HasPrefix(filepath.ToSlash(rel), "../") {
			continue
		}
		return resolved, filepath.ToSlash(rel), nil
	}
	return "", "", fmt.Errorf("MP4 文件不存在或不在允许目录内")
}

func validateRecordTaskPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" ||
		strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", fmt.Errorf("path 必须是相对 MP4 路径")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("path 不得包含控制字符")
		}
	}
	slash := strings.ReplaceAll(raw, `\`, "/")
	clean := filepath.ToSlash(filepath.Clean(slash))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") ||
		!strings.EqualFold(filepath.Ext(clean), ".mp4") {
		return "", fmt.Errorf("path 必须是无路径穿越的相对 .mp4 文件路径")
	}
	return clean, nil
}

func recordVODTarget(action string, q url.Values) string {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
	if invalidRecordName(vhost) || invalidRecordName(app) || invalidRecordName(stream) {
		return action
	}
	return vhost + "/" + app + "/" + stream
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func recordVODFailure(message string) map[string]any {
	return map[string]any{"code": -1, "msg": message}
}

func normalizeRecordVODResult(action string, result map[string]any) map[string]any {
	if result == nil {
		return recordVODFailure("ZLM 返回空结果")
	}
	successMessage := map[string]string{
		"loadMP4File":     "已加载为点播流",
		"startRecordTask": "已截录当前流",
		"deleteRecordFile": "已删除录像文件",
		"startRecord":     "已开始录制",
		"stopRecord":      "已停止录制",
		"setRecordSpeed":  "录像播放速度已设置",
		"seekRecordStamp": "录像播放位置已调整",
		"pauseStream":     "已暂停 ZLM 代理流",
		"seekStream":      "代理流位置已调整",
		"setStreamSpeed":  "代理流速度已设置",
	}[action]
	switch action {
	case "loadMP4File":
		if asFloat(result["code"]) != 0 {
			return recordVODFailure(firstNonEmpty(asString(result["msg"]), "加载点播流失败"))
		}
	case "startRecordTask":
		data, _ := result["data"].(map[string]any)
		if strings.TrimSpace(asString(data["path"])) == "" {
			return recordVODFailure("ZLM 成功响应缺少 data.path")
		}
	case "setRecordSpeed", "seekRecordStamp", "pauseStream", "seekStream", "setStreamSpeed":
		value, ok := result["result"]
		if !ok || asFloat(value) != 0 {
			return recordVODFailure("ZLM 操作未生效")
		}
	}
	result["msg"] = successMessage
	return result
}

// RecordVODOperation validates, audits and executes record/VOD mutations on one node.
func (h *Hub) RecordVODOperation(nodeID, user, action string, q url.Values) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	if h == nil || h.audit == nil {
		return recordVODFailure("操作审计不可用，已拒绝执行")
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		result := recordVODFailure("unknown node")
		h.recordAudit(nodeID, user, action, action, false, asString(result["msg"]))
		return result
	}
	target := recordVODTarget(action, q)
	if action == "deleteRecordFile" {
		if p := strings.TrimSpace(q.Get("file_path")); p != "" {
			target = filepath.ToSlash(p)
		}
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return recordVODFailure("操作审计预写失败，已拒绝执行")
	}
	finish := func(result map[string]any, attempted bool) map[string]any {
		out := h.finishAuditedMutation(nodeID, user, action, target, operationID, result, attempted)
		if strings.Contains(asString(out["msg"]), "审计结果写入失败") {
			if attempted {
				return recordVODFailure("上游可能已执行但审计结果写入失败")
			}
			return recordVODFailure("操作未执行且审计结果写入失败")
		}
		return out
	}

	var vals url.Values
	var jsonBody map[string]any
	var vodRel string
	var vodSeek int64
	var vodSpeed float64
	var vodSpeedSet bool
	switch action {
	case "loadMP4File":
		vals, err = validateRecordIdentity(q, false)
		if err == nil {
			var abs, rel string
			abs, rel, err = resolveRecordMP4(n, q.Get("file_path"))
			if err == nil {
				vodRel = rel
				target += " file=" + rel
				repeat, repeatErr := parseRecordInt(q.Get("file_repeat"), "file_repeat", 0, 1, 0)
				seek, seekErr := parseRecordInt(q.Get("seek_ms"), "seek_ms", 0, 86400000, 0)
				speed, speedErr := parseRecordSpeed(q.Get("speed"))
				if repeatErr != nil {
					err = repeatErr
				} else if seekErr != nil {
					err = seekErr
				} else if speedErr != nil {
					err = speedErr
				} else {
					vals.Set("file_path", abs)
					vals.Set("file_repeat", strconv.FormatInt(repeat, 10))
					vodSeek = seek
					vodSpeed = speed
					vodSpeedSet = strings.TrimSpace(q.Get("speed")) != ""
				}
			}
		}
	case "startRecordTask":
		vals, err = validateRecordIdentity(q, false)
		if err == nil {
			var recordPath string
			recordPath, err = validateRecordTaskPath(q.Get("path"))
			back, backErr := parseEventRecordMS(q, "back")
			forward, forwardErr := parseEventRecordMS(q, "forward")
			if err == nil && backErr != nil {
				err = backErr
			}
			if err == nil && forwardErr != nil {
				err = forwardErr
			}
			if err == nil && back == 0 && forward == 0 {
				err = fmt.Errorf("back_ms/forward_ms 至少一个必须大于 0")
			}
			if err == nil {
				vals.Set("path", recordPath)
				vals.Set("back_ms", strconv.FormatInt(back, 10))
				vals.Set("forward_ms", strconv.FormatInt(forward, 10))
			}
		}
	case "startRecord":
		vals, err = validateRecordIdentity(q, false)
		if err == nil {
			var recType string
			recType, err = zlmRecordType(q.Get("kind"), q.Get("type"))
			if err == nil {
				var maxSecond int64
				maxSecond, err = parseRecordInt(q.Get("max_second"), "max_second", 1, 31536000, 600)
				if err == nil {
					vals.Set("type", recType)
					vals.Set("max_second", strconv.FormatInt(maxSecond, 10))
				}
			}
		}
	case "stopRecord":
		vals, err = validateRecordIdentity(q, false)
	case "setRecordSpeed":
		vals, err = validateRecordIdentity(q, true)
		if err == nil {
			var speed float64
			speed, err = parseRecordSpeed(q.Get("speed"))
			if err == nil {
				vals.Set("speed", strconv.FormatFloat(speed, 'g', -1, 64))
			}
		}
	case "seekRecordStamp":
		vals, err = validateRecordIdentity(q, true)
		if err == nil {
			var stamp int64
			stamp, err = parseRecordInt(q.Get("stamp"), "stamp", 0, 86400000, 0)
			if err == nil {
				vals.Set("stamp", strconv.FormatInt(stamp, 10))
			}
		}
	case "pauseStream", "seekStream", "setStreamSpeed":
		vals, err = validateRecordIdentity(q, false)
		if err == nil {
			jsonBody = map[string]any{
				"vhost": vals.Get("vhost"), "app": vals.Get("app"), "stream": vals.Get("stream"),
			}
			if action == "seekStream" {
				var position int64
				position, err = parseRecordInt(q.Get("position"), "position", 0, 86400000, 0)
				jsonBody["position"] = position
			}
			if action == "setStreamSpeed" {
				var speed float64
				speed, err = parseRecordSpeed(q.Get("speed"))
				jsonBody["speed"] = speed
			}
		}
	case "deleteRecordFile":
		var abs string
		abs, err = resolveMediaFile(n, q.Get("file_path"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				err = fmt.Errorf("文件不存在")
			} else if errors.Is(err, os.ErrPermission) {
				err = fmt.Errorf("不允许删除该文件")
			} else {
				err = fmt.Errorf("无法定位要删除的文件")
			}
		} else if removeErr := os.Remove(abs); removeErr != nil {
			err = fmt.Errorf("删除失败")
		}
	default:
		err = fmt.Errorf("unknown operation")
	}
	if err != nil {
		return finish(recordVODFailure(err.Error()), false)
	}
	if action == "deleteRecordFile" {
		return finish(map[string]any{"code": 0, "msg": "已删除录像文件"}, true)
	}

	var result map[string]any
	if action == "startRecord" {
		if vals.Get("type") == "1" {
			result, err = h.zlm.callPOST(n, "setServerConfig", url.Values{
				"protocol.mp4_max_second": {vals.Get("max_second")},
			})
			if err != nil {
				return finish(recordVODFailure("ZLM setServerConfig 调用失败"), true)
			}
		}
		result, err = h.zlm.callPOST(n, action, vals)
	} else if action == "stopRecord" {
		var firstErr error
		for _, recType := range []string{"0", "1"} {
			stopVals := url.Values{
				"type": {recType}, "vhost": {vals.Get("vhost")},
				"app": {vals.Get("app")}, "stream": {vals.Get("stream")},
			}
			current, callErr := h.zlm.callPOST(n, action, stopVals)
			if result == nil && current != nil {
				result = current
			}
			if firstErr == nil && callErr != nil {
				firstErr = callErr
			}
		}
		err = firstErr
	} else if jsonBody != nil {
		result, err = h.zlm.callJSON(n, action, jsonBody)
	} else {
		result, err = h.zlm.callPOST(n, action, vals)
	}
	if err != nil {
		msg := "ZLM " + action + " 调用失败"
		if result != nil {
			if zmsg := strings.TrimSpace(asString(result["msg"])); zmsg != "" {
				msg = zmsg
			}
		} else if errMsg := err.Error(); strings.Contains(errMsg, "zlm code=") {
			msg = errMsg
		}
		failure := recordVODFailure(msg)
		if result != nil {
			failure["zlm_code"] = result["code"]
		}
		return finish(failure, true)
	}
	out := normalizeRecordVODResult(action, result)
	if action == "startRecordTask" && asFloat(out["code"]) == 0 {
		if data, ok := result["data"].(map[string]any); ok {
			if p := strings.TrimSpace(asString(data["path"])); p != "" {
				out["msg"] = "已截录当前流，保存为 " + p + "（写盘后刷新下方列表可见，文件名含 event- 和时间）"
			}
		}
	}
	if action == "loadMP4File" && asFloat(out["code"]) == 0 && vodRel != "" {
		vhost, app, stream := vals.Get("vhost"), vals.Get("app"), vals.Get("stream")
		h.rememberVODLoad(nodeID, vodRel, vhost, app, stream)
		out["vod_app"] = app
		out["vod_stream"] = stream
		if app != "" && stream != "" {
			out["msg"] = "已加载为点播流 " + app + "/" + stream
		}
		if notes := h.applyLoadedMP4Playback(n, vhost, app, stream, vodSeek, vodSpeed, vodSpeedSet); notes != "" {
			out["msg"] = asString(out["msg"]) + "；" + notes
		}
	}
	return finish(out, true)
}

func recordPlaybackSchemas() []string {
	return []string{"rtmp", "fmp4", "ts", "rtsp"}
}

func (h *Hub) applyLoadedMP4Playback(n config.Node, vhost, app, stream string, seek int64, speed float64, speedSet bool) string {
	if h == nil || h.zlm == nil {
		return ""
	}
	var notes []string
	if seek > 0 {
		if err := h.callRecordPlayback(n, "seekRecordStamp", vhost, app, stream, url.Values{
			"stamp": {strconv.FormatInt(seek, 10)},
		}); err != nil {
			notes = append(notes, "起始毫秒未生效")
		}
	}
	if speedSet && speed != 1 {
		if err := h.callRecordPlayback(n, "setRecordSpeed", vhost, app, stream, url.Values{
			"speed": {strconv.FormatFloat(speed, 'g', -1, 64)},
		}); err != nil {
			notes = append(notes, "速度未生效")
		}
	}
	return strings.Join(notes, "，")
}

func (h *Hub) callRecordPlayback(n config.Node, action, vhost, app, stream string, extra url.Values) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		for _, schema := range recordPlaybackSchemas() {
			vals := url.Values{
				"vhost": {vhost}, "app": {app}, "stream": {stream}, "schema": {schema},
			}
			for k, vs := range extra {
				vals[k] = vs
			}
			result, err := h.zlm.callPOST(n, action, vals)
			if err == nil && asFloat(result["result"]) == 0 {
				return nil
			}
			if err != nil {
				last = err
				continue
			}
			last = fmt.Errorf("ZLM %s 未生效", action)
		}
		time.Sleep(80 * time.Millisecond)
	}
	return last
}

type vodLoad struct {
	Vhost  string
	App    string
	Stream string
	File   string
}

type vodLiveStream struct {
	Vhost         string
	App           string
	Stream        string
	OriginURL     string
	OriginType    float64
	OriginTypeStr string
}

func vodFileKey(nodeID, rel string) string {
	return nodeID + "\n" + filepath.ToSlash(strings.TrimSpace(rel))
}

func (h *Hub) rememberVODLoad(nodeID, rel, vhost, app, stream string) {
	if h == nil || strings.TrimSpace(rel) == "" || strings.TrimSpace(app) == "" || strings.TrimSpace(stream) == "" {
		return
	}
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	h.vodMu.Lock()
	if h.vodLoads == nil {
		h.vodLoads = map[string]vodLoad{}
	}
	h.vodLoads[vodFileKey(nodeID, rel)] = vodLoad{Vhost: vhost, App: app, Stream: stream, File: filepath.ToSlash(rel)}
	h.vodMu.Unlock()
}

func (h *Hub) forgetVODLoad(nodeID, rel string) {
	if h == nil {
		return
	}
	h.vodMu.Lock()
	delete(h.vodLoads, vodFileKey(nodeID, rel))
	h.vodMu.Unlock()
}

func (h *Hub) lookupVODLoad(nodeID, rel string) (vodLoad, bool) {
	if h == nil {
		return vodLoad{}, false
	}
	h.vodMu.Lock()
	defer h.vodMu.Unlock()
	load, ok := h.vodLoads[vodFileKey(nodeID, rel)]
	return load, ok
}

func isMP4VODOrigin(originType float64, originTypeStr string) bool {
	if originType == 5 {
		return true
	}
	s := strings.ToLower(originTypeStr)
	return strings.Contains(s, "mp4") || strings.Contains(s, "vod")
}

func vodStreamAlive(lives []vodLiveStream, vhost, app, stream string) bool {
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	for _, live := range lives {
		lv := live.Vhost
		if lv == "" {
			lv = "__defaultVhost__"
		}
		if strings.EqualFold(lv, vhost) && live.App == app && live.Stream == stream {
			return true
		}
	}
	return false
}

func matchVODOrigin(file MediaFile, lives []vodLiveStream) (vodLoad, bool) {
	rel := strings.ToLower(filepath.ToSlash(file.Path))
	name := strings.ToLower(file.Name)
	if rel == "" && name == "" {
		return vodLoad{}, false
	}
	for _, live := range lives {
		if !isMP4VODOrigin(live.OriginType, live.OriginTypeStr) {
			continue
		}
		origin := strings.ToLower(filepath.ToSlash(live.OriginURL))
		if origin == "" {
			continue
		}
		if rel != "" && (strings.HasSuffix(origin, "/"+rel) || strings.HasSuffix(origin, rel)) {
			return vodLoad{Vhost: live.Vhost, App: live.App, Stream: live.Stream, File: file.Path}, true
		}
		if name != "" && strings.HasSuffix(origin, "/"+name) {
			return vodLoad{Vhost: live.Vhost, App: live.App, Stream: live.Stream, File: file.Path}, true
		}
	}
	return vodLoad{}, false
}

func attachVODMarks(h *Hub, n config.Node, host string, files []MediaFile, lives []vodLiveStream) []MediaFile {
	if len(files) == 0 {
		return files
	}
	for i, file := range files {
		load, ok := h.lookupVODLoad(n.ID, file.Path)
		if ok && len(lives) > 0 && !vodStreamAlive(lives, load.Vhost, load.App, load.Stream) {
			h.forgetVODLoad(n.ID, file.Path)
			ok = false
		}
		if !ok {
			load, ok = matchVODOrigin(file, lives)
		}
		if !ok {
			continue
		}
		if load.Vhost == "" {
			load.Vhost = "__defaultVhost__"
		}
		urls := playURLs(host, n, load.Vhost, load.App, load.Stream)
		files[i].VodLoaded = true
		files[i].VodVhost = load.Vhost
		files[i].VodApp = load.App
		files[i].VodStream = load.Stream
		files[i].PlayURL = firstNonEmpty(urls["http-flv"], urls["hls"], urls["rtmp"])
		files[i].PlaySID = n.ID + "|" + load.Vhost + "|" + load.App + "|" + load.Stream
	}
	return files
}
