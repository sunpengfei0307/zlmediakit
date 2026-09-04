package service

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

const (
	ipModeOff    = "off"
	ipModeAllow  = "allow"
	ipModeDeny   = "deny"
	ipListBlack  = "black"
	ipListWhite  = "white"
	streamIPPref = "ip"
)

type StreamIPRule struct {
	ID        string `json:"id"`
	IP        string `json:"ip"`
	List      string `json:"list"`
	AllowPush bool   `json:"allow_push"`
	AllowPlay bool   `json:"allow_play"`
	Note      string `json:"note"`
	Disabled  bool   `json:"disabled,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func normalizeIPMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ipModeAllow:
		return ipModeAllow
	case ipModeDeny:
		return ipModeDeny
	default:
		return ipModeOff
	}
}

func normalizePeerIP(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("IP 不能为空")
	}
	if strings.HasPrefix(raw, "[") {
		if i := strings.Index(raw, "]"); i > 1 {
			raw = raw[1:i]
		}
	} else if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	if ip := net.ParseIP(raw); ip != nil {
		return ip.String(), nil
	}
	if _, n, err := net.ParseCIDR(raw); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("无效 IP：%s", raw)
}

func ipRuleMatches(ruleIP, got string) bool {
	got = strings.TrimSpace(got)
	if got == "" || ruleIP == "" {
		return false
	}
	if parsed := net.ParseIP(got); parsed != nil {
		got = parsed.String()
	}
	if ruleIP == got {
		return true
	}
	if _, n, err := net.ParseCIDR(ruleIP); err == nil {
		ip := net.ParseIP(got)
		return ip != nil && n.Contains(ip)
	}
	return false
}

func hookRequestIP(body map[string]any) string {
	for _, k := range []string{"ip", "peer_ip", "real_ip"} {
		if v, err := normalizePeerIP(asString(body[k])); err == nil {
			return v
		}
	}
	return ""
}

func denyByIP(st streamAuthState, isPush bool, ip string) (bool, string) {
	mode := normalizeIPMode(st.IPMode)
	if mode == ipModeOff {
		return false, ""
	}
	if ip == "" {
		if mode == ipModeDeny {
			return true, "缺少客户端 IP"
		}
		return false, ""
	}
	if mode == ipModeAllow {
		for _, r := range st.IPs {
			if r.Disabled || r.List != ipListBlack || !ipRuleMatches(r.IP, ip) {
				continue
			}
			if isPush && r.AllowPush {
				return true, "IP 在推流黑名单"
			}
			if !isPush && r.AllowPlay {
				return true, "IP 在拉流黑名单"
			}
		}
		return false, ""
	}
	for _, r := range st.IPs {
		if r.Disabled || r.List != ipListWhite || !ipRuleMatches(r.IP, ip) {
			continue
		}
		if isPush && r.AllowPush {
			return false, ""
		}
		if !isPush && r.AllowPlay {
			return false, ""
		}
	}
	if isPush {
		return true, "IP 未在推流白名单"
	}
	return true, "IP 未在拉流白名单"
}

func (h *Hub) SetStreamIPMode(mode string) error {
	mode = normalizeIPMode(mode)
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	st.IPMode = mode
	err := h.saveStreamAuth(st)
	streamAuthMu.Unlock()
	if err != nil {
		return err
	}
	logger.Infor("IP 限制模式=%s", mode)
	return nil
}

func (h *Hub) AddStreamIPRule(ip, list string, push, play bool, note string) (StreamIPRule, error) {
	norm, err := normalizePeerIP(ip)
	if err != nil {
		return StreamIPRule{}, err
	}
	list = strings.ToLower(strings.TrimSpace(list))
	if list != ipListBlack && list != ipListWhite {
		return StreamIPRule{}, fmt.Errorf("名单类型须为 black 或 white")
	}
	if !push && !play {
		return StreamIPRule{}, fmt.Errorf("至少选择推流或拉流其一")
	}
	note = strings.TrimSpace(note)
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	now := time.Now()
	for _, r := range st.IPs {
		if r.IP == norm && r.List == list {
			streamAuthMu.Unlock()
			return StreamIPRule{}, fmt.Errorf("该 IP 已在%s中", listLabel(list))
		}
	}
	item := StreamIPRule{
		ID: streamIPPref + "-" + fmt.Sprintf("%x", now.UnixNano()),
		IP: norm, List: list, AllowPush: push, AllowPlay: play,
		Note: note, CreatedAt: now.Unix(),
	}
	st.IPs = append(st.IPs, item)
	if st.IPMode == "" || st.IPMode == ipModeOff {
		if list == ipListBlack {
			st.IPMode = ipModeAllow
		}
	}
	err = h.saveStreamAuth(st)
	mode := st.IPMode
	streamAuthMu.Unlock()
	if err != nil {
		return item, err
	}
	logger.Infor("IP 规则已新增 ip=%s list=%s push=%v play=%v mode=%s", item.IP, item.List, item.AllowPush, item.AllowPlay, mode)
	return item, nil
}

func listLabel(list string) string {
	if list == ipListWhite {
		return "白名单"
	}
	return "黑名单"
}

func (h *Hub) ToggleStreamIPRule(id string, enabled bool) (StreamIPRule, error) {
	id = strings.TrimSpace(id)
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	for i := range st.IPs {
		if st.IPs[i].ID != id {
			continue
		}
		st.IPs[i].Disabled = !enabled
		item := st.IPs[i]
		err := h.saveStreamAuth(st)
		streamAuthMu.Unlock()
		if err != nil {
			return item, err
		}
		logger.Infor("IP 规则 id=%s ip=%s enabled=%v", id, item.IP, enabled)
		return item, nil
	}
	streamAuthMu.Unlock()
	return StreamIPRule{}, fmt.Errorf("IP 规则不存在")
}

func (h *Hub) DeleteStreamIPRule(id string) error {
	id = strings.TrimSpace(id)
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	out := st.IPs[:0]
	found := false
	for _, r := range st.IPs {
		if r.ID == id {
			if !r.Disabled {
				streamAuthMu.Unlock()
				return fmt.Errorf("请先停用再删除")
			}
			found = true
			continue
		}
		out = append(out, r)
	}
	if !found {
		streamAuthMu.Unlock()
		return fmt.Errorf("IP 规则不存在")
	}
	st.IPs = out
	err := h.saveStreamAuth(st)
	streamAuthMu.Unlock()
	if err != nil {
		return err
	}
	logger.Infor("IP 规则已删除 id=%s", id)
	return nil
}

func (h *Hub) PeerIPListed(ip string) (black, white bool) {
	norm, err := normalizePeerIP(ip)
	if err != nil {
		return false, false
	}
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	streamAuthMu.Unlock()
	for _, r := range st.IPs {
		if !ipRuleMatches(r.IP, norm) {
			continue
		}
		if r.List == ipListBlack {
			black = true
		}
		if r.List == ipListWhite {
			white = true
		}
	}
	return black, white
}

func (h *Hub) KickPeerIPAllNodes(ip string) int {
	norm, err := normalizePeerIP(ip)
	if err != nil || h == nil || h.zlm == nil || config.C == nil {
		return 0
	}
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	hit := 0
	for _, n := range nodes {
		v, callErr := h.zlm.callPOST(n, "kick_sessions", url.Values{"peer_ip": {norm}})
		if callErr != nil || v == nil {
			continue
		}
		hit += int(asFloat(v["count_hit"]))
	}
	if hit > 0 {
		logger.Infor("黑名单已踢掉对端 %s 的 %d 个在线连接", norm, hit)
	}
	return hit
}
