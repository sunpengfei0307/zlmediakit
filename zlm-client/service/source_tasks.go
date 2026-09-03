package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"zlm-admin/core/config"
)

const (
	SourceTaskPullAdd      = "addStreamProxy"
	SourceTaskPullDelete   = "delStreamProxy"
	SourceTaskPusherAdd    = "addStreamPusherProxy"
	SourceTaskPusherDelete = "delStreamPusherProxy"
	SourceTaskFFmpegAdd    = "addFFmpegSource"
	SourceTaskFFmpegDelete = "delFFmpegSource"
)

type SourceTaskSection struct {
	Items []map[string]any
	Error string
}

type SourceTasksView struct {
	Pull, Pusher, FFmpeg SourceTaskSection
}

var pullSourceSchemes = map[string]bool{
	"rtsp": true, "rtsps": true, "rtmp": true, "rtmps": true, "http": true,
	"https": true, "srt": true, "webrtc": true, "webrtcs": true,
}
var pusherTargetSchemes = map[string]bool{
	"rtmp": true, "rtmps": true, "rtsp": true, "rtsps": true,
	"srt": true, "webrtc": true, "webrtcs": true,
}
var ffmpegTargetSchemes = map[string]bool{"rtmp": true, "rtsp": true, "srt": true}

func (h *Hub) ListSourceTasks(nodeID string) SourceTasksView {
	empty := SourceTaskSection{Items: []map[string]any{}}
	out := SourceTasksView{Pull: empty, Pusher: empty, FFmpeg: empty}
	if h == nil || h.zlm == nil {
		out.Pull.Error, out.Pusher.Error, out.FFmpeg.Error = "ZLM 客户端不可用", "ZLM 客户端不可用", "ZLM 客户端不可用"
		return out
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		out.Pull.Error, out.Pusher.Error, out.FFmpeg.Error = "unknown node", "unknown node", "unknown node"
		return out
	}
	out.Pull = h.listSourceSection(n, "listStreamProxy", "getProxyInfo")
	out.Pusher = h.listSourceSection(n, "listStreamPusherProxy", "getProxyPusherInfo")
	out.FFmpeg = h.listSourceSection(n, "listFFmpegSource", "")
	return out
}

func (h *Hub) listSourceSection(n config.Node, listAPI, infoAPI string) SourceTaskSection {
	raw, err := h.zlm.call(n, listAPI, nil)
	if err != nil {
		return SourceTaskSection{Items: []map[string]any{}, Error: err.Error()}
	}
	rows := sourceTaskRows(raw["data"])
	for i := range rows {
		normalizeSourceTaskRow(rows[i], listAPI)
		key := strings.TrimSpace(asString(rows[i]["key"]))
		if infoAPI == "" || key == "" {
			continue
		}
		info, infoErr := h.zlm.call(n, infoAPI, url.Values{"key": {key}})
		if infoErr != nil {
			rows[i]["_error"] = infoErr.Error()
			continue
		}
		detail := sourceTaskObject(info["data"])
		if detail == nil {
			detail = sourceTaskObject(info)
		}
		for k, v := range detail {
			if k != "code" && k != "msg" {
				rows[i][k] = v
			}
		}
		normalizeSourceTaskRow(rows[i], listAPI)
	}
	return SourceTaskSection{Items: rows}
}

func normalizeSourceTaskRow(row map[string]any, listAPI string) {
	if src, ok := row["src"].(map[string]any); ok {
		for _, key := range []string{"schema", "vhost", "app", "stream"} {
			if value, exists := src[key]; exists {
				row[key] = value
			}
		}
	}
	if listAPI == "listStreamPusherProxy" {
		if strings.TrimSpace(asString(row["schema"])) == "" {
			if schema, _, ok := strings.Cut(asString(row["key"]), "/"); ok {
				row["schema"] = schema
			}
		}
		if dst := asString(row["url"]); dst != "" {
			row["dst_url"] = dst
		}
	}
}

