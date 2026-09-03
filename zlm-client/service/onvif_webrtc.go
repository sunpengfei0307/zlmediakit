package service

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	WebRTCRoomKeeperAdd    = "addWebrtcRoomKeeper"
	WebRTCRoomKeeperDelete = "delWebrtcRoomKeeper"
)

type OnvifWebRTCSection struct {
	Items []map[string]any
	Error string
}

type OnvifWebRTCDetail struct {
	Item  map[string]any
	Error string
}

type OnvifWebRTCView struct {
	Rooms, Keepers OnvifWebRTCSection
	Player         OnvifWebRTCDetail
}

var unexpectedUserinfoPattern = regexp.MustCompile(`(?i)[a-z0-9._~%-]+:[^@\s/]+@`)

func sanitizeOnvifWebRTCText(message string) string {
	return unexpectedUserinfoPattern.ReplaceAllString(message, "[REDACTED]@")
}

func safeOnvifWebRTCError(err error) string {
	if err == nil {
		return ""
	}
	return sanitizeOnvifWebRTCText(err.Error())
}

func cleanOnvifWebRTCItems(raw any, identity string) []map[string]any {
	rows := asSlice(raw)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		value := strings.TrimSpace(asString(row[identity]))
		if validateOnvifWebRTCKey(identity, value) != nil {
			continue
		}
		copy := make(map[string]any, len(row))
		for key, item := range row {
			copy[key] = item
		}
		out = append(out, copy)
	}
	return out
}

func (h *Hub) ListOnvifWebRTC(nodeID, playerKey string) OnvifWebRTCView {
	empty := OnvifWebRTCSection{Items: []map[string]any{}}
	view := OnvifWebRTCView{Rooms: empty, Keepers: empty}
	if h == nil || h.zlm == nil {
		view.Rooms.Error, view.Keepers.Error = "ZLM 客户端不可用", "ZLM 客户端不可用"
		return view
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		view.Rooms.Error, view.Keepers.Error = "unknown node", "unknown node"
		return view
	}
	if raw, err := h.zlm.call(n, "listWebrtcRooms", nil); err != nil {
		view.Rooms.Error = safeOnvifWebRTCError(err)
	} else {
		view.Rooms.Items = cleanOnvifWebRTCItems(raw["data"], "room_id")
	}
	if raw, err := h.zlm.call(n, "listWebrtcRoomKeepers", nil); err != nil {
		view.Keepers.Error = safeOnvifWebRTCError(err)
	} else {
		view.Keepers.Items = cleanOnvifWebRTCItems(raw["data"], "room_key")
	}
	playerKey = strings.TrimSpace(playerKey)
	if playerKey == "" {
		return view
	}
	if err := validateOnvifWebRTCKey("key", playerKey); err != nil {
		view.Player.Error = err.Error()
		return view
	}
	raw, err := h.zlm.call(n, "getWebrtcProxyPlayerInfo", url.Values{"key": {playerKey}})
	if err != nil {
		view.Player.Error = safeOnvifWebRTCError(err)
		return view
	}
	item, _ := raw["data"].(map[string]any)
	if item == nil {
		view.Player.Error = "WebRTC Proxy Player 响应缺少 data"
		return view
	}
	view.Player.Item = item
	return view
}

func (h *Hub) SearchOnvifDevices(nodeID, user string, q url.Values) map[string]any {
	const action = "searchOnvifDevice"
	target := "timeout_ms=" + strings.TrimSpace(q.Get("timeout_ms"))
	if subnet := strings.TrimSpace(q.Get("subnet_prefix")); subnet != "" {
		target += " subnet_prefix=" + subnet
	}
	if h == nil || h.audit == nil {
		return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": "unknown node"})
	}
	vals, err := validateOnvifSearch(q)
	if err != nil {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": err.Error()})
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return map[string]any{"code": -1, "msg": "操作审计预写失败，已拒绝执行: " + safeOnvifWebRTCError(err)}
	}
	timeoutMS, _ := strconv.Atoi(vals.Get("timeout_ms"))
	result, callErr := h.zlm.callPOSTWithTimeout(
		context.Background(), n, action, vals,
		time.Duration(timeoutMS)*time.Millisecond+1500*time.Millisecond,
	)
	if callErr != nil {
		result = map[string]any{"code": -1, "msg": safeOnvifWebRTCError(callErr)}
	}
	if result == nil {
		result = map[string]any{"code": -1, "msg": "ZLM 返回空结果"}
	}
	if operationSucceeded(result) {
		result["devices"] = cleanOnvifDevices(result["data"])
		if asString(result["msg"]) == "" {
			result["msg"] = "发现完成"
		}
	} else {
		result["msg"] = sanitizeOnvifWebRTCText(asString(result["msg"]))
	}
	return h.finishAuditedMutation(nodeID, user, action, target, operationID, result, true)
}

