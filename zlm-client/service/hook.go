package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

// ZLMediaKit 官方 HTTP Hook 全量（config.ini [hook] on_*）。
var hookEventNames = []string{
	"on_flow_report",
	"on_http_access",
	"on_play",
	"on_publish",
	"on_record_mp4",
	"on_record_ts",
	"on_rtsp_auth",
	"on_rtsp_realm",
	"on_shell_login",
	"on_stream_changed",
	"on_stream_none_reader",
	"on_stream_not_found",
	"on_server_started",
	"on_server_exited",
	"on_server_keepalive",
	"on_send_rtp_stopped",
	"on_rtp_server_timeout",
}

func hookBaseURL() string {
	port := 7788
	if config.C != nil && config.C.Basic.Port > 0 {
		port = config.C.Basic.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d/hook", port)
}

func hookReply(event string) map[string]any {
	resp := map[string]any{"code": 0, "msg": "success"}
	switch event {
	case "on_stream_none_reader":
		resp["close"] = false
	case "on_http_access":
		resp["err"] = ""
		resp["path"] = ""
		resp["second"] = 600
	case "on_rtsp_realm":
		resp["realm"] = ""
	case "on_rtsp_auth":
		resp["encrypted"] = false
		resp["passwd"] = ""
	}
	return resp
}

func hookShouldStore(event string) bool {
	switch event {
	case "on_server_keepalive", "on_http_access":
		return false
	default:
		return true
	}
}

func hookStream(body map[string]any) string {
	app := asString(body["app"])
	stream := asString(body["stream"])
	if stream == "" {
		stream = asString(body["stream_id"])
	}
	if app == "" && stream == "" {
		return "-"
	}
	if app == "" {
		return stream
	}
	return app + "/" + stream
}

func hookBrief(body map[string]any) string {
	if len(body) == 0 {
		return ""
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Sprint(body)
	}
	s := string(b)
	if len(s) > 500 {
		return s[:500] + "..."
	}
	return s
}

func logHook(event string, body map[string]any) {
	stream := hookStream(body)
	schema := asString(body["schema"])
	ip := asString(body["ip"])
	sid := asString(body["id"])
	server := asString(body["mediaServerId"])
	switch event {
	case "on_server_keepalive":
		logger.Debug("hook keepalive server=%s", server)
	case "on_http_access":
		logger.Debug("hook on_http_access ip=%s path=%s", ip, asString(body["path"]))
	case "on_stream_changed":
		if asTruthy(body["regist"]) {
			logger.Infor("hook 流启动 %s schema=%s origin=%s", stream, schema, asString(body["originTypeStr"]))
		} else {
			logger.Warnf("hook 流停止 %s schema=%s", stream, schema)
		}
	case "on_publish":
		logger.Infor("hook 推流开始 %s schema=%s ip=%s:%v id=%s origin=%s",
			stream, schema, ip, body["port"], sid, asString(body["originTypeStr"]))
	case "on_play":
		logger.Infor("hook 播放开始 %s schema=%s ip=%s:%v id=%s", stream, schema, ip, body["port"], sid)
	case "on_flow_report":
		logger.Infor("hook 会话结束 %s schema=%s ip=%s id=%s player=%v bytes=%v dur=%vs",
			stream, schema, ip, sid, body["player"], body["totalBytes"], body["duration"])
	case "on_stream_not_found":
		logger.Warnf("hook 流不存在 %s schema=%s ip=%s:%v", stream, schema, ip, body["port"])
	case "on_stream_none_reader":
		logger.Infor("hook 无人观看 %s schema=%s 保持不关流", stream, schema)
	case "on_send_rtp_stopped":
		logger.Error("hook 发送RTP停止 %s ssrc=%v err=%v msg=%s", stream, body["ssrc"], body["err"], asString(body["msg"]))
	case "on_rtp_server_timeout":
		logger.Error("hook RTP收流超时 %s port=%v ssrc=%v", stream, body["local_port"], body["ssrc"])
	case "on_server_started":
		logger.Warnf("hook ZLM启动 server=%s", server)
	case "on_server_exited":
		logger.Error("hook ZLM退出 server=%s", server)
	case "on_record_mp4", "on_record_ts":
		logger.Infor("hook %s %s file=%s size=%v url=%s", event, stream, asString(body["file_name"]), body["file_size"], asString(body["url"]))
	default:
		logger.Infor("hook %s %s %s", event, stream, hookBrief(body))
	}
}

func ourHookURL(cur, want string) bool {
	cur = strings.TrimSpace(cur)
	if cur == "" {
		return true
	}
	return strings.Contains(cur, "/hook/")
}

func (h *Hub) ensureAllHooks() {
	if h == nil || config.C == nil {
		return
	}
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	for _, n := range nodes {
		h.ensureNodeHooks(n)
	}
}

func (h *Hub) ensureNodeHooks(n config.Node) {
	base := hookBaseURL()
	cur := map[string]string{}
	if v, err := h.zlm.call(n, "getServerConfig", nil); err == nil {
		cur = zlmFlatConfig(v)
	} else {
		logger.Warnf("ensure hooks 读配置失败 node=%s: %v", n.ID, err)
	}
	kv := map[string]string{"hook.enable": "1"}
	for _, name := range hookEventNames {
		want := base + "/" + name
		key := "hook." + name
		if ourHookURL(cur[key], want) && strings.TrimSpace(cur[key]) != want {
			kv[key] = want
		}
	}
	if n, _ := strconv.Atoi(strings.TrimSpace(cur["rtp_proxy.gop_cache"])); cur["rtp_proxy.gop_cache"] != "" && n < 15 {
		kv["rtp_proxy.gop_cache"] = "30"
	}
	if _, ok := cur["protocol.gop_cache"]; ok {
		if n, _ := strconv.Atoi(strings.TrimSpace(cur["protocol.gop_cache"])); n < 15 {
			kv["protocol.gop_cache"] = "30"
		}
	}
	if len(kv) == 1 && cur["hook.enable"] == "1" {
		logger.Infor("ensure hooks node=%s already complete", n.ID)
		return
	}
	raw, _ := json.Marshal(kv)
	out := h.setServerConfig(n, raw)
	logger.Infor("ensure hooks node=%s enable=1 urls=%d code=%v msg=%v", n.ID, len(kv)-1, out["code"], out["msg"])
}

func zlmFlatConfig(v map[string]any) map[string]string {
	out := map[string]string{}
	var flat map[string]any
	switch data := v["data"].(type) {
	case []any:
		if len(data) > 0 {
			if m, ok := data[0].(map[string]any); ok {
				flat = m
			}
		}
	case map[string]any:
		flat = data
	}
	for k, val := range flat {
		out[k] = fmt.Sprint(val)
	}
	return out
}