func sourceTaskRows(v any) []map[string]any {
	if rows := asSlice(v); rows != nil {
		return rows
	}
	m, ok := v.(map[string]any)
	if !ok {
		return []map[string]any{}
	}
	if asString(m["key"]) != "" {
		return []map[string]any{sourceTaskObject(m)}
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		row := sourceTaskObject(m[key])
		if row == nil {
			row = map[string]any{}
		}
		if asString(row["key"]) == "" {
			row["key"] = key
		}
		rows = append(rows, row)
	}
	return rows
}

func sourceTaskObject(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, value := range m {
		out[k] = value
	}
	return out
}

func (h *Hub) SourceTaskOperation(nodeID, user, action string, q url.Values, localAdmin bool) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	target := sourceTaskAuditTarget(action, q)
	if h == nil || h.audit == nil {
		return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		result := map[string]any{"code": -1, "msg": "unknown node"}
		h.recordAudit(nodeID, user, action, target, false, asString(result["msg"]))
		return result
	}
	vals, err := validateSourceTaskOperation(action, q, localAdmin)
	if err != nil {
		result := map[string]any{"code": -1, "msg": err.Error()}
		return h.recordRejectedMutation(nodeID, user, action, target, result)
	}
	viaFFmpeg := false
	if action == SourceTaskPullAdd && pullNeedsFFmpeg(vals.Get("url")) {
		action = SourceTaskFFmpegAdd
		vals, err = pullAsFFmpegSource(n, vals)
		if err != nil {
			result := map[string]any{"code": -1, "msg": err.Error()}
			return h.recordRejectedMutation(nodeID, user, action, target, result)
		}
		viaFFmpeg = true
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return map[string]any{"code": -1, "msg": "操作审计预写失败，已拒绝执行: " + err.Error()}
	}
	h.sourceTaskMu.Lock()
	defer h.sourceTaskMu.Unlock()
	existing := map[string]bool{}
	listAPI := sourceTaskListAPI(action)
	if listAPI != "" {
		listed, listErr := h.zlm.call(n, listAPI, nil)
		if listErr != nil {
			result := map[string]any{"code": -1, "msg": "读取现有任务列表失败，已拒绝新增: " + listErr.Error()}
			return h.finishAuditedMutation(nodeID, user, action, target, operationID, result, false)
		}
		for _, row := range sourceTaskRows(listed["data"]) {
			if key := strings.TrimSpace(asString(row["key"])); key != "" {
				existing[key] = true
			}
		}
	}
	result, callErr := h.zlm.callPOSTWithTimeout(context.Background(), n, action, vals, sourceTaskWriteTimeout(action, vals))
	if callErr != nil {
		if recovered := h.recoverSourceTaskAdd(n, action, listAPI, vals, existing, callErr); recovered != nil {
			result, callErr = recovered, nil
		} else {
			result = zlmCallFailure(result, callErr)
		}
	}
	if result == nil {
		result = map[string]any{"code": -1, "msg": "ZLM 返回空结果"}
	}
	if callErr == nil && operationSucceeded(result) {
		switch action {
		case SourceTaskPullAdd, SourceTaskPusherAdd, SourceTaskFFmpegAdd:
			key := sourceTaskResultKey(result)
			if key == "" {
				result["code"], result["msg"] = -1, "ZLM 未返回任务 key，操作结果无效"
			} else if existing[key] {
				result["code"], result["msg"] = -1, "任务已存在，未新增"
			}
		case SourceTaskPullDelete, SourceTaskPusherDelete, SourceTaskFFmpegDelete:
			if !sourceTaskDeleteFlag(result) {
				result["code"], result["msg"] = -1, "ZLM 未删除任务（flag=false）"
			}
		}
	}
	success := operationSucceeded(result)
	if viaFFmpeg && success {
		result["msg"] = "ZLM 拉流代理不支持该 HTTP 地址，已改用 FFmpeg 拉到 " + vals.Get("dst_url")
	}
	if asString(result["msg"]) == "" {
		if success {
			result["msg"] = "操作成功"
		} else {
			result["msg"] = "操作失败"
		}
	}
	auditMessage := redactSourceAuditMessage(asString(result["msg"]), q)
	auditResult := map[string]any{}
	for key, value := range result {
		auditResult[key] = value
	}
	auditResult["msg"] = auditMessage
	final := h.finishAuditedMutation(nodeID, user, action, target, operationID, auditResult, true)
	if asString(final["msg"]) != auditMessage {
		return final
	}
	return result
}