func (h *Hub) ImportOnvifPull(nodeID, user string, q url.Values, localAdmin bool) map[string]any {
	rawURL := strings.TrimSpace(q.Get("url"))
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "rtsp" && parsed.Scheme != "rtsps") {
		result := map[string]any{"code": -1, "msg": "url 仅允许已验证的 rtsp/rtsps URL"}
		if h == nil || h.audit == nil {
			return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
		}
		return h.recordRejectedMutation(nodeID, user, SourceTaskPullAdd, sourceTaskAuditTarget(SourceTaskPullAdd, q), result)
	}
	result := h.SourceTaskOperation(nodeID, user, SourceTaskPullAdd, q, localAdmin)
	if result == nil {
		return map[string]any{"code": -1, "msg": "拉流代理返回空结果"}
	}
	safe, _ := sanitizeImportValue(result, q).(map[string]any)
	out := map[string]any{
		"code": result["code"],
		"msg":  redactSourceAuditMessage(asString(safe["msg"]), q),
	}
	if key := sourceTaskResultKey(result); key != "" {
		out["key"] = key
	}
	return out
}

func sanitizeImportValue(value any, q url.Values) any {
	redactText := func(text string) string {
		text = redactSourceAuditMessage(text, q)
		text = sanitizeOnvifWebRTCText(text)
		if parsed, err := url.Parse(strings.TrimSpace(q.Get("url"))); err == nil && parsed.User != nil {
			if password, ok := parsed.User.Password(); ok && password != "" {
				text = strings.ReplaceAll(text, password, "[REDACTED]")
			}
			text = redactStandaloneSourceUsername(text, parsed.User.Username())
		}
		return text
	}
	switch typed := value.(type) {
	case string:
		return redactText(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "password") || strings.Contains(lower, "passwd") ||
				strings.Contains(lower, "credential") {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = sanitizeImportValue(item, q)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = sanitizeImportValue(item, q)
		}
		return out
	default:
		return value
	}
}

func validateOnvifSearch(q url.Values) (url.Values, error) {
	timeout, err := strconv.Atoi(strings.TrimSpace(q.Get("timeout_ms")))
	if err != nil || timeout < 500 || timeout > 10000 {
		return nil, fmt.Errorf("timeout_ms 必须是 500..10000 的整数")
	}
	out := url.Values{"timeout_ms": {strconv.Itoa(timeout)}}
	rawSubnet := q.Get("subnet_prefix")
	if hasControl(rawSubnet) {
		return nil, fmt.Errorf("subnet_prefix 必须是 IPv4 地址或 CIDR")
	}
	subnet := strings.TrimSpace(rawSubnet)
	if subnet == "" {
		return out, nil
	}
	if strings.Contains(subnet, "://") {
		return nil, fmt.Errorf("subnet_prefix 必须是三段 IPv4 前缀、完整 IPv4 或 /24 CIDR")
	}
	if ip := net.ParseIP(subnet); ip != nil && ip.To4() != nil {
		out.Set("subnet_prefix", ipv4ThreeOctets(ip.To4()))
		return out, nil
	}
	if prefix, ok := parseThreeOctetIPv4Prefix(subnet); ok {
		out.Set("subnet_prefix", prefix)
		return out, nil
	}
	ip, network, err := net.ParseCIDR(subnet)
	if err != nil || ip.To4() == nil || network.IP.To4() == nil {
		return nil, fmt.Errorf("subnet_prefix 必须是三段 IPv4 前缀、完整 IPv4 或 /24 CIDR")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones != 24 {
		return nil, fmt.Errorf("subnet_prefix CIDR 仅允许 /24")
	}
	out.Set("subnet_prefix", ipv4ThreeOctets(ip.To4()))
	return out, nil
}

func parseThreeOctetIPv4Prefix(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return "", false
	}
	normalized := make([]string, 3)
	for i, part := range parts {
		if part == "" {
			return "", false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 || strconv.Itoa(n) != part {
			return "", false
		}
		normalized[i] = strconv.Itoa(n)
	}
	return strings.Join(normalized, "."), true
}

func ipv4ThreeOctets(ip net.IP) string {
	ip = ip.To4()
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", ip[0], ip[1], ip[2])
}

func cleanOnvifDevices(raw any) []map[string]any {
	rows := asSlice(raw)
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		rawURL := strings.TrimSpace(asString(row["onvif_url"]))
		u, err := url.Parse(rawURL)
		if err != nil || !u.IsAbs() || u.Host == "" ||
			(u.Scheme != "http" && u.Scheme != "https") || hasControl(rawURL) {
			continue
		}
		u.User, u.RawQuery, u.Fragment, u.ForceQuery = nil, "", "", false
		copy := make(map[string]any, len(row))
		for key, value := range row {
			if strings.Contains(strings.ToLower(key), "pass") {
				continue
			}
			copy[key] = value
		}
		copy["onvif_url"] = u.String()
		out = append(out, copy)
	}
	return out
}

