package service

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"unicode"
)

const (
	AdvancedRestart         = "restartServer"
	AdvancedDeleteRecordDir = "deleteRecordDirectory"
	AdvancedDeleteSnapDir   = "deleteSnapDirectory"
	AdvancedBroadcast       = "broadcastMessage"
)

var advancedBroadcastTemplates = map[string]string{
	"maintenance": "服务即将维护，请稍后重新连接",
	"offline":     "当前流即将下线",
	"notice":      "",
}

var advancedPeriodPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
var advancedMP4NamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.mp4$`)
var advancedSnapNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.jpe?g$`)

func advancedFailure(msg string) map[string]any {
	return map[string]any{"code": -1, "msg": msg}
}

func (h *Hub) AdvancedOperation(nodeID, user, action string, q url.Values) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	if h == nil || h.audit == nil {
		return advancedFailure("操作审计不可用，已拒绝执行")
	}
	if !isAdvancedAction(action) {
		result := advancedFailure("不支持的高级操作")
		h.recordAudit(nodeID, user, action, action, false, asString(result["msg"]))
		return result
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		result := advancedFailure("unknown node")
		h.recordAudit(nodeID, user, action, advancedTarget(action, q), false, asString(result["msg"]))
		return result
	}
	target := advancedTarget(action, q)
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return advancedFailure("操作审计预写失败，已拒绝执行")
	}
	finish := func(result map[string]any, attempted bool) map[string]any {
		out := h.finishAuditedMutation(nodeID, user, action, target, operationID, result, attempted)
		if strings.Contains(asString(out["msg"]), "审计结果写入失败") {
			if attempted {
				return advancedFailure("上游可能已执行但审计结果写入失败")
			}
			return advancedFailure("操作未执行且审计结果写入失败")
		}
		return sanitizeAdvancedResult(out)
	}

	vals, err := validateAdvanced(action, q)
	if err != nil {
		return finish(advancedFailure(err.Error()), false)
	}
	result, err := h.zlm.callPOST(n, action, vals)
	if action == AdvancedDeleteRecordDir {
		result, err = interpretDeleteRecordDirectory(result, err)
	}
	if err != nil {
		msg := err.Error()
		if result != nil && asString(result["msg"]) != "" {
			msg = asString(result["msg"])
		}
		if result == nil {
			result = advancedFailure(msg)
		} else {
			result["code"] = -1
			result["msg"] = msg
		}
		return finish(result, true)
	}
	if result == nil {
		result = map[string]any{"code": 0, "msg": advancedSuccessMessage(action)}
	} else if strings.TrimSpace(asString(result["msg"])) == "" {
		result["msg"] = advancedSuccessMessage(action)
		result["code"] = 0
	} else {
		result["code"] = 0
	}
	return finish(result, true)
}

func isAdvancedAction(action string) bool {
	switch action {
	case AdvancedRestart, AdvancedDeleteRecordDir, AdvancedDeleteSnapDir, AdvancedBroadcast:
		return true
	default:
		return false
	}
}

func advancedSuccessMessage(action string) string {
	switch action {
	case AdvancedRestart:
		return "已请求重启 MediaServer"
	case AdvancedDeleteRecordDir:
		return "已删除受限录像目录"
	case AdvancedDeleteSnapDir:
		return "已删除受限截图目录"
	case AdvancedBroadcast:
		return "已发送模板广播"
	default:
		return "操作成功"
	}
}

func advancedTarget(action string, q url.Values) string {
	switch action {
	case AdvancedRestart:
		return "MediaServer"
	case AdvancedDeleteRecordDir:
		return strings.Join([]string{
			defaultVhost(q.Get("vhost")), q.Get("app"), q.Get("stream"), q.Get("period"), q.Get("name"),
		}, "/")
	case AdvancedDeleteSnapDir:
		return strings.Join([]string{
			defaultVhost(q.Get("vhost")), q.Get("app"), q.Get("stream"), q.Get("file"),
		}, "/")
	case AdvancedBroadcast:
		return q.Get("schema") + "://" + defaultVhost(q.Get("vhost")) + "/" + q.Get("app") + "/" + q.Get("stream")
	default:
		return action
	}
}

