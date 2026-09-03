package service

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	RTPOpenServer          = "openRtpServer"
	RTPOpenServerMultiplex = "openRtpServerMultiplex"
	RTPConnectServer       = "connectRtpServer"
	RTPCloseServer         = "closeRtpServer"
	RTPUpdateSSRC          = "updateRtpServerSSRC"
	RTPPauseCheck          = "pauseRtpCheck"
	RTPResumeCheck         = "resumeRtpCheck"
	RTPStartSend           = "startSendRtp"
	RTPStartSendPassive    = "startSendRtpPassive"
	RTPStartSendTalk       = "startSendRtpTalk"
	RTPStopSend            = "stopSendRtp"
)

type RTPSection struct {
	Items []map[string]any
	Error string
}

type RTPView struct {
	Receivers RTPSection
	Senders   RTPSection
}

func (h *Hub) ListRTP(nodeID, vhost, app, stream string) RTPView {
	out := RTPView{
		Receivers: RTPSection{Items: []map[string]any{}},
		Senders:   RTPSection{Items: []map[string]any{}},
	}
	if h == nil || h.zlm == nil {
		out.Receivers.Error, out.Senders.Error = "ZLM 客户端不可用", "ZLM 客户端不可用"
		return out
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		out.Receivers.Error, out.Senders.Error = "unknown node", "unknown node"
		return out
	}
	if raw, err := h.zlm.call(n, "listRtpServer", nil); err != nil {
		out.Receivers.Error = err.Error()
	} else {
		out.Receivers.Items = sourceTaskRows(raw["data"])
		for _, row := range out.Receivers.Items {
			streamID := strings.TrimSpace(asString(row["stream_id"]))
			if streamID == "" {
				row["_error"] = "响应缺少 stream_id"
				continue
			}
			vals := url.Values{"stream_id": {streamID}}
			if value := strings.TrimSpace(asString(row["vhost"])); value != "" {
				vals.Set("vhost", value)
			}
			if value := strings.TrimSpace(asString(row["app"])); value != "" {
				vals.Set("app", value)
			}
			detail, detailErr := h.zlm.call(n, "getRtpInfo", vals)
			if detailErr != nil {
				row["_error"] = detailErr.Error()
				continue
			}
			for key, value := range detail {
				if key != "code" && key != "msg" {
					row[key] = value
				}
			}
		}
	}
	vhost = strings.TrimSpace(vhost)
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" || stream == "" {
		return out
	}
	raw, err := h.zlm.call(n, "listRtpSender", url.Values{
		"vhost": {vhost}, "app": {app}, "stream": {stream},
	})
	if err != nil {
		out.Senders.Error = err.Error()
		return out
	}
	senders := rtpSenderValues(raw["data"])
	for _, item := range senders {
		row := map[string]any{"vhost": vhost, "app": app, "stream": stream, "ssrc": item}
		if len(senders) == 1 {
			row["bytesSpeed"], row["totalBytes"] = raw["bytesSpeed"], raw["totalBytes"]
		} else {
			row["_stats_unavailable"] = true
			row["_stats_note"] = "当前ZLM API不提供逐发送器统计"
		}
		out.Senders.Items = append(out.Senders.Items, row)
	}
	return out
}

