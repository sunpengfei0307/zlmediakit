package service

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"zlm-admin/core/logger"
)

const (
	streamAuthKVKey  = "stream-tokens"
	streamAuthIDPref = "tok"
)

type StreamAuthToken struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	AllowPush bool   `json:"allow_push"`
	AllowPlay bool   `json:"allow_play"`
	App       string `json:"app"`
	Stream    string `json:"stream"`
	Enabled   bool   `json:"enabled"`
	ExpireAt  int64  `json:"expire_at"`
	CreatedAt int64  `json:"created_at"`
}

type streamAuthState struct {
	Enabled bool              `json:"enabled"`
	Tokens  []StreamAuthToken `json:"tokens"`
	IPMode  string            `json:"ip_mode,omitempty"`
	IPs     []StreamIPRule    `json:"ips,omitempty"`
}

var streamAuthMu sync.Mutex

func (h *Hub) streamAuthKV() *LocalKV {
	if h == nil {
		return nil
	}
	return h.kv
}

func (h *Hub) loadStreamAuth() streamAuthState {
	out := streamAuthState{}
	kv := h.streamAuthKV()
	if kv == nil {
		return out
	}
	_ = kv.ForEach(kvBucketAuth, func(k, v []byte) error {
		if string(k) != streamAuthKVKey {
			return nil
		}
		_ = json.Unmarshal(v, &out)
		return nil
	})
	if out.Tokens == nil {
		out.Tokens = []StreamAuthToken{}
	}
	if out.IPs == nil {
		out.IPs = []StreamIPRule{}
	}
	out.IPMode = normalizeIPMode(out.IPMode)
	return out
}

func (h *Hub) saveStreamAuth(st streamAuthState) error {
	kv := h.streamAuthKV()
	if kv == nil {
		return fmt.Errorf("本地存储不可用")
	}
	if st.Tokens == nil {
		st.Tokens = []StreamAuthToken{}
	}
	if st.IPs == nil {
		st.IPs = []StreamIPRule{}
	}
	st.IPMode = normalizeIPMode(st.IPMode)
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return kv.Put(kvBucketAuth, []byte(streamAuthKVKey), raw)
}

func (h *Hub) StreamAuthView() map[string]any {
	streamAuthMu.Lock()
	defer streamAuthMu.Unlock()
	st := h.loadStreamAuth()
	items := make([]map[string]any, 0, len(st.Tokens))
	now := time.Now().Unix()
	for _, t := range st.Tokens {
		expired := t.ExpireAt > 0 && now > t.ExpireAt
		items = append(items, map[string]any{
			"id": t.ID, "name": t.Name, "token": t.Token,
			"allow_push": t.AllowPush, "allow_play": t.AllowPlay,
			"app": t.App, "stream": t.Stream, "enabled": t.Enabled && !expired,
			"expired": expired, "expire_at": t.ExpireAt, "created_at": t.CreatedAt,
		})
	}
	ips := make([]map[string]any, 0, len(st.IPs))
	listed := make([]map[string]string, 0, len(st.IPs))
	for _, r := range st.IPs {
		ips = append(ips, map[string]any{
			"id": r.ID, "ip": r.IP, "list": r.List,
			"allow_push": r.AllowPush, "allow_play": r.AllowPlay,
			"note": r.Note, "enabled": !r.Disabled, "created_at": r.CreatedAt,
		})
		listed = append(listed, map[string]string{"ip": r.IP, "list": r.List})
	}
	return map[string]any{
		"enabled": st.Enabled, "tokens": items, "count": len(items), "has_tokens": len(items) > 0,
		"ip_mode": st.IPMode, "ips": ips, "ip_count": len(ips), "listed": listed,
	}
}