func defaultVhost(vhost string) string {
	vhost = strings.TrimSpace(vhost)
	if vhost == "" {
		return "__defaultVhost__"
	}
	return vhost
}

func validateAdvanced(action string, q url.Values) (url.Values, error) {
	switch action {
	case AdvancedRestart:
		return url.Values{}, nil
	case AdvancedDeleteRecordDir:
		out, err := validateRecordIdentity(q, false)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(q.Get("customized_path")) != "" {
			return nil, fmt.Errorf("不允许自定义删除根路径")
		}
		period := strings.TrimSpace(q.Get("period"))
		if !advancedPeriodPattern.MatchString(period) {
			return nil, fmt.Errorf("period 必须是 YYYY-MM-DD")
		}
		out.Set("period", period)
		if name := strings.TrimSpace(q.Get("name")); name != "" {
			if !advancedMP4NamePattern.MatchString(name) || strings.Contains(name, "..") {
				return nil, fmt.Errorf("name 只能是普通 mp4 文件名")
			}
			out.Set("name", name)
		}
		return out, nil
	case AdvancedDeleteSnapDir:
		out, err := validateRecordIdentity(q, false)
		if err != nil {
			return nil, err
		}
		if file := strings.TrimSpace(q.Get("file")); file != "" {
			base := path.Base(strings.ReplaceAll(file, "\\", "/"))
			if base != file || !advancedSnapNamePattern.MatchString(file) || strings.Contains(file, "..") {
				return nil, fmt.Errorf("file 只能是普通 jpg 文件名")
			}
			out.Set("file", file)
		}
		return out, nil
	case AdvancedBroadcast:
		out, err := validateRecordIdentity(q, true)
		if err != nil {
			return nil, err
		}
		msg, err := resolveBroadcastMessage(q)
		if err != nil {
			return nil, err
		}
		out.Set("msg", msg)
		return out, nil
	default:
		return nil, fmt.Errorf("不支持的高级操作")
	}
}

func resolveBroadcastMessage(q url.Values) (string, error) {
	tpl := strings.TrimSpace(q.Get("template"))
	fixed, ok := advancedBroadcastTemplates[tpl]
	if !ok {
		return "", fmt.Errorf("广播模板必须是 maintenance/offline/notice")
	}
	if tpl != "notice" {
		return fixed, nil
	}
	msg := strings.TrimSpace(q.Get("msg"))
	if msg == "" || len([]rune(msg)) > 200 {
		return "", fmt.Errorf("notice 文本长度必须是 1..200")
	}
	if strings.ContainsAny(msg, "<>&") {
		return "", fmt.Errorf("notice 文本不得包含 HTML 标记")
	}
	for _, r := range msg {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("notice 文本含控制字符")
		}
	}
	return msg, nil
}

func interpretDeleteRecordDirectory(result map[string]any, err error) (map[string]any, error) {
	if result == nil {
		return result, err
	}
	if _, isBool := result["code"].(bool); isBool {
		if asTruthy(result["code"]) {
			result["code"] = 0
			return result, nil
		}
		return result, fmt.Errorf("删除录像目录失败")
	}
	if err != nil && asString(result["path"]) != "" && asFloat(result["code"]) == 1 {
		result["code"] = 0
		return result, nil
	}
	return result, err
}

func sanitizeAdvancedResult(result map[string]any) map[string]any {
	if result == nil {
		return advancedFailure("空结果")
	}
	msg := zlmSecretQueryPattern.ReplaceAllString(asString(result["msg"]), "[REDACTED]")
	out := map[string]any{
		"code": result["code"],
		"msg":  msg,
	}
	if asFloat(out["code"]) == 0 && strings.TrimSpace(msg) == "" {
		out["msg"] = "操作成功"
	}
	if raw := strings.TrimSpace(asString(result["path"])); raw != "" {
		out["path"] = redactAbsolutePath(raw)
	}
	return out
}

func redactAbsolutePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/mp4/"); i >= 0 {
		return p[i+1:]
	}
	if i := strings.LastIndex(p, "/snap/"); i >= 0 {
		return p[i+1:]
	}
	return path.Base(strings.TrimRight(p, "/"))
}