func rtpSenderValues(value any) []string {
	var out []string
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if value := strings.TrimSpace(asString(item)); value != "" {
				out = append(out, value)
			}
		}
	case []string:
		for _, item := range values {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func (h *Hub) RTPOperation(nodeID, user, action string, q url.Values) map[string]any {
	if q == nil {
		q = url.Values{}
	}
	action = strings.TrimSpace(action)
	target := rtpAuditTarget(action, q)
	if h == nil || h.audit == nil {
		return map[string]any{"code": -1, "msg": "操作审计不可用，已拒绝执行"}
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": "unknown node"})
	}
	vals, err := validateRTPOperation(action, q)
	if err != nil {
		return h.recordRejectedMutation(nodeID, user, action, target, map[string]any{"code": -1, "msg": err.Error()})
	}
	operationID, err := h.beginAuditedMutation(nodeID, user, action, target)
	if err != nil {
		return map[string]any{"code": -1, "msg": "操作审计预写失败，已拒绝执行: " + err.Error()}
	}
	result, callErr := h.zlm.callPOST(n, action, vals)
	if callErr != nil {
		result = map[string]any{"code": -1, "msg": callErr.Error()}
	}
	if result == nil {
		result = map[string]any{"code": -1, "msg": "ZLM 返回空结果"}
	}
	if callErr == nil && operationSucceeded(result) {
		switch action {
		case RTPCloseServer:
			if asFloat(result["hit"]) <= 0 {
				result["code"], result["msg"] = -1, "ZLM 未找到或未关闭 RTP 接收服务（hit=0）"
			}
		case RTPOpenServer, RTPOpenServerMultiplex:
			if asFloat(result["port"]) <= 0 {
				result["code"], result["msg"] = -1, "ZLM 未返回有效监听端口"
			}
		case RTPStartSend, RTPStartSendPassive, RTPStartSendTalk:
			if asFloat(result["local_port"]) <= 0 {
				result["code"], result["msg"] = -1, "ZLM 未返回有效本地端口"
			}
		}
	}
	if strings.TrimSpace(asString(result["msg"])) == "" {
		if operationSucceeded(result) {
			result["msg"] = "操作成功"
		} else {
			result["msg"] = "操作失败"
		}
	}
	return h.finishAuditedMutation(nodeID, user, action, target, operationID, result, true)
}

func validateRTPOperation(action string, q url.Values) (url.Values, error) {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	out := url.Values{"vhost": {vhost}}
	if err := validateSourceName("vhost", vhost, true); err != nil {
		return nil, err
	}
	copyName := func(name string, required bool) error {
		value := strings.TrimSpace(q.Get(name))
		if err := validateSourceName(name, value, required); err != nil {
			return err
		}
		if value != "" {
			out.Set(name, value)
		}
		return nil
	}
	copySSRC := func(required bool) error {
		raw := strings.TrimSpace(q.Get("ssrc"))
		if raw == "" {
			if required {
				return fmt.Errorf("ssrc 必填")
			}
			return nil
		}
		value, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return fmt.Errorf("ssrc 必须是 0..4294967295 的十进制整数")
		}
		out.Set("ssrc", strconv.FormatUint(value, 10))
		return nil
	}
	switch action {
	case RTPOpenServer, RTPOpenServerMultiplex:
		if err := copyName("stream_id", true); err != nil {
			return nil, err
		}
		if err := copyName("app", false); err != nil {
			return nil, err
		}
		if strings.TrimSpace(q.Get("port")) == "" {
			return nil, fmt.Errorf("port 必填")
		}
		if err := copyBoundedInt(out, q, "port", 0, 65535); err != nil {
			return nil, err
		}
		tcpModes := []string{"0", "1", "2"}
		if action == RTPOpenServerMultiplex {
			tcpModes = []string{"0", "1"}
		}
		if err := copyEnum(out, q, "tcp_mode", tcpModes...); err != nil {
			return nil, err
		}
		if err := copyEnum(out, q, "only_track", "0", "1", "2"); err != nil {
			return nil, err
		}
		if action == RTPOpenServer {
			if err := copyEnum(out, q, "re_use_port", "0", "1"); err != nil {
				return nil, err
			}
			if err := copySSRC(false); err != nil {
				return nil, err
			}
		}
		if raw := strings.TrimSpace(q.Get("local_ip")); raw != "" {
			if net.ParseIP(raw) == nil {
				return nil, fmt.Errorf("local_ip 必须是 IP 地址")
			}
			out.Set("local_ip", raw)
		}
	case RTPConnectServer:
		if err := copyRTPReceiverIdentity(out, q, copyName); err != nil {
			return nil, err
		}
		if err := copyHost(out, q, "dst_url", true); err != nil {
			return nil, err
		}
		if strings.TrimSpace(q.Get("dst_port")) == "" {
			return nil, fmt.Errorf("dst_port 必填")
		}
		if err := copyBoundedInt(out, q, "dst_port", 1, 65535); err != nil {
			return nil, err
		}
	case RTPCloseServer, RTPResumeCheck:
		if err := copyRTPReceiverIdentity(out, q, copyName); err != nil {
			return nil, err
		}
	case RTPUpdateSSRC:
		if err := copyRTPReceiverIdentity(out, q, copyName); err != nil {
			return nil, err
		}
		if err := copySSRC(true); err != nil {
			return nil, err
		}
	case RTPPauseCheck:
		if err := copyRTPReceiverIdentity(out, q, copyName); err != nil {
			return nil, err
		}
		if err := copyBoundedInt(out, q, "pause_seconds", 1, 86400); err != nil {
			return nil, err
		}
	case RTPStartSend, RTPStartSendPassive, RTPStartSendTalk, RTPStopSend:
		for _, name := range []string{"app", "stream"} {
			if err := copyName(name, true); err != nil {
				return nil, err
			}
		}
		if action == RTPStopSend {
			if err := copySSRC(false); err != nil {
				return nil, err
			}
			break
		}
		if err := copySSRC(true); err != nil {
			return nil, err
		}
		if action == RTPStartSend {
			if err := copyHost(out, q, "dst_url", true); err != nil {
				return nil, err
			}
			if strings.TrimSpace(q.Get("dst_port")) == "" {
				return nil, fmt.Errorf("dst_port 必填")
			}
			if err := copyBoundedInt(out, q, "dst_port", 1, 65535); err != nil {
				return nil, err
			}
			if strings.TrimSpace(q.Get("is_udp")) == "" {
				return nil, fmt.Errorf("is_udp 必填")
			}
			if err := copyEnum(out, q, "is_udp", "0", "1"); err != nil {
				return nil, err
			}
			if err := copyEnum(out, q, "ssrc_multi_send", "0", "1"); err != nil {
				return nil, err
			}
			if err := copyBoundedInt(out, q, "udp_rtcp_timeout", 0, 3600000); err != nil {
				return nil, err
			}
			if err := copyBoundedInt(out, q, "close_delay_ms", 0, 3600000); err != nil {
				return nil, err
			}
		}
		if action == RTPStartSendTalk {
			if err := copyName("recv_stream_id", true); err != nil {
				return nil, err
			}
		}
		if action != RTPStartSendTalk {
			if err := copyBoundedInt(out, q, "src_port", 0, 65535); err != nil {
				return nil, err
			}
		}
		if err := copyBoundedInt(out, q, "pt", 0, 127); err != nil {
			return nil, err
		}
		if err := copyEnum(out, q, "type", "0", "1", "2"); err != nil {
			return nil, err
		}
		for _, name := range []string{"only_audio", "from_mp4", "enable_origin_recv_limit"} {
			if err := copyEnum(out, q, name, "0", "1"); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("unknown RTP operation")
	}
	return out, nil
}

func copyRTPReceiverIdentity(_ url.Values, _ url.Values, copyName func(string, bool) error) error {
	if err := copyName("stream_id", true); err != nil {
		return err
	}
	return copyName("app", false)
}

var rtpHostnamePattern = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.?$`)

func copyHost(out, in url.Values, name string, required bool) error {
	raw := strings.TrimSpace(in.Get(name))
	if raw == "" {
		if required {
			return fmt.Errorf("%s 必填", name)
		}
		return nil
	}
	if len(raw) > 253 || strings.Contains(raw, "://") || strings.ContainsAny(raw, `/\@?#`) {
		return fmt.Errorf("%s 只能填写纯主机名或 IP", name)
	}
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%s 含非法字符", name)
		}
	}
	if net.ParseIP(strings.Trim(raw, "[]")) == nil && !rtpHostnamePattern.MatchString(raw) {
		return fmt.Errorf("%s 只能填写纯主机名或 IP", name)
	}
	out.Set(name, raw)
	return nil
}

