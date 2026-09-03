package service

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

type zlmVersion struct {
	BuildTime  string
	BranchName string
	CommitHash string
}

type versionCacheEntry struct {
	version zlmVersion
	err     string
	expires time.Time
}

func versionFromResponse(v map[string]any) zlmVersion {
	data, _ := v["data"].(map[string]any)
	if data == nil {
		data = v
	}
	return zlmVersion{
		BuildTime:  asString(data["buildTime"]),
		BranchName: asString(data["branchName"]),
		CommitHash: asString(data["commitHash"]),
	}
}

func (h *Hub) cachedVersion(n config.Node) (zlmVersion, error) {
	now := time.Now()
	h.mu.Lock()
	if cached, ok := h.versions[n.ID]; ok && now.Before(cached.expires) {
		h.mu.Unlock()
		if cached.err != "" {
			return cached.version, fmt.Errorf("%s", cached.err)
		}
		return cached.version, nil
	}
	h.mu.Unlock()

	raw, err := h.zlm.call(n, "version", nil)
	info := versionFromResponse(raw)
	entry := versionCacheEntry{version: info, expires: now.Add(5 * time.Minute)}
	if err != nil {
		entry.err = err.Error()
		entry.expires = now.Add(30 * time.Second)
	}
	h.mu.Lock()
	if h.versions == nil {
		h.versions = map[string]versionCacheEntry{}
	}
	h.versions[n.ID] = entry
	h.mu.Unlock()
	return info, err
}

var auditOperationSequence uint64

func (h *Hub) writeAudit(node, user, action, target string, success bool, message string) error {
	return h.writeAuditPhase(node, user, action, target, success, message, "", "")
}

func (h *Hub) writeAuditPhase(node, user, action, target string, success bool, message, operationID, phase string) error {
	if h == nil || h.audit == nil {
		return fmt.Errorf("操作审计不可用")
	}
	if strings.TrimSpace(user) == "" {
		user = "-"
	}
	entry := AuditEntry{
		Node: node, User: user, Action: action, Target: target,
		Success: success, Message: message, Timestamp: time.Now(),
		OperationID: operationID, Phase: phase,
	}
	if err := h.audit.Record(entry); err != nil {
		logger.Warnf("operation audit write failed action=%s node=%s: %v", action, node, err)
		return err
	}
	return nil
}

func (h *Hub) recordAudit(node, user, action, target string, success bool, message string) {
	_ = h.writeAudit(node, user, action, target, success, message)
}

func (h *Hub) beginAuditedMutation(node, user, action, target string) (string, error) {
	operationID := fmt.Sprintf("%x-%x", time.Now().UnixNano(), atomic.AddUint64(&auditOperationSequence, 1))
	if err := h.writeAuditPhase(node, user, action, target, false, "操作意图/开始", operationID, "intent"); err != nil {
		return "", err
	}
	return operationID, nil
}

func (h *Hub) finishAuditedMutation(node, user, action, target, operationID string, result map[string]any, upstreamAttempted bool) map[string]any {
	success := operationSucceeded(result)
	message := operationMessage(result, map[bool]string{true: "操作成功", false: "操作失败"}[success])
	if err := h.writeAuditPhase(node, user, action, target, success, message, operationID, "result"); err != nil {
		if upstreamAttempted {
			return map[string]any{"code": -1, "msg": "上游可能已执行但审计结果写入失败: " + err.Error()}
		}
		return map[string]any{"code": -1, "msg": "操作未执行且审计结果写入失败: " + err.Error()}
	}
	return result
}

func operationMessage(result map[string]any, fallback string) string {
	if result != nil {
		if msg := strings.TrimSpace(asString(result["msg"])); msg != "" {
			return msg
		}
	}
	return fallback
}

func operationSucceeded(result map[string]any) bool {
	return result != nil && asFloat(result["code"]) == 0
}