func (h *Hub) SetStreamAuthEnabled(on bool) error {
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	st.Enabled = on
	if on && len(st.Tokens) == 0 {
		st.Tokens = append(st.Tokens, newStreamAuthToken("控制台预览", "", true, true, "", "", 0))
	}
	err := h.saveStreamAuth(st)
	streamAuthMu.Unlock()
	if err != nil {
		return err
	}
	if on {
		logger.Infor("鉴权开关已开启 tokens=%d", len(st.Tokens))
	} else {
		logger.Infor("鉴权开关已关闭，推拉流不再校验 token")
	}
	h.afterStreamAuthChanged()
	return nil
}

func (h *Hub) AddStreamAuthToken(name, token string, push, play bool, app, stream string, expireDays int) (StreamAuthToken, error) {
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	item := newStreamAuthToken(name, token, push, play, app, stream, expireDays)
	if item.Token == "" {
		streamAuthMu.Unlock()
		return item, fmt.Errorf("token 不能为空")
	}
	if !item.AllowPush && !item.AllowPlay {
		streamAuthMu.Unlock()
		return item, fmt.Errorf("至少选择推流或播放其一")
	}
	st.Tokens = append(st.Tokens, item)
	st.Enabled = true
	err := h.saveStreamAuth(st)
	streamAuthMu.Unlock()
	if err != nil {
		return item, err
	}
	logger.Infor("鉴权新增 Token name=%s app=%s stream=%s push=%v play=%v 已开启校验", item.Name, item.App, item.Stream, item.AllowPush, item.AllowPlay)
	h.afterStreamAuthChanged()
	return item, nil
}

func (h *Hub) DeleteStreamAuthToken(id string) error {
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	out := st.Tokens[:0]
	found := false
	for _, t := range st.Tokens {
		if t.ID == id {
			if t.Enabled {
				streamAuthMu.Unlock()
				return fmt.Errorf("请先停用再删除")
			}
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		streamAuthMu.Unlock()
		return fmt.Errorf("token 不存在")
	}
	st.Tokens = out
	if len(st.Tokens) == 0 {
		st.Enabled = false
	}
	err := h.saveStreamAuth(st)
	off := !st.Enabled
	streamAuthMu.Unlock()
	if err != nil {
		return err
	}
	if off {
		logger.Infor("鉴权删除最后一个 Token，已关闭校验")
	} else {
		logger.Infor("鉴权删除 Token id=%s", id)
	}
	h.afterStreamAuthChanged()
	return nil
}

func (h *Hub) ToggleStreamAuthToken(id string, enabled bool) error {
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	for i := range st.Tokens {
		if st.Tokens[i].ID == id {
			st.Tokens[i].Enabled = enabled
			err := h.saveStreamAuth(st)
			streamAuthMu.Unlock()
			if err != nil {
				return err
			}
			logger.Infor("鉴权 Token id=%s enabled=%v", id, enabled)
			h.afterStreamAuthChanged()
			return nil
		}
	}
	streamAuthMu.Unlock()
	return fmt.Errorf("token 不存在")
}

func (h *Hub) afterStreamAuthChanged() {
	if h == nil {
		return
	}
	h.restartDashJobsForAuth()
	h.stopSnapWorkers()
}

func (h *Hub) restartDashJobsForAuth() {
	dashMu.Lock()
	old := dashJobs
	dashJobs = map[string]*dashJob{}
	dashMu.Unlock()
	for _, job := range old {
		if job != nil && job.cancel != nil {
			job.cancel()
		}
	}
	if DashEnabled() {
		go h.EnsureDASHAll()
	}
}

func newStreamAuthToken(name, token string, push, play bool, app, stream string, expireDays int) StreamAuthToken {
	name = strings.TrimSpace(name)
	token = strings.TrimSpace(token)
	if token == "" {
		token = randomStreamToken()
	}
	if name == "" {
		name = "token"
	}
	now := time.Now()
	item := StreamAuthToken{
		ID: streamAuthIDPref + "-" + fmt.Sprintf("%x", now.UnixNano()),
		Name: name, Token: token, AllowPush: push, AllowPlay: play,
		App: strings.TrimSpace(app), Stream: strings.TrimSpace(stream),
		Enabled: true, CreatedAt: now.Unix(),
	}
	if expireDays > 0 {
		item.ExpireAt = now.Add(time.Duration(expireDays) * 24 * time.Hour).Unix()
	}
	return item
}

func randomStreamToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func hookRequestToken(body map[string]any) string {
	if t := strings.TrimSpace(asString(body["token"])); t != "" {
		return t
	}
	q, err := url.ParseQuery(asString(body["params"]))
	if err != nil {
		q = url.Values{}
	}
	if t := strings.TrimSpace(q.Get("token")); t != "" {
		return t
	}
	path := asString(body["path"])
	if i := strings.Index(path, "?"); i >= 0 {
		if pq, err := url.ParseQuery(path[i+1:]); err == nil {
			if t := strings.TrimSpace(pq.Get("token")); t != "" {
				return t
			}
		}
	}
	return ""
}

func parseHTTPMediaPath(raw string) (app, stream string) {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	raw = strings.Trim(raw, "/")
	if raw == "" || strings.HasPrefix(raw, "index/") || strings.HasPrefix(raw, "static/") {
		return "", ""
	}
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return "", ""
	}
	app = parts[0]
	stream = parts[1]
	for _, suf := range []string{".live.flv", ".live.ts", ".flv", ".mp4", ".m3u8", ".mpd", ".ts", ".fmp4", ".m4s"} {
		if len(stream) > len(suf) && strings.EqualFold(stream[len(stream)-len(suf):], suf) {
			stream = stream[:len(stream)-len(suf)]
			break
		}
	}
	if invalidRecordName(app) || invalidRecordName(stream) {
		return "", ""
	}
	return app, stream
}