func (h *Hub) recordRejectedMutation(nodeID, user, action, target string, result map[string]any) map[string]any {
	if err := h.writeAudit(nodeID, user, action, target, false, operationMessage(result, "操作失败")); err != nil {
		return map[string]any{"code": -1, "msg": "操作未执行且审计写入失败: " + err.Error()}
	}
	return result
}

func sourceTaskWriteTimeout(action string, vals url.Values) time.Duration {
	switch action {
	case SourceTaskFFmpegAdd:
		ms, _ := strconv.Atoi(strings.TrimSpace(vals.Get("timeout_ms")))
		if ms < 1000 {
			ms = 10000
		}
		d := time.Duration(ms)*time.Millisecond + 8*time.Second
		if d > 90*time.Second {
			return 90 * time.Second
		}
		return d
	case SourceTaskPullAdd, SourceTaskPusherAdd:
		sec, _ := strconv.Atoi(strings.TrimSpace(vals.Get("timeout_sec")))
		if sec < 1 {
			sec = 10
		}
		d := time.Duration(sec)*time.Second + 8*time.Second
		if d > 60*time.Second {
			return 60 * time.Second
		}
		return d
	default:
		return 15 * time.Second
	}
}

func isZLMTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "deadline exceeded")
}

func sourceTaskRowMatches(row map[string]any, action string, vals url.Values) bool {
	switch action {
	case SourceTaskFFmpegAdd:
		src, dst := vals.Get("src_url"), vals.Get("dst_url")
		return (src != "" && asString(row["src_url"]) == src) || (dst != "" && asString(row["dst_url"]) == dst)
	case SourceTaskPullAdd:
		return asString(row["url"]) == vals.Get("url") && asString(row["stream"]) == vals.Get("stream")
	case SourceTaskPusherAdd:
		dst := vals.Get("dst_url")
		return dst != "" && (asString(row["dst_url"]) == dst || asString(row["url"]) == dst)
	default:
		return false
	}
}

func (h *Hub) recoverSourceTaskAdd(n config.Node, action, listAPI string, vals url.Values, existing map[string]bool, callErr error) map[string]any {
	if h == nil || h.zlm == nil || listAPI == "" || !isZLMTimeoutErr(callErr) {
		return nil
	}
	switch action {
	case SourceTaskPullAdd, SourceTaskPusherAdd, SourceTaskFFmpegAdd:
	default:
		return nil
	}
	listed, err := h.zlm.call(n, listAPI, nil)
	if err != nil {
		return nil
	}
	var matched map[string]any
	newRows := make([]map[string]any, 0, 1)
	for _, row := range sourceTaskRows(listed["data"]) {
		key := strings.TrimSpace(asString(row["key"]))
		if key == "" || existing[key] {
			continue
		}
		newRows = append(newRows, row)
		if sourceTaskRowMatches(row, action, vals) {
			matched = row
			break
		}
	}
	if matched == nil {
		if len(newRows) != 1 {
			return nil
		}
		matched = newRows[0]
	}
	key := strings.TrimSpace(asString(matched["key"]))
	if key == "" {
		return nil
	}
	return map[string]any{
		"code": 0, "key": key, "data": matched,
		"msg": "已添加（接口超时，已从列表确认）",
	}
}

func sourceTaskListAPI(action string) string {
	switch action {
	case SourceTaskPullAdd:
		return "listStreamProxy"
	case SourceTaskPusherAdd:
		return "listStreamPusherProxy"
	case SourceTaskFFmpegAdd:
		return "listFFmpegSource"
	default:
		return ""
	}
}