func rtpAuditTarget(action string, q url.Values) string {
	vhost := strings.TrimSpace(q.Get("vhost"))
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	switch action {
	case RTPOpenServer, RTPOpenServerMultiplex, RTPConnectServer, RTPCloseServer, RTPUpdateSSRC, RTPPauseCheck, RTPResumeCheck:
		target := vhost + "/" + strings.TrimSpace(q.Get("app")) + "/" + strings.TrimSpace(q.Get("stream_id"))
		if action == RTPConnectServer {
			target += " -> " + rtpAuditHost(q.Get("dst_url")) + ":" + strings.TrimSpace(q.Get("dst_port"))
		}
		return target
	case RTPStartSend, RTPStartSendPassive, RTPStartSendTalk, RTPStopSend:
		target := vhost + "/" + strings.TrimSpace(q.Get("app")) + "/" + strings.TrimSpace(q.Get("stream"))
		if action == RTPStartSend {
			target += " -> " + rtpAuditHost(q.Get("dst_url")) + ":" + strings.TrimSpace(q.Get("dst_port"))
		} else if action == RTPStartSendTalk {
			target += " <-> " + strings.TrimSpace(q.Get("recv_stream_id"))
		} else if action == RTPStartSendPassive {
			target += " listen:" + strings.TrimSpace(q.Get("src_port"))
		}
		return target
	default:
		return ""
	}
}

func rtpAuditHost(raw string) string {
	values := url.Values{"host": {raw}}
	out := url.Values{}
	if copyHost(out, values, "host", true) != nil {
		return "[invalid-host]"
	}
	return out.Get("host")
}