func (h *Hub) WebRTCRoomKeeperOperation(nodeID, user, action string, q url.Values) map[string]any {
	target := roomKeeperTarget(action, q)
	if h == nil || h.audit == nil {
		return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": "unknown node"})
	}
	vals, err := validateRoomKeeperOperation(action, q)
	if err != nil {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": err.Error()})
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return map[string]any{"code": -1, "msg": "操作审计预写失败，已拒绝执行: " + safeOnvifWebRTCError(err)}
	}
	result, callErr := h.zlm.callPOST(n, action, vals)
	if callErr != nil {
		result = map[string]any{"code": -1, "msg": safeOnvifWebRTCError(callErr)}
	}
	if result == nil {
		result = map[string]any{"code": -1, "msg": "ZLM 返回空结果"}
	}
	if operationSucceeded(result) && action == WebRTCRoomKeeperAdd {
		data, _ := result["data"].(map[string]any)
		if data == nil || strings.TrimSpace(asString(data["room_key"])) == "" {
			result["code"], result["msg"] = -1, "ZLM 未返回 data.room_key，操作结果无效"
		}
	}
	result["msg"] = sanitizeOnvifWebRTCText(asString(result["msg"]))
	if asString(result["msg"]) == "" {
		if operationSucceeded(result) {
			result["msg"] = "操作成功"
		} else {
			result["msg"] = "操作失败"
		}
	}
	return h.finishAuditedMutation(nodeID, user, action, target, operationID, result, true)
}

func validateRoomKeeperOperation(action string, q url.Values) (url.Values, error) {
	switch action {
	case WebRTCRoomKeeperAdd:
		host := strings.TrimSpace(q.Get("server_host"))
		host, err := normalizePureHost(host)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(strings.TrimSpace(q.Get("server_port")))
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("server_port 必须是 1..65535 的整数")
		}
		roomID := strings.TrimSpace(q.Get("room_id"))
		if err := validateOnvifWebRTCKey("room_id", roomID); err != nil {
			return nil, err
		}
		ssl := strings.TrimSpace(q.Get("ssl"))
		if ssl != "0" && ssl != "1" {
			return nil, fmt.Errorf("ssl 仅允许 0/1")
		}
		return url.Values{
			"server_host": {host}, "server_port": {strconv.Itoa(port)},
			"room_id": {roomID}, "ssl": {ssl},
		}, nil
	case WebRTCRoomKeeperDelete:
		key := strings.TrimSpace(q.Get("room_key"))
		if err := validateOnvifWebRTCKey("room_key", key); err != nil {
			return nil, err
		}
		return url.Values{"room_key": {key}}, nil
	default:
		return nil, fmt.Errorf("unknown WebRTC RoomKeeper operation")
	}
}

func normalizePureHost(host string) (string, error) {
	if host == "" {
		return "", fmt.Errorf("server_host 必填")
	}
	if len(host) > 253 || hasControl(host) || strings.Contains(host, "..") ||
		strings.Contains(host, "://") || strings.ContainsAny(host, "@/?#\\") {
		return "", fmt.Errorf("server_host 必须是纯主机名或 IP")
	}
	open, close := strings.HasPrefix(host, "["), strings.HasSuffix(host, "]")
	if open || close {
		if !open || !close || strings.Count(host, "[") != 1 || strings.Count(host, "]") != 1 {
			return "", fmt.Errorf("server_host IPv6 方括号必须成对")
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
		ip := net.ParseIP(inner)
		if ip == nil || ip.To4() != nil {
			return "", fmt.Errorf("server_host 方括号内必须是 IPv6")
		}
		return "[" + ip.String() + "]", nil
	}
	if strings.ContainsAny(host, "[]") {
		return "", fmt.Errorf("server_host IPv6 方括号必须成对")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("server_host 必须是纯主机名或 IP")
		}
		for _, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' {
				return "", fmt.Errorf("server_host 必须是纯主机名或 IP")
			}
		}
	}
	return host, nil
}

func validateOnvifWebRTCKey(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s 必填", name)
	}
	if len(value) > 1024 || strings.Contains(value, "..") || hasControl(value) {
		return fmt.Errorf("%s 含非法字符", name)
	}
	return nil
}

func roomKeeperTarget(action string, q url.Values) string {
	if action == WebRTCRoomKeeperDelete {
		return strings.TrimSpace(q.Get("room_key"))
	}
	return sanitizeOnvifWebRTCText(strings.TrimSpace(q.Get("server_host"))) + ":" +
		strings.TrimSpace(q.Get("server_port")) + "/" + strings.TrimSpace(q.Get("room_id"))
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