func sourceTaskDeleteFlag(result map[string]any) bool {
	if data, ok := result["data"].(map[string]any); ok {
		if _, exists := data["flag"]; exists {
			return asTruthy(data["flag"])
		}
	}
	return asTruthy(result["flag"])
}

func validateSourceTaskOperation(action string, q url.Values, localAdmin bool) (url.Values, error) {
	switch action {
	case SourceTaskPullAdd:
		return validatePullSource(q)
	case SourceTaskPusherAdd:
		return validatePusherSource(q)
	case SourceTaskFFmpegAdd:
		return validateFFmpegSource(q, localAdmin)
	case SourceTaskPullDelete, SourceTaskPusherDelete, SourceTaskFFmpegDelete:
		rawKey := q.Get("key")
		if err := validateSourceTaskKey(rawKey); err != nil {
			return nil, err
		}
		key := strings.TrimSpace(rawKey)
		return url.Values{"key": {key}}, nil
	default:
		return nil, fmt.Errorf("unknown source task operation")
	}
}

func validateSourceTaskKey(key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("key 必填")
	}
	if len(key) > 1024 {
		return fmt.Errorf("key 过长")
	}
	if strings.Contains(key, `\`) {
		return fmt.Errorf("key 含非法反斜杠")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return fmt.Errorf("key 含控制字符")
		}
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == ".." {
			return fmt.Errorf("key 含非法路径穿越段")
		}
	}
	return nil
}

func validatePullSource(q url.Values) (url.Values, error) {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
	for name, value := range map[string]string{"vhost": vhost, "app": app, "stream": stream} {
		if err := validateSourceName(name, value, true); err != nil {
			return nil, err
		}
	}
	rawURL := strings.TrimSpace(q.Get("url"))
	if _, err := validateAbsoluteURL("url", rawURL, pullSourceSchemes, false); err != nil {
		return nil, err
	}
	out := url.Values{"vhost": {vhost}, "app": {app}, "stream": {stream}, "url": {rawURL}}
	if err := copyBoundedInt(out, q, "retry_count", -1, 100); err != nil {
		return nil, err
	}
	if err := copyBoundedInt(out, q, "timeout_sec", 1, 120); err != nil {
		return nil, err
	}
	if err := copyEnum(out, q, "force", "0", "1"); err != nil {
		return nil, err
	}
	if err := copyEnum(out, q, "rtp_type", "0", "1", "2"); err != nil {
		return nil, err
	}
	for _, key := range []string{"enable_audio", "add_mute_audio", "auto_close", "enable_hls", "enable_hls_fmp4", "enable_mp4", "mp4_as_player", "hls_demand", "rtsp_demand", "rtmp_demand", "ts_demand", "fmp4_demand"} {
		if err := copyEnum(out, q, key, "0", "1"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func validatePusherSource(q url.Values) (url.Values, error) {
	schema := strings.ToLower(strings.TrimSpace(q.Get("schema")))
	if !map[string]bool{"rtmp": true, "rtsp": true, "srt": true, "webrtc": true}[schema] {
		return nil, fmt.Errorf("schema 仅允许 rtmp、rtsp、srt、webrtc")
	}
	vhost := strings.TrimSpace(q.Get("vhost"))
	app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
	for name, value := range map[string]string{"schema": schema, "vhost": vhost, "app": app, "stream": stream} {
		if err := validateSourceName(name, value, true); err != nil {
			return nil, err
		}
	}
	dst := strings.TrimSpace(q.Get("dst_url"))
	if _, err := validateAbsoluteURL("dst_url", dst, pusherTargetSchemes, false); err != nil {
		return nil, err
	}
	out := url.Values{"schema": {schema}, "vhost": {vhost}, "app": {app}, "stream": {stream}, "dst_url": {dst}}
	if err := copyBoundedInt(out, q, "retry_count", -1, 100); err != nil {
		return nil, err
	}
	if err := copyEnum(out, q, "rtp_type", "0", "1", "2"); err != nil {
		return nil, err
	}
	if err := copyBoundedInt(out, q, "timeout_sec", 1, 120); err != nil {
		return nil, err
	}
	return out, nil
}

func validateFFmpegSource(q url.Values, localAdmin bool) (url.Values, error) {
	src, dst := strings.TrimSpace(q.Get("src_url")), strings.TrimSpace(q.Get("dst_url"))
	allowedSrc := make(map[string]bool, len(pullSourceSchemes)+1)
	for k, v := range pullSourceSchemes {
		allowedSrc[k] = v
	}
	allowedSrc["file"] = true
	srcURL, err := validateAbsoluteURL("src_url", src, allowedSrc, true)
	if err != nil {
		return nil, err
	}
	if srcURL.Scheme == "file" {
		if !localAdmin {
			return nil, fmt.Errorf("file 源仅允许本机管理员操作")
		}
		if srcURL.Host != "" && !strings.EqualFold(srcURL.Host, "localhost") {
			return nil, fmt.Errorf("file 源必须是本机绝对路径")
		}
		if !isAbsoluteLocalPath(filepath.FromSlash(srcURL.Path)) {
			return nil, fmt.Errorf("file 源必须是绝对路径")
		}
	}
	if _, err := validateAbsoluteURL("dst_url", dst, ffmpegTargetSchemes, false); err != nil {
		return nil, err
	}
	out := url.Values{"src_url": {src}, "dst_url": {dst}}
	if strings.TrimSpace(q.Get("timeout_ms")) == "" {
		return nil, fmt.Errorf("timeout_ms 必填")
	}
	if err := copyBoundedInt(out, q, "timeout_ms", 1000, 120000); err != nil {
		return nil, err
	}
	for _, key := range []string{"enable_hls", "enable_mp4"} {
		if err := copyEnum(out, q, key, "0", "1"); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func validateSourceName(name, value string, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("%s 必填", name)
		}
		return nil
	}
	if len(value) > 255 || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("%s 含非法路径字符", name)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s 含控制字符", name)
		}
	}
	return nil
}

func pullNeedsFFmpeg(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return !zlmPullHTTPNative(u)
	default:
		return false
	}
}

func zlmPullHTTPNative(u *url.URL) bool {
	if u == nil {
		return false
	}
	path := strings.ToLower(u.EscapedPath())
	return strings.HasSuffix(path, ".m3u8") || strings.HasSuffix(path, ".ts") || strings.HasSuffix(path, ".flv")
}

func pullAsFFmpegSource(n config.Node, pull url.Values) (url.Values, error) {
	app, stream := strings.TrimSpace(pull.Get("app")), strings.TrimSpace(pull.Get("stream"))
	src := strings.TrimSpace(pull.Get("url"))
	if app == "" || stream == "" || src == "" {
		return nil, fmt.Errorf("改用 FFmpeg 拉流时 app、stream、url 必填")
	}
	timeoutMS := 10000
	if sec := strings.TrimSpace(pull.Get("timeout_sec")); sec != "" {
		if parsed, err := strconv.Atoi(sec); err == nil && parsed >= 1 {
			timeoutMS = parsed * 1000
		}
	}
	if timeoutMS < 1000 {
		timeoutMS = 1000
	}
	if timeoutMS > 120000 {
		timeoutMS = 120000
	}
	return url.Values{
		"src_url": {src}, "dst_url": {localRTMPURL(n, app, stream)},
		"timeout_ms": {strconv.Itoa(timeoutMS)}, "enable_hls": {"0"}, "enable_mp4": {"0"},
	}, nil
}

func localRTMPURL(n config.Node, app, stream string) string {
	return fmt.Sprintf("rtmp://127.0.0.1:%d/%s/%s", nz(n.RTMPPort, 1935), app, stream)
}

func validateAbsoluteURL(name, raw string, allowed map[string]bool, allowFile bool) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s 必填", name)
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() {
		return nil, fmt.Errorf("%s 必须是绝对 URL", name)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if !allowed[u.Scheme] {
		return nil, fmt.Errorf("%s scheme 不允许", name)
	}
	if u.Scheme == "file" {
		if !allowFile || u.Path == "" {
			return nil, fmt.Errorf("%s file 路径无效", name)
		}
	} else if u.Host == "" {
		return nil, fmt.Errorf("%s 必须包含主机", name)
	}
	return u, nil
}

func copyBoundedInt(dst, src url.Values, key string, minValue, maxValue int) error {
	raw := strings.TrimSpace(src.Get(key))
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return fmt.Errorf("%s 必须是 %d..%d 的整数", key, minValue, maxValue)
	}
	dst.Set(key, strconv.Itoa(value))
	return nil
}

func copyEnum(dst, src url.Values, key string, allowed ...string) error {
	raw := strings.TrimSpace(src.Get(key))
	if raw == "" {
		return nil
	}
	for _, value := range allowed {
		if raw == value {
			dst.Set(key, raw)
			return nil
		}
	}
	return fmt.Errorf("%s 仅允许 %s", key, strings.Join(allowed, "/"))
}

func sourceTaskResultKey(result map[string]any) string {
	if key := strings.TrimSpace(asString(result["key"])); key != "" {
		return key
	}
	if data, ok := result["data"].(map[string]any); ok {
		return strings.TrimSpace(asString(data["key"]))
	}
	return ""
}

func sourceTaskAuditTarget(action string, q url.Values) string {
	switch action {
	case SourceTaskPullAdd:
		return sourceTaskStreamTarget(q) + " <- " + redactSourceURL(q.Get("url"))
	case SourceTaskPusherAdd:
		return strings.TrimSpace(q.Get("schema")) + "://" + sourceTaskStreamTarget(q) + " -> " + redactSourceURL(q.Get("dst_url"))
	case SourceTaskFFmpegAdd:
		return redactSourceURL(q.Get("src_url")) + " -> " + redactSourceURL(q.Get("dst_url"))
	case SourceTaskPullDelete, SourceTaskPusherDelete, SourceTaskFFmpegDelete:
		return strings.TrimSpace(q.Get("key"))
	default:
		return ""
	}
}

func sourceTaskStreamTarget(q url.Values) string {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	return vhost + "/" + strings.TrimSpace(q.Get("app")) + "/" + strings.TrimSpace(q.Get("stream"))
}

func redactSourceURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "[invalid-url]"
	}
	u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
	return u.String()
}

var sourceURLUserinfoPattern = regexp.MustCompile(`(?i)[a-z0-9._~%-]+:[^@\s/]+@`)

func redactSourceAuditMessage(message string, q url.Values) string {
	for _, key := range []string{"url", "src_url", "dst_url"} {
		raw := strings.TrimSpace(q.Get(key))
		if raw != "" {
			message = strings.ReplaceAll(message, raw, redactSourceURL(raw))
			if parsed, err := url.Parse(raw); err == nil && parsed.User != nil {
				if password, ok := parsed.User.Password(); ok && password != "" {
					message = strings.ReplaceAll(message, password, "[REDACTED]")
				}
				message = redactStandaloneSourceUsername(message, parsed.User.Username())
			}
		}
	}
	return sourceURLUserinfoPattern.ReplaceAllString(message, "[REDACTED]@")
}

func redactStandaloneSourceUsername(message, username string) string {
	if len(username) < 3 || strings.TrimSpace(username) != username ||
		strings.IndexFunc(username, unicode.IsControl) >= 0 {
		return message
	}
	pattern := regexp.MustCompile(`(^|[^A-Za-z0-9._~%-])` + regexp.QuoteMeta(username) + `([^A-Za-z0-9._~%-]|$)`)
	return pattern.ReplaceAllString(message, `${1}[REDACTED]${2}`)
}

func isAbsoluteLocalPath(path string) bool {
	if filepath.IsAbs(path) || strings.HasPrefix(filepath.ToSlash(path), "/") {
		return true
	}
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) &&
		path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}