func operationTarget(action string, q url.Values) string {
	switch action {
	case "close_stream", "close_streams":
		vhost := strings.TrimSpace(q.Get("vhost"))
		if vhost == "" {
			vhost = "__defaultVhost__"
		}
		target := vhost + "/" + strings.TrimSpace(q.Get("app")) + "/" + strings.TrimSpace(q.Get("stream"))
		if schema := strings.TrimSpace(q.Get("schema")); schema != "" {
			target = schema + "://" + target
		}
		return target
	case "kick_session":
		return strings.TrimSpace(q.Get("id"))
	case "kick_sessions":
		return "peer_ip=" + strings.TrimSpace(q.Get("peer_ip")) + " local_port=" + strings.TrimSpace(q.Get("local_port"))
	default:
		return ""
	}
}

// CoreOperation is the single-node boundary for audited administrative mutations.
func (h *Hub) CoreOperation(nodeID, user, action string, q url.Values) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	if action == "kick" {
		action = "kick_session"
	}
	target := operationTarget(action, q)
	if h == nil || h.audit == nil {
		return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		result := map[string]any{"code": -1, "msg": "unknown node"}
		h.recordAudit(nodeID, user, action, target, false, asString(result["msg"]))
		return result
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return map[string]any{"code": -1, "msg": "操作审计预写失败，已拒绝执行: " + err.Error()}
	}

	var result map[string]any
	upstreamAttempted := false
	switch action {
	case "close_stream", "close_streams":
		vhost := strings.TrimSpace(q.Get("vhost"))
		if vhost == "" {
			vhost = "__defaultVhost__"
		}
		app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
		schema := strings.TrimSpace(q.Get("schema"))
		if app == "" || stream == "" || action == "close_stream" && schema == "" {
			result = map[string]any{"code": -1, "msg": "缺少关闭流所需参数"}
			break
		}
		vals := url.Values{
			"vhost": {vhost}, "app": {app}, "stream": {stream}, "force": {"1"},
		}
		if schema != "" {
			vals.Set("schema", schema)
		}
		upstreamAttempted = true
		v, err := h.zlm.callPOST(n, action, vals)
		if err != nil {
			result = zlmCallFailure(v, err)
		} else {
			result = v
			if result == nil {
				result = map[string]any{"code": 0}
			}
			if action == "close_streams" {
				count := int(asFloat(result["count_closed"]))
				if count <= 0 {
					result["code"] = -1
					result["msg"] = "未命中可关闭的流"
				} else {
					result["msg"] = fmt.Sprintf("已关闭 %d 个流实例", count)
				}
			} else if asString(result["msg"]) == "" {
				result["msg"] = "已关闭流"
			}
		}
	case "kick_session":
		upstreamAttempted = strings.TrimSpace(q.Get("id")) != ""
		result = h.kickSession(n, strings.TrimSpace(q.Get("id")))
	case "kick_sessions":
		peerIP := strings.TrimSpace(q.Get("peer_ip"))
		localPort := strings.TrimSpace(q.Get("local_port"))
		if peerIP == "" && localPort == "" {
			result = map[string]any{"code": -1, "msg": "peer_ip 或 local_port 过滤条件至少填写一个，禁止无条件踢出全部会话"}
			break
		}
		if localPort != "" {
			port, err := strconv.ParseUint(localPort, 10, 16)
			if err != nil || port == 0 {
				result = map[string]any{"code": -1, "msg": "local_port 必须是 1..65535 的十进制整数"}
				break
			}
		}
		vals := url.Values{}
		if peerIP != "" {
			vals.Set("peer_ip", peerIP)
		}
		if localPort != "" {
			vals.Set("local_port", localPort)
		}
		upstreamAttempted = true
		v, err := h.zlm.callPOST(n, "kick_sessions", vals)
		if err != nil {
			result = zlmCallFailure(v, err)
		} else {
			result = v
			if result == nil {
				result = map[string]any{"code": 0}
			}
			count := int(asFloat(result["count_hit"]))
			if count <= 0 {
				result["code"] = -1
				result["msg"] = "未命中符合条件的会话"
			} else {
				result["msg"] = fmt.Sprintf("已踢出 %d 个会话", count)
			}
		}
	default:
		result = map[string]any{"code": -1, "msg": "unknown operation"}
	}

	return h.finishAuditedMutation(nodeID, user, action, strings.TrimSpace(target), operationID, result, upstreamAttempted)
}