func tokenMatches(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	gb, wb := []byte(got), []byte(want)
	if len(gb) != len(wb) {
		return false
	}
	return subtle.ConstantTimeCompare(gb, wb) == 1
}

func tokenActive(t StreamAuthToken, now int64) bool {
	if !t.Enabled {
		return false
	}
	return t.ExpireAt == 0 || now <= t.ExpireAt
}

func tokenCovers(t StreamAuthToken, app, stream string) bool {
	if t.App != "" && t.App != app {
		return false
	}
	if t.Stream != "" && t.Stream != stream {
		return false
	}
	return true
}

func streamAuthRequired(st streamAuthState, app, stream string, now int64) bool {
	if !st.Enabled {
		return false
	}
	for _, t := range st.Tokens {
		if tokenActive(t, now) && tokenCovers(t, app, stream) {
			return true
		}
	}
	return false
}

func (h *Hub) denyStreamHook(event string, body map[string]any) (bool, string) {
	playLike := event == "on_play" || event == "on_http_access"
	if event != "on_publish" && !playLike {
		return false, ""
	}
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	streamAuthMu.Unlock()
	app := strings.TrimSpace(asString(body["app"]))
	stream := strings.TrimSpace(asString(body["stream"]))
	if event == "on_http_access" && (app == "" || stream == "") {
		app, stream = parseHTTPMediaPath(asString(body["path"]))
		if app == "" || stream == "" {
			return false, ""
		}
	}
	if event == "on_http_access" && httpAccessIsContinuitySegment(asString(body["path"])) {
		return false, ""
	}
	ip := hookRequestIP(body)
	if deny, msg := denyByIP(st, event == "on_publish", ip); deny {
		logger.Warnf("鉴权拒绝 %s %s/%s ip=%s: %s", event, app, stream, ip, msg)
		return true, msg
	}
	if !st.Enabled {
		return false, ""
	}
	now := time.Now().Unix()
	if !streamAuthRequired(st, app, stream, now) {
		return false, ""
	}
	got := hookRequestToken(body)
	if got == "" {
		logger.Warnf("鉴权拒绝 %s %s/%s: 缺少 token", event, app, stream)
		return true, "缺少 token"
	}
	for _, t := range st.Tokens {
		if !tokenActive(t, now) || !tokenMatches(got, t.Token) || !tokenCovers(t, app, stream) {
			continue
		}
		if event == "on_publish" && !t.AllowPush {
			logger.Warnf("鉴权拒绝 %s %s/%s: 该 token 不允许推流", event, app, stream)
			return true, "该 token 不允许推流"
		}
		if playLike && !t.AllowPlay {
			logger.Warnf("鉴权拒绝 %s %s/%s: 该 token 不允许播放", event, app, stream)
			return true, "该 token 不允许播放"
		}
		logger.Infor("鉴权通过 %s %s/%s name=%s", event, app, stream, t.Name)
		return false, ""
	}
	logger.Warnf("鉴权拒绝 %s %s/%s: token 无效", event, app, stream)
	return true, "token 无效"
}

func (h *Hub) playTokenFor(app, stream string) string {
	return h.mediaTokenFor(app, stream, true)
}

func (h *Hub) mediaTokenFor(app, stream string, play bool) string {
	streamAuthMu.Lock()
	st := h.loadStreamAuth()
	streamAuthMu.Unlock()
	now := time.Now().Unix()
	if !streamAuthRequired(st, app, stream, now) {
		return ""
	}
	for _, t := range st.Tokens {
		if !tokenActive(t, now) || !tokenCovers(t, app, stream) {
			continue
		}
		if play && t.AllowPlay {
			return t.Token
		}
		if !play && t.AllowPush {
			return t.Token
		}
	}
	return ""
}

func isPlayMediaURL(u string) bool {
	lu := strings.ToLower(strings.TrimSpace(u))
	if strings.HasPrefix(lu, "ffplay ") {
		return isPlayMediaURL(strings.TrimSpace(u[6:]))
	}
	for _, p := range []string{"http://", "https://", "ws://", "wss://", "rtmp://", "rtsp://", "webrtc://", "webrtcs://", "srt://"} {
		if strings.HasPrefix(lu, p) {
			return true
		}
	}
	return false
}

func httpAccessIsContinuitySegment(path string) bool {
	p := strings.ToLower(strings.TrimSpace(path))
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if strings.Contains(p, ".live.") || strings.HasSuffix(p, ".m3u8") || strings.HasSuffix(p, ".mpd") {
		return false
	}
	base := p
	if i := strings.LastIndex(p, "/"); i >= 0 {
		base = p[i+1:]
	}
	if base == "init.mp4" {
		return true
	}
	for _, suf := range []string{".ts", ".m4s", ".cmfv", ".cmfa", ".m4a", ".m4v"} {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

func appendSRTPlayToken(u, tok string) string {
	const key = "streamid="
	i := strings.Index(strings.ToLower(u), key)
	if i < 0 || strings.Contains(u, "token=") {
		return u
	}
	start := i + len(key)
	endRel := strings.Index(u[start:], "&")
	sid, rest := u[start:], ""
	if endRel >= 0 {
		sid = u[start : start+endRel]
		rest = u[start+endRel:]
	}
	if strings.Contains(sid, "token=") {
		return u
	}
	return u[:start] + sid + ",token=" + tok + rest
}

func withStreamPlayToken(u, app, stream string) string {
	if H == nil || u == "" || strings.Contains(u, "token=") || !isPlayMediaURL(u) {
		return u
	}
	tok := H.playTokenFor(app, stream)
	if tok == "" {
		return u
	}
	lu := strings.ToLower(strings.TrimSpace(u))
	if strings.HasPrefix(lu, "ffplay ") {
		return "ffplay " + withStreamPlayToken(strings.TrimSpace(u[6:]), app, stream)
	}
	if strings.HasPrefix(lu, "srt://") {
		return appendSRTPlayToken(u, tok)
	}
	if strings.HasPrefix(lu, "webrtc://") || strings.HasPrefix(lu, "webrtcs://") {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "token=" + url.QueryEscape(tok)
}
