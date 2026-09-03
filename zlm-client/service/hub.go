package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
	"zlm-admin/model"
)

var H *Hub

type Hub struct {
	zlm           *zlmClient
	audit         AuditService
	mu            sync.Mutex
	sourceTaskMu  sync.Mutex
	snapWorkerMu  sync.Mutex
	snapWorkers   map[snapWorkerKey]*snapWorkerJob
	snapWorkerRun func(context.Context, snapWorkerSpec) error
	hooks         []model.HookEvent
	hist          *historyStore
	clock         sessClock
	online        map[string]bool
	versions      map[string]versionCacheEntry
	vodMu         sync.Mutex
	vodLoads      map[string]vodLoad
}

type sessClock struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func sessID(row map[string]any) string {
	id := asString(row["id"])
	if id == "" {
		id = asString(row["identifier"])
	}
	return id
}

func (c *sessClock) key(nodeID, id string) string { return nodeID + "|" + id }

func (c *sessClock) mark(nodeID, id string, started time.Time) {
	if id == "" {
		return
	}
	if started.IsZero() {
		started = time.Now()
	}
	k := c.key(nodeID, id)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]time.Time{}
	}
	if old, ok := c.seen[k]; !ok || started.Before(old) {
		c.seen[k] = started
	}
}

func (c *sessClock) forget(nodeID, id string) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, c.key(nodeID, id))
}

func (c *sessClock) observe(nodeID string, rows []map[string]any, prune bool) {
	now := time.Now()
	alive := make(map[string]struct{}, len(rows))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen == nil {
		c.seen = map[string]time.Time{}
	}
	for _, row := range rows {
		id := sessID(row)
		if id == "" {
			continue
		}
		k := c.key(nodeID, id)
		if _, ok := c.seen[k]; !ok {
			c.seen[k] = now
		}
		alive[k] = struct{}{}
		row["aliveSecond"] = now.Sub(c.seen[k]).Seconds()
	}
	if !prune {
		return
	}
	prefix := nodeID + "|"
	for k := range c.seen {
		if strings.HasPrefix(k, prefix) {
			if _, ok := alive[k]; !ok {
				delete(c.seen, k)
			}
		}
	}
}

func Init() {
	for i := range config.C.Nodes {
		ApplyZLMIni(&config.C.Nodes[i])
		logger.Infor("node %s api=%s ini=%s log=%s", config.C.Nodes[i].ID, config.C.Nodes[i].API, config.C.Nodes[i].INI, config.C.Nodes[i].LogDir)
	}
	kvPath := filepath.Join(config.LogDir(), "zlm-admin.kv")
	kv, err := OpenLocalKV(kvPath)
	if err != nil {
		logger.Error("local kv init failed path=%s: %v", kvPath, err)
	}
	var audit AuditService
	if kv != nil {
		audit, err = NewAuditService(kv)
		if err != nil {
			logger.Error("operation audit init failed path=%s: %v", kvPath, err)
		}
	}
	H = &Hub{
		zlm: newZLM(), audit: audit, hooks: make([]model.HookEvent, 0, 256),
		hist: newHistory(kv), online: map[string]bool{}, versions: map[string]versionCacheEntry{},
	}
	_ = collectHost("/")
	time.Sleep(220 * time.Millisecond)
	_ = collectHost("/")
	go func() {
		time.Sleep(2 * time.Second)
		if H != nil {
			H.sweepMediaOnce()
			H.ensureAllHooks()
			H.EnsureHLSInitAll()
		}
	}()
}

func (h *Hub) nodeByID(id string) (config.Node, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range config.C.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return config.Node{}, false
}

func (h *Hub) rememberNode(n config.Node) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range config.C.Nodes {
		if config.C.Nodes[i].ID != n.ID {
			continue
		}
		dst := &config.C.Nodes[i]
		if n.API != "" {
			dst.API = n.API
		}
		if n.Secret != "" {
			dst.Secret = n.Secret
		}
		if n.HTTPPort > 0 {
			dst.HTTPPort = n.HTTPPort
		}
		if n.HTTPSPort > 0 {
			dst.HTTPSPort = n.HTTPSPort
		}
		if n.WebRTCPort > 0 {
			dst.WebRTCPort = n.WebRTCPort
		}
		if n.Root != "" {
			dst.Root = n.Root
		}
		if n.INI != "" {
			dst.INI = n.INI
		}
		if n.LogDir != "" {
			dst.LogDir = n.LogDir
		}
		if n.Bin != "" {
			dst.Bin = n.Bin
		}
		if n.WWW != "" {
			dst.WWW = n.WWW
		}
		if n.MP4Save != "" {
			dst.MP4Save = n.MP4Save
		}
		if n.HLSSave != "" {
			dst.HLSSave = n.HLSSave
		}
		return
	}
}

func (h *Hub) noteReachable(id, api string, ok bool, reason string) {
	h.mu.Lock()
	if h.online == nil {
		h.online = map[string]bool{}
	}
	prev, seen := h.online[id]
	if seen && prev == ok {
		h.mu.Unlock()
		return
	}
	h.online[id] = ok
	h.mu.Unlock()
	if ok {
		if seen {
			logger.Warnf("ZLM 节点恢复 id=%s api=%s", id, api)
		}
		return
	}
	logger.Error("ZLM 节点断开 id=%s api=%s err=%s", id, api, reason)
}

func publicNode(n config.Node) config.Node {
	n.Secret = ""
	return n
}

type ProtoShare struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type nodeOut struct {
	config.Node
	Online       bool               `json:"online"`
	Error        string             `json:"error,omitempty"`
	Streams      int                `json:"streams"`
	Sessions     int                `json:"sessions"`
	Viewers      int                `json:"viewers"`
	BytesSpeed   float64            `json:"bytes_speed"`
	InBps        float64            `json:"in_bps"`
	OutBps       float64            `json:"out_bps"`
	Recording    int                `json:"recording"`
	Waiting      int                `json:"waiting"`
	MediaSource  int                `json:"media_source"`
	Muxer        int                `json:"muxer"`
	Socket       int                `json:"socket"`
	TcpServer    int                `json:"tcp_server"`
	TcpSession   int                `json:"tcp_session"`
	UdpServer    int                `json:"udp_server"`
	UdpSession   int                `json:"udp_session"`
	Buffer       int                `json:"buffer"`
	Frame        int                `json:"frame"`
	RtpPacket    int                `json:"rtp_packet"`
	RtmpPacket   int                `json:"rtmp_packet"`
	Protocols    []ProtoShare       `json:"protocols,omitempty"`
	Statistic    map[string]any     `json:"statistic,omitempty"`
	ThreadAvg    float64            `json:"thread_avg"`
	Host         *model.HostMetrics `json:"host,omitempty"`
	HookSeen     string             `json:"hook_seen,omitempty"`
	BuildTime    string             `json:"buildTime,omitempty"`
	BranchName   string             `json:"branchName,omitempty"`
	CommitHash   string             `json:"commitHash,omitempty"`
	VersionError string             `json:"version_error,omitempty"`
}

func (h *Hub) Overview() map[string]any {
	outs := make([]nodeOut, 0, len(config.C.Nodes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	for _, n := range nodes {
		n := n
		wg.Add(1)
		go func() {
			defer wg.Done()
			snap, grouped := h.zlm.groupedLiveMedia(&n)
			h.rememberNode(n)
			h.noteReachable(n.ID, n.API, snap.Online, snap.Error)
			h.clock.observe(n.ID, snap.Sessions, true)
			out := nodeOut{
				Node:      publicNode(n),
				Online:    snap.Online,
				Error:     snap.Error,
				Streams:   len(grouped),
				Sessions:  len(snap.Sessions),
				Statistic: snap.Statistic,
				ThreadAvg: avgLoad(snap.Threads),
				HookSeen:  h.lastHook(n.ID),
			}
			applyLiveStats(&out, grouped)
			if snap.Online {
				version, err := h.cachedVersion(n)
				out.BuildTime = version.BuildTime
				out.BranchName = version.BranchName
				out.CommitHash = version.CommitHash
				if err != nil {
					out.VersionError = err.Error()
				}
			}
			if n.LocalMetrics {
				hm := collectHost("/")
				out.Host = &hm
			} else if n.MetricsURL != "" {
				if hm, err := fetchRemoteMetrics(n.MetricsURL); err == nil {
					out.Host = hm
				}
			}
			mu.Lock()
			outs = append(outs, out)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return map[string]any{
		"code": 0, "nodes": outs, "at": time.Now().Format(time.RFC3339),
		"https_port": config.C.Basic.HttpsPort, "http_port": config.C.Basic.Port,
	}
}

func (h *Hub) Nodes() map[string]any {
	h.mu.Lock()
	src := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	nodes := make([]config.Node, 0, len(src))
	for _, n := range src {
		nodes = append(nodes, publicNode(n))
	}
	return map[string]any{"code": 0, "nodes": nodes}
}

func (h *Hub) NodeAction(id, action, host string, q url.Values, body []byte) (any, int, []byte) {
	n, ok := h.nodeByID(id)
	if !ok {
		return map[string]any{"code": -1, "msg": "unknown node"}, 404, nil
	}
	if host == "" {
		host = "127.0.0.1"
	}
	switch action {
	case "", "detail":
		snap, grouped := h.zlm.groupedLiveMedia(&n)
		h.rememberNode(n)
		h.noteReachable(n.ID, n.API, snap.Online, snap.Error)
		players := h.zlm.collectPlayers(n, grouped)
		sessions := make([]map[string]any, 0, len(snap.Sessions))
		for _, s := range snap.Sessions {
			row := map[string]any{}
			for k, v := range s {
				row[k] = v
			}
			annotateSession(row, grouped, players)
			sessions = append(sessions, row)
		}
		h.clock.observe(n.ID, sessions, true)
		var hostm *model.HostMetrics
		if n.LocalMetrics {
			hm := collectHost("/")
			hostm = &hm
		}
		return map[string]any{
			"code": 0, "node": publicNode(n), "online": snap.Online, "error": snap.Error,
			"streams": grouped, "sessions": sessions, "statistic": snap.Statistic,
			"threads": snap.Threads, "work_threads": snap.WorkThreads, "host": hostm,
		}, 200, nil
	case "playurls":
		vhost, app, stream := q.Get("vhost"), q.Get("app"), q.Get("stream")
		return map[string]any{
			"code": 0, "links": playLinks(host, n, vhost, app, stream),
			"urls":        playURLs(host, n, vhost, app, stream),
			"enable_dash": DashEnabled(),
		}, 200, nil
	case "players":
		v, err := h.zlm.call(n, "getMediaPlayerList", url.Values{
			"schema": {q.Get("schema")}, "vhost": {q.Get("vhost")}, "app": {q.Get("app")}, "stream": {q.Get("stream")},
		})
		if err != nil {
			return map[string]any{"code": -1, "msg": err.Error()}, 200, nil
		}
		return v, 200, nil
	case "kick", "kick_session", "kick_sessions", "close_stream", "close_streams":
		return h.CoreOperation(id, "", action, q), 200, nil
	case "webrtc":
		publicIP := q.Get("host")
		if publicIP == "" {
			publicIP = host
		}
		typ := q.Get("type")
		if typ == "" {
			typ = "play"
		}
		data, status, err := h.zlm.webrtcPlay(n, q.Get("app"), q.Get("stream"), typ, string(body), publicIP)
		if err != nil {
			return map[string]any{"code": -1, "msg": err.Error()}, 200, nil
		}
		if status == 0 {
			status = 200
		}
		return nil, status, data
	case "stream_conns":
		return h.streamConns(n, q.Get("vhost"), q.Get("app"), q.Get("stream")), 200, nil
	case "files":
		files, roots, apps, err := listMediaFiles(n, fileListOpt{
			Kind: q.Get("kind"), Role: q.Get("role"), App: q.Get("app"), Stream: q.Get("stream"), Period: q.Get("period"),
		})
		out := map[string]any{"code": 0, "node": publicNode(n), "roots": roots, "files": files, "apps": apps}
		if err != nil {
			out["msg"] = err.Error()
		}
		return out, 200, nil
	case "records":
		return h.records(n, q, host), 200, nil
	case "is_recording":
		vhost := q.Get("vhost")
		if vhost == "" {
			vhost = "__defaultVhost__"
		}
		app, stream := q.Get("app"), q.Get("stream")
		recType, recErr := zlmRecordType(q.Get("kind"), q.Get("type"))
		if recErr != nil {
			return map[string]any{"code": -1, "msg": recErr.Error()}, 200, nil
		}
		v, err := h.zlm.call(n, "isRecording", url.Values{
			"type": {recType}, "vhost": {vhost}, "app": {app}, "stream": {stream},
		})
		if err != nil {
			return zlmCallFailure(v, err), 200, nil
		}
		return v, 200, nil
	case "config":
		return h.serverConfig(n), 200, nil
	case "set_config":
		return h.setServerConfig(n, body), 200, nil
	case "media_paths":
		return h.applyMediaPaths(id, n, body), 200, nil
	case "set_monitor":
		return h.setMonitor(id, body)
	case "dash_ensure":
		return h.EnsureDASH(id, q.Get("vhost"), q.Get("app"), q.Get("stream")), 200, nil
	case "hls_init":
		return h.EnsureHLSInit(id, q.Get("vhost"), q.Get("app"), q.Get("stream")), 200, nil
	case "snap":
		p, err := h.SnapNow(id, q.Get("app"), q.Get("stream"))
		if err != nil {
			return map[string]any{"code": -1, "msg": err.Error(), "path": p}, 200, nil
		}
		return map[string]any{"code": 0, "path": p, "msg": "已保存截图"}, 200, nil
	default:
		return map[string]any{"code": -1, "msg": "unknown action"}, 404, nil
	}
}

// zlmRecordType maps UI kind to ZLM startRecord type: 0=HLS, 1=MP4, 2=HLS-fMP4.
func zlmRecordType(kind, typ string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	t := strings.ToLower(strings.TrimSpace(typ))
	if k == "flv" || t == "flv" {
		return "", fmt.Errorf("ZLM 不支持 FLV 录制，请改用 HLS 或 MP4")
	}
	switch {
	case t == "0" || k == "hls":
		return "0", nil
	case t == "2" || k == "hls-fmp4" || k == "hls_fmp4":
		return "2", nil
	case t == "" || t == "1" || t == "mp4" || k == "" || k == "mp4":
		return "1", nil
	default:
		return "", fmt.Errorf("未知录制类型 %s", k)
	}
}

func zlmCallFailure(response map[string]any, err error) map[string]any {
	out := map[string]any{"code": -1, "msg": err.Error()}
	if response != nil {
		out["zlm_response"] = response
	}
	return out
}

func (h *Hub) records(n config.Node, q url.Values, host string) map[string]any {
	vhost := q.Get("vhost")
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	app, stream := strings.TrimSpace(q.Get("app")), strings.TrimSpace(q.Get("stream"))
	period := strings.TrimSpace(q.Get("period"))
	vals := url.Values{"vhost": {vhost}, "app": {app}, "stream": {stream}}
	if period != "" {
		vals.Set("period", period)
	}
	if p := q.Get("customized_path"); p != "" {
		vals.Set("customized_path", p)
	}
	var zlmData any
	var zlmErr string
	if app != "" && stream != "" {
		v, err := h.zlm.call(n, "getMP4RecordFile", vals)
		if err != nil {
			zlmErr = err.Error()
			if v != nil {
				zlmData = v["data"]
			}
		} else {
			zlmData = v["data"]
		}
	}
	files, roots, apps, ferr := listMediaFiles(n, fileListOpt{
		Kind: q.Get("kind"), Role: q.Get("role"), App: app, Stream: stream, Period: period,
	})
	if apps == nil {
		apps = map[string][]string{}
	}
	var lives []vodLiveStream
	if v, err := h.zlm.call(n, "getMediaList", nil); err == nil {
		for _, row := range asSlice(v["data"]) {
			a, s := asString(row["app"]), asString(row["stream"])
			if a == "" || s == "" {
				continue
			}
			apps[a] = mergeSorted(apps[a], s)
			lives = append(lives, vodLiveStream{
				Vhost:         asString(row["vhost"]),
				App:           a,
				Stream:        s,
				OriginURL:     asString(row["originUrl"]),
				OriginType:    asFloat(row["originType"]),
				OriginTypeStr: asString(row["originTypeStr"]),
			})
		}
	}
	dates := map[string]bool{}
	for _, f := range files {
		if f.Date != "" {
			dates[f.Date] = true
		}
	}
	files = mergeZlmRecordFiles(n, files, zlmData, app, stream, period)
	files = attachVODMarks(h, n, host, files, lives)
	for _, f := range files {
		if f.Date != "" {
			dates[f.Date] = true
		}
	}
	dateList := make([]string, 0, len(dates))
	for d := range dates {
		dateList = append(dateList, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dateList)))
	recording := map[string]any{}
	if app != "" && stream != "" {
		for _, item := range []struct{ key, typ string }{{"mp4", "1"}, {"hls", "0"}} {
			v, err := h.zlm.call(n, "isRecording", url.Values{
				"type": {item.typ}, "vhost": {vhost}, "app": {app}, "stream": {stream},
			})
			if err == nil && v != nil {
				recording[item.key] = zlmRecordingOn(v)
			}
		}
	}
	flags, recCfg := h.serverProtoFlags(n)
	groups := groupFileProtocols(files, flags)
	out := map[string]any{
		"code": 0, "node": publicNode(n), "zlm": zlmData, "files": files, "roots": roots,
		"apps": apps, "dates": dateList, "recording": recording,
		"flags": flags, "record_cfg": recCfg, "groups": groups,
	}
	if zlmErr != "" {
		out["zlm_error"] = zlmErr
	}
	if ferr != nil {
		out["msg"] = ferr.Error()
	}
	return out
}

func cfgFlagOn(v string) bool {
	s := strings.TrimSpace(strings.ToLower(v))
	return s == "1" || s == "true" || s == "yes" || s == "on"
}

func flattenServerConfig(v map[string]any) map[string]string {
	out := map[string]string{}
	var data any
	if v != nil {
		data = v["data"]
	}
	switch t := data.(type) {
	case []any:
		if len(t) > 0 {
			if m, ok := t[0].(map[string]any); ok {
				for k, val := range m {
					out[k] = fmt.Sprint(val)
				}
			}
		}
	case map[string]any:
		for k, val := range t {
			out[k] = fmt.Sprint(val)
		}
	}
	return out
}

func (h *Hub) serverProtoFlags(n config.Node) (map[string]bool, map[string]any) {
	flags := map[string]bool{}
	cfg := map[string]any{"mp4_max_second": 600}
	v, err := h.zlm.call(n, "getServerConfig", nil)
	if err != nil {
		return flags, cfg
	}
	flat := flattenServerConfig(v)
	flags["hls"] = cfgFlagOn(flat["protocol.enable_hls"])
	flags["hls_fmp4"] = cfgFlagOn(flat["protocol.enable_hls_fmp4"])
	flags["ts"] = cfgFlagOn(flat["protocol.enable_ts"])
	flags["fmp4"] = cfgFlagOn(flat["protocol.enable_fmp4"])
	flags["rtmp"] = cfgFlagOn(flat["protocol.enable_rtmp"])
	flags["mp4"] = cfgFlagOn(flat["protocol.enable_mp4"])
	sec := 0
	fmt.Sscanf(flat["protocol.mp4_max_second"], "%d", &sec)
	if sec <= 0 {
		sec = 600
	}
	cfg["mp4_max_second"] = sec
	mode := "segment"
	if sec >= 86400 {
		mode = "single"
	}
	cfg["mode"] = mode
	return flags, cfg
}

// ProtocolLayout controls which protocol-management panels are shown.
type ProtocolLayout struct {
	RTP, ONVIF, WebRTC bool
}

func protocolPortOn(v string, defaultOn bool) bool {
	s := strings.TrimSpace(v)
	if s == "" {
		return defaultOn
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return defaultOn
	}
	return n > 0
}

func FeatureCompileDisabled(msg string) bool {
	s := strings.ToLower(strings.TrimSpace(msg))
	return strings.Contains(s, "enable_webrtc") ||
		strings.Contains(s, "enable_onvif") ||
		strings.Contains(s, "enable_rtpproxy")
}

func ProtocolReady(enabled bool, msgs ...string) bool {
	if !enabled {
		return false
	}
	for _, m := range msgs {
		if FeatureCompileDisabled(m) {
			return false
		}
	}
	return true
}

func protocolLayoutFromConfig(flat map[string]string) ProtocolLayout {
	if flat == nil {
		flat = map[string]string{}
	}
	return ProtocolLayout{
		RTP:    protocolPortOn(flat["rtp_proxy.port"], true),
		ONVIF:  protocolPortOn(flat["onvif.port"], true),
		WebRTC: protocolPortOn(flat["rtc.signalingPort"], true) || protocolPortOn(flat["rtc.port"], false),
	}
}

func (h *Hub) ProtocolLayout(nodeID string) ProtocolLayout {
	out := ProtocolLayout{RTP: true, ONVIF: true, WebRTC: true}
	if h == nil || h.zlm == nil {
		return out
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return out
	}
	v, err := h.zlm.call(n, "getServerConfig", nil)
	if err != nil {
		return out
	}
	return protocolLayoutFromConfig(flattenServerConfig(v))
}

func copyMediaFiles(in []MediaFile) []MediaFile {
	if len(in) == 0 {
		return []MediaFile{}
	}
	out := make([]MediaFile, len(in))
	copy(out, in)
	return out
}

func sortGroupFiles(files []MediaFile) []MediaFile {
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].Head != files[j].Head {
			return files[i].Head < files[j].Head
		}
		return files[i].ModTime > files[j].ModTime
	})
	return files
}

func groupFileProtocols(files []MediaFile, flags map[string]bool) []map[string]any {
	by := map[string][]MediaFile{}
	for _, f := range files {
		id := f.Proto
		if id == "" {
			id = "other"
		}
		by[id] = append(by[id], f)
		if id == "hls" && f.Ext == ".ts" {
			by["ts"] = append(by["ts"], f)
		}
	}
	type spec struct {
		id, label, off, none string
		on                   bool
	}
	specs := []spec{
		{"hls", "HLS", "未开启 protocol.enable_hls / enable_hls_fmp4", "暂无 m3u8 / ts / m4s 切片（流未在播或尚未生成）", flags["hls"] || flags["hls_fmp4"]},
		{"dash", "DASH", "ZLM 默认不产出 DASH，请用播放页 FFmpeg 命令生成", "暂无 DASH 的 mpd / m4s 分片", true},
		{"ts", "HTTP-TS", "未开启 protocol.enable_ts", "实时 HTTP-TS 不单独落盘；这里列出磁盘上的 ts 切片", flags["ts"]},
		{"fmp4", "HTTP-MP4", "未开启 protocol.enable_fmp4", "实时 HTTP-fMP4 一般不落盘。HLS-fMP4 请看 hls，DASH 分片请看 dash", flags["fmp4"]},
		{"record", "录制", "", "暂无 MP4 录制文件（HLS 录制请切到 hls）", true},
		{"snap", "截图", "", "暂无定时截图", true},
	}
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		list := sortGroupFiles(copyMediaFiles(by[s.id]))
		note := ""
		if !s.on && len(list) == 0 {
			note = s.off
		} else if !s.on && len(list) > 0 {
			note = "协议已关闭，以下为磁盘残留"
		} else if len(list) == 0 {
			note = s.none
		}
		out = append(out, map[string]any{
			"id": s.id, "label": s.label, "enabled": s.on, "note": note, "files": list,
		})
	}
	return out
}

func mergeSorted(list []string, item string) []string {
	for _, s := range list {
		if s == item {
			return list
		}
	}
	list = append(list, item)
	sort.Strings(list)
	return list
}

func mergeZlmRecordFiles(n config.Node, files []MediaFile, zlmData any, app, stream, period string) []MediaFile {
	m, ok := zlmData.(map[string]any)
	if !ok || m == nil {
		return files
	}
	rootPath := asString(m["rootPath"])
	raw := m["paths"]
	if raw == nil {
		raw = m["files"]
	}
	var names []string
	switch t := raw.(type) {
	case []any:
		for _, x := range t {
			names = append(names, asString(x))
		}
	case map[string]any:
		for k := range t {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		return files
	}
	seen := map[string]bool{}
	for _, f := range files {
		seen[f.Path] = true
		seen[f.Name] = true
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || isDateFolder(name) {
			continue
		}
		abs := name
		if rootPath != "" && !filepath.IsAbs(name) {
			abs = filepath.Join(rootPath, name)
		}
		rel := relToNode(n, abs)
		if seen[rel] || seen[filepath.Base(name)] {
			continue
		}
		seen[rel] = true
		ext := strings.ToLower(filepath.Ext(name))
		if ext == "" {
			ext = ".mp4"
		}
		role, place, a, s, date, proto := classifyRel(rel, filepath.Base(name), ext)
		if a == "" {
			a = app
		}
		if s == "" {
			s = stream
		}
		if date == "" {
			date = period
		}
		st, err := os.Stat(abs)
		size := int64(0)
		mtime := ""
		if err == nil {
			size = st.Size()
			mtime = st.ModTime().Format(time.RFC3339)
		}
		files = append(files, MediaFile{
			Path: rel, Name: filepath.Base(name), Dir: filepath.ToSlash(filepath.Dir(rel)),
			Ext: ext, Size: size, ModTime: mtime, Kind: fileKind(ext),
			Role: role, Place: place, App: a, Stream: s, Date: date,
			Proto: proto, Head: fileHeadRank(filepath.Base(name), ext),
			DurationSec: fileDurationSec(abs, ext),
		})
	}
	fillMissingDurations(files)
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime > files[j].ModTime })
	return files
}

func groupServerConfig(v map[string]any) []map[string]any {
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
	if flat == nil {
		return nil
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type grp struct {
		name string
		rows []map[string]string
	}
	groups := []grp{}
	idx := map[string]int{}
	for _, k := range keys {
		cat, name := k, k
		if i := strings.Index(k, "."); i > 0 {
			cat, name = k[:i], k[i+1:]
		}
		gi, ok := idx[cat]
		if !ok {
			gi = len(groups)
			idx[cat] = gi
			groups = append(groups, grp{name: cat})
		}
		groups[gi].rows = append(groups[gi].rows, map[string]string{
			"key": k, "name": name, "value": fmt.Sprint(flat[k]),
		})
	}
	out := make([]map[string]any, 0, len(groups))
	for _, g := range groups {
		out = append(out, map[string]any{"section": g.name, "items": g.rows})
	}
	return out
}

func (h *Hub) serverConfig(n config.Node) map[string]any {
	v, err := h.zlm.call(n, "getServerConfig", nil)
	if err != nil {
		return map[string]any{"code": -1, "msg": err.Error(), "node": publicNode(n)}
	}
	return map[string]any{
		"code": 0, "node": publicNode(n), "groups": groupServerConfig(v), "raw": v["data"],
	}
}

func configNeedsRestart(k string) bool {
	lk := strings.ToLower(k)
	return strings.Contains(lk, ".port") || strings.HasSuffix(lk, "sslport") ||
		strings.Contains(lk, "listen_ip") || strings.HasSuffix(lk, ".sockport") ||
		strings.HasSuffix(lk, ".tcpport")
}

func (h *Hub) refreshNodeIni(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range config.C.Nodes {
		if config.C.Nodes[i].ID == id {
			config.C.Nodes[i].WWW, config.C.Nodes[i].MP4Save, config.C.Nodes[i].HLSSave = "", "", ""
			ApplyZLMIni(&config.C.Nodes[i])
			return
		}
	}
}

func (h *Hub) setServerConfig(n config.Node, body []byte) map[string]any {
	var kv map[string]string
	if err := json.Unmarshal(body, &kv); err != nil {
		return map[string]any{"code": -1, "msg": "JSON 须为 {key:value}"}
	}
	issues := ValidateZLMConfig(kv)
	if HasFatalCfgIssue(issues) {
		return map[string]any{"code": -1, "msg": FormatCfgIssues(issues), "issues": issues}
	}
	vals := url.Values{}
	restart := make([]string, 0)
	for k, v := range kv {
		k = strings.TrimSpace(k)
		if k == "" || k == "secret" || k == "api.secret" {
			continue
		}
		vals.Set(k, v)
		if configNeedsRestart(k) {
			restart = append(restart, k)
		}
	}
	if len(vals) == 0 {
		return map[string]any{"code": -1, "msg": "没有可保存的配置项"}
	}
	ret, err := h.zlm.callPOST(n, "setServerConfig", vals)
	if err != nil {
		return zlmCallFailure(ret, err)
	}
	if ret == nil {
		ret = map[string]any{}
	}
	sort.Strings(restart)
	ret["changed"] = len(vals)
	ret["restart_keys"] = restart
	if warns := issueMsgs(issues, false); len(warns) > 0 {
		ret["warnings"] = warns
	}
	h.refreshNodeIni(n.ID)
	if n2, ok := h.nodeByID(n.ID); ok {
		ret["node"] = publicNode(n2)
	} else {
		ret["node"] = publicNode(n)
	}
	return ret
}

func (h *Hub) applyMediaPaths(id string, n config.Node, body []byte) map[string]any {
	var req struct {
		Base        string `json:"base"`
		EnableVhost *bool  `json:"enable_vhost"`
		LiveKeepSec *int   `json:"live_keep_sec"`
	}
	_ = json.Unmarshal(body, &req)
	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = "/data/zlm"
	}
	if !filepath.IsAbs(base) {
		return map[string]any{"code": -1, "msg": "落盘根目录必须是绝对路径"}
	}
	keep := defaultLiveKeepSec
	if req.LiveKeepSec != nil {
		keep = ClampLiveKeepSec(*req.LiveKeepSec)
	} else if n.LiveKeepSec > 0 {
		keep = ClampLiveKeepSec(n.LiveKeepSec)
	}
	created := make([]string, 0, 2)
	for _, s := range []string{"mp4", "snap"} {
		d := filepath.Join(base, s)
		if err := os.MkdirAll(d, 0755); err != nil {
			return map[string]any{"code": -1, "msg": "创建目录失败 " + d + ": " + err.Error()}
		}
		created = append(created, d)
	}
	vhost := "0"
	if req.EnableVhost != nil {
		if *req.EnableVhost {
			vhost = "1"
		}
	} else if n.EnableVhost {
		vhost = "1"
	}
	removed := CleanUnusedMediaDirs(base, vhost == "1")
	kv := map[string]string{
		"http.rootPath":          base,
		"http.dirMenu":           "1",
		"api.downloadRoot":       base,
		"api.snapRoot":           filepath.ToSlash(filepath.Join(base, "snap", ".cache")) + "/",
		"protocol.hls_save_path": base,
		"protocol.mp4_save_path": filepath.Join(base, "mp4"),
		"general.enableVhost":    vhost,
		"hls.deleteDelaySec":     strconv.Itoa(keep),
		"hls.segKeep":            "0",
		"record.fastStart":       "1",
	}
	raw, _ := json.Marshal(kv)
	out := h.setServerConfig(n, raw)
	h.setNodeEnableVhost(id, vhost == "1")
	h.setNodeLiveKeep(id, keep)
	out["dirs"] = created
	out["removed"] = removed
	out["applied"] = kv
	out["note"] = "8090 根目录即 " + base + "。关闭虚拟主机会清掉 __defaultVhost__；live/ 是播放切片（" + strconv.Itoa(keep) + " 秒后清），正式录像在 " + filepath.Join(base, "mp4")
	logger.Infor("media_paths id=%s base=%s hls=%s mp4=%s http.rootPath=%s enableVhost=%s liveKeep=%d removed=%v", id, base, kv["protocol.hls_save_path"], kv["protocol.mp4_save_path"], kv["http.rootPath"], vhost, keep, removed)
	return out
}

func (h *Hub) setNodeEnableVhost(id string, on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if config.C == nil {
		return
	}
	for i := range config.C.Nodes {
		if config.C.Nodes[i].ID == id {
			config.C.Nodes[i].EnableVhost = on
			return
		}
	}
}

func (h *Hub) setNodeLiveKeep(id string, sec int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if config.C == nil {
		return
	}
	sec = ClampLiveKeepSec(sec)
	for i := range config.C.Nodes {
		if config.C.Nodes[i].ID == id {
			config.C.Nodes[i].LiveKeepSec = sec
			return
		}
	}
}

func (h *Hub) setMonitor(id string, body []byte) (any, int, []byte) {
	var req struct {
		Root    string `json:"root"`
		API     string `json:"api"`
		INI     string `json:"ini"`
		LogDir  string `json:"log_dir"`
		Bin     string `json:"bin"`
		Persist bool   `json:"persist"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return map[string]any{"code": -1, "msg": "JSON 无效: " + err.Error()}, 400, nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var n *config.Node
	for i := range config.C.Nodes {
		if config.C.Nodes[i].ID == id {
			n = &config.C.Nodes[i]
			break
		}
	}
	if n == nil {
		return map[string]any{"code": -1, "msg": "unknown node"}, 404, nil
	}
	if req.Root != "" {
		n.Root = filepath.Clean(req.Root)
	}
	if req.Bin != "" {
		n.Bin = filepath.Clean(req.Bin)
	}
	if n.Root == "" && n.Bin != "" {
		n.Root = filepath.Dir(n.Bin)
	}
	inRoot := func(p string) bool {
		if p == "" || n.Root == "" {
			return false
		}
		p = filepath.Clean(p)
		return insideRoot(n.Root, p) || p == n.Root
	}
	if req.INI != "" && inRoot(req.INI) {
		n.INI = req.INI
	} else if n.Root != "" {
		n.INI = filepath.Join(n.Root, "config.ini")
	}
	if req.LogDir != "" && inRoot(req.LogDir) {
		n.LogDir = req.LogDir
	} else if n.Root != "" {
		n.LogDir = filepath.Join(n.Root, "log")
	}
	if req.Bin != "" && inRoot(req.Bin) {
		n.Bin = req.Bin
	} else if n.Root != "" {
		n.Bin = filepath.Join(n.Root, "MediaServer")
	}
	n.WWW, n.MP4Save, n.HLSSave = "", "", ""
	ApplyZLMIni(n)
	if req.API != "" {
		n.API = strings.TrimRight(req.API, "/")
	}
	warns := []string{}
	if n.Bin != "" {
		if st, err := os.Stat(n.Bin); err != nil {
			warns = append(warns, "二进制不存在: "+n.Bin)
		} else if st.IsDir() {
			warns = append(warns, "bin 指向目录而非 MediaServer")
		}
	}
	if n.INI != "" {
		if _, err := os.Stat(n.INI); err != nil {
			warns = append(warns, "ini 不存在: "+n.INI)
		}
	}
	var persistErr string
	if req.Persist {
		if err := config.Save(); err != nil {
			persistErr = err.Error()
		}
	}
	logger.Infor("set_monitor id=%s root=%s api=%s ini=%s bin=%s persist=%v", n.ID, n.Root, n.API, n.INI, n.Bin, req.Persist)
	out := map[string]any{"code": 0, "node": publicNode(*n), "warns": warns}
	if persistErr != "" {
		out["persist_error"] = persistErr
	}
	return out, 200, nil
}

func (h *Hub) kickSession(n config.Node, id string) map[string]any {
	id = strings.TrimSpace(id)
	if id == "" {
		return map[string]any{"code": -1, "msg": "缺少会话 id，无法踢掉"}
	}
	try := func(sid string) error {
		_, err := h.zlm.callPOST(n, "kick_session", url.Values{"id": {sid}})
		return err
	}
	firstErr := try(id)
	if firstErr == nil {
		logger.Infor("kick 会话 node=%s id=%s 已踢掉", n.ID, id)
		return map[string]any{"code": 0, "msg": "已踢掉"}
	}
	snap := h.zlm.fetchAll(&n)
	for _, s := range snap.Sessions {
		sid := asString(s["id"])
		ident := asString(s["identifier"])
		if sid == "" {
			sid = ident
		}
		if sid != id && ident != id {
			continue
		}
		for _, cand := range []string{sid, ident} {
			if cand == "" || cand == id {
				continue
			}
			if err := try(cand); err == nil {
				logger.Infor("kick 会话 node=%s id=%s 已踢掉 stream=%s", n.ID, id, sessionBrief(s))
				return map[string]any{"code": 0, "msg": "已踢掉"}
			}
		}
	}
	logger.Warnf("kick 会话失败 node=%s id=%s err=%v", n.ID, id, firstErr)
	return map[string]any{"code": -1, "msg": "踢掉失败: " + firstErr.Error()}
}

// sessionBrief 提取会话关键信息（app/stream、对端地址），便于日志定位被踢的是哪个链接。
func sessionBrief(s map[string]any) string {
	app, stream := asString(s["app"]), asString(s["stream"])
	peer := asString(s["peer_ip"])
	if p := asString(s["peer_port"]); p != "" && p != "0" {
		peer += ":" + p
	}
	parts := []string{}
	if app != "" && stream != "" {
		parts = append(parts, app+"/"+stream)
	}
	if peer != "" {
		parts = append(parts, "peer="+peer)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return ""
}

func (h *Hub) streamConns(n config.Node, vhost, app, stream string) map[string]any {
	if vhost == "" {
		vhost = "__defaultVhost__"
	}
	snap, grouped := h.zlm.groupedLiveMedia(&n)
	h.rememberNode(n)
	h.noteReachable(n.ID, n.API, snap.Online, snap.Error)
	var media map[string]any
	for _, g := range grouped {
		if asString(g["app"]) == app && asString(g["stream"]) == stream && (vhost == "" || vhost == "__defaultVhost__" || asString(g["vhost"]) == vhost) {
			media = g
			break
		}
	}
	playerIndex := h.zlm.collectPlayers(n, grouped)
	pubs := make([]map[string]any, 0)
	related := make([]map[string]any, 0)
	for _, s := range snap.Sessions {
		row := map[string]any{}
		for k, v := range s {
			row[k] = v
		}
		annotateSession(row, grouped, playerIndex)
		match := asString(row["app"]) == app && asString(row["stream"]) == stream
		if pub, _ := row["is_publisher"].(bool); pub && match {
			pubs = append(pubs, row)
			continue
		}
		if match {
			related = append(related, row)
		}
	}
	players := make([]map[string]any, 0)
	schemas := []string{}
	if media != nil {
		if arr, ok := media["schemas"].([]string); ok {
			schemas = arr
		}
	}
	if len(schemas) == 0 {
		schemas = []string{"rtmp", "rtsp", "http", "ts", "fmp4", "hls", "rtc"}
	}
	mediaSchema := ""
	if media != nil {
		mediaSchema = asString(media["origin_schema"])
	}
	if mediaSchema == "" && len(schemas) > 0 {
		mediaSchema = schemas[0]
	}
	var mediaOnline bool
	var mediaOnlineKnown bool
	var mediaInfo any
	mediaErrs := make([]string, 0, 2)
	if mediaSchema == "" {
		mediaErrs = append(mediaErrs, "未找到可用于查询流详情的 schema")
	} else {
		vals := url.Values{"schema": {mediaSchema}, "vhost": {vhost}, "app": {app}, "stream": {stream}}
		if v, err := h.zlm.call(n, "isMediaOnline", vals); err != nil {
			mediaErrs = append(mediaErrs, "在线状态不可用: "+err.Error())
		} else {
			if online, ok := v["online"]; ok {
				mediaOnline = asTruthy(online)
				mediaOnlineKnown = true
			} else {
				mediaErrs = append(mediaErrs, "在线状态响应缺少 online")
			}
		}
		if v, err := h.zlm.call(n, "getMediaInfo", vals); err != nil {
			mediaErrs = append(mediaErrs, "媒体信息不可用: "+err.Error())
		} else {
			info := make(map[string]any, len(v))
			for key, value := range v {
				if key != "code" && key != "msg" {
					info[key] = value
				}
			}
			mediaInfo = info
		}
	}
	for _, sch := range schemas {
		v, err := h.zlm.call(n, "getMediaPlayerList", url.Values{"schema": {sch}, "vhost": {vhost}, "app": {app}, "stream": {stream}})
		if err != nil {
			continue
		}
		for _, p := range asSlice(v["data"]) {
			p["schema"] = sch
			p["role"] = "拉流"
			p["note"] = "通过 " + sch + " 协议正在拉流播放"
			p["name"] = sessionName(p)
			players = append(players, p)
		}
	}
	all := make([]map[string]any, 0, len(snap.Sessions)+len(pubs)+len(players)+len(related))
	all = append(all, snap.Sessions...)
	all = append(all, pubs...)
	all = append(all, players...)
	all = append(all, related...)
	h.clock.observe(n.ID, all, true)
	out := map[string]any{
		"code": 0, "media": media, "publishers": pubs, "players": players, "sessions": related,
		"online": snap.Online, "error": snap.Error, "media_online": mediaOnline,
		"media_online_known": mediaOnlineKnown, "media_info": mediaInfo,
	}
	if len(mediaErrs) > 0 {
		out["media_error"] = strings.Join(mediaErrs, "；")
	}
	return out
}

func (h *Hub) Events() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	evs := append([]model.HookEvent(nil), h.hooks...)
	seen := map[string]struct{}{}
	for _, name := range hookEventNames {
		seen[name] = struct{}{}
	}
	for _, e := range evs {
		if e.Event != "" {
			seen[e.Event] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for k := range seen {
		names = append(names, k)
	}
	sort.Strings(names)
	return map[string]any{"code": 0, "events": evs, "names": names}
}

func (h *Hub) Logs(nodeID, file, lv string, maxLines int) map[string]any {
	if nodeID == "" {
		nodeID = "zlm-1"
	}
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return map[string]any{"code": -1, "msg": "unknown node"}
	}
	dir := n.LogDir
	if dir == "" {
		return map[string]any{"code": -1, "msg": "未配置 log_dir"}
	}
	files, err := listLogFiles(dir)
	if err != nil {
		return map[string]any{"code": -1, "msg": err.Error(), "dir": dir}
	}
	p, err := safeLogPath(dir, file)
	if err != nil {
		return map[string]any{"code": 0, "files": files, "lines": []string{}, "offset": int64(0), "msg": err.Error()}
	}
	lines, size, err := readTailLines(p, logSnapMaxBytes, maxLines, lv)
	if err != nil {
		return map[string]any{"code": -1, "files": files, "msg": err.Error(), "dir": dir}
	}
	return map[string]any{
		"code": 0, "dir": dir, "file": filepath.Base(p), "files": files,
		"lines": lines, "offset": size, "size": size,
	}
}

func (h *Hub) LogStream(ctx context.Context, w http.ResponseWriter, nodeID, file, lv string, offset int64) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("stream unsupported")
	}
	if nodeID == "" {
		nodeID = "zlm-1"
	}
	n, found := h.nodeByID(nodeID)
	if !found || n.LogDir == "" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = writeSSE(w, "logerr", map[string]any{"msg": "unknown node or empty log_dir"})
		flusher.Flush()
		return nil
	}
	p, err := safeLogPath(n.LogDir, file)
	if err != nil {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = writeSSE(w, "logerr", map[string]any{"msg": err.Error()})
		flusher.Flush()
		return nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lineCh := make(chan string, 256)
	errCh := make(chan error, 1)
	go func() {
		errCh <- followLog(ctx, p, offset, lv, func(line string) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case lineCh <- line:
				return nil
			}
		})
	}()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err != nil && ctx.Err() == nil {
				_ = writeSSE(w, "logerr", map[string]any{"msg": err.Error()})
				flusher.Flush()
			}
			return err
		case line := <-lineCh:
			if err := writeSSE(w, "message", map[string]string{"line": line}); err != nil {
				return err
			}
			flusher.Flush()
		case <-ping.C:
			if err := writeSSEPing(w); err != nil {
				return err
			}
			flusher.Flush()
		}
	}
}

func (h *Hub) LocalMetrics() model.HostMetrics { return collectHost("/") }

func (h *Hub) Hook(event string, raw []byte) map[string]any {
	event = strings.Trim(strings.TrimSpace(event), "/")
	var body map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	if body == nil {
		body = map[string]any{}
	}
	server := asString(body["mediaServerId"])
	logHook(event, body)
	if hookShouldStore(event) {
		h.mu.Lock()
		h.hooks = append([]model.HookEvent{{Time: time.Now().Format("15:04:05"), Event: event, Server: server, Body: body}}, h.hooks...)
		if len(h.hooks) > 400 {
			h.hooks = h.hooks[:400]
		}
		h.mu.Unlock()
	}
	nodeID := h.hookNodeID(server)
	sid := asString(body["id"])
	switch event {
	case "on_play", "on_publish":
		h.clock.mark(nodeID, sid, time.Now())
		if event == "on_publish" && DashEnabled() {
			go h.EnsureDASH(nodeID, asString(body["vhost"]), asString(body["app"]), asString(body["stream"]))
		}
	case "on_flow_report":
		h.clock.forget(nodeID, sid)
	case "on_stream_changed":
		if asTruthy(body["regist"]) {
			go h.EnsureHLSInit(nodeID, asString(body["vhost"]), asString(body["app"]), asString(body["stream"]))
			if DashEnabled() {
				go h.EnsureDASH(nodeID, asString(body["vhost"]), asString(body["app"]), asString(body["stream"]))
			}
		}
	case "on_server_exited":
		h.noteReachable(nodeID, "", false, "on_server_exited")
	case "on_server_started":
		h.noteReachable(nodeID, "", true, "on_server_started")
		go h.ensureAllHooks()
	}
	return hookReply(event)
}

func (h *Hub) hookNodeID(server string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(config.C.Nodes) == 1 {
		return config.C.Nodes[0].ID
	}
	for _, n := range config.C.Nodes {
		if n.ID == server || n.Name == server {
			return n.ID
		}
	}
	if len(config.C.Nodes) > 0 {
		return config.C.Nodes[0].ID
	}
	return "default"
}

func (h *Hub) lastHook(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.hooks {
		if e.Server == id || e.Server == "" {
			return e.Time + " " + e.Event
		}
	}
	return ""
}

func avgLoad(threads []map[string]any) float64 {
	if len(threads) == 0 {
		return 0
	}
	var s float64
	for _, t := range threads {
		s += asFloat(t["load"])
	}
	return s / float64(len(threads))
}

func fetchRemoteMetrics(u string) (*model.HostMetrics, error) {
	resp, err := http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var hm model.HostMetrics
	if err := json.NewDecoder(resp.Body).Decode(&hm); err != nil {
		return nil, err
	}
	return &hm, nil
}

func (h *Hub) SampleLoop() {
	h.sampleOnce()
	tick := time.NewTicker(15 * time.Second)
	save := time.NewTicker(60 * time.Second)
	defer tick.Stop()
	defer save.Stop()
	for {
		select {
		case <-tick.C:
			h.sampleOnce()
		case <-save.C:
			if h.hist != nil {
				h.hist.save()
			}
			h.sweepMediaOnce()
		}
	}
}

func (h *Hub) sampleOnce() {
	if h.hist == nil {
		return
	}
	h.mu.Lock()
	nodes := append([]config.Node(nil), config.C.Nodes...)
	h.mu.Unlock()
	push, pull, conn := 0, 0, 0
	var inBps, outBps float64
	for _, n := range nodes {
		n := n
		snap := h.zlm.fetchAll(&n)
		h.clock.observe(n.ID, snap.Sessions, true)
		h.noteReachable(n.ID, n.API, snap.Online, snap.Error)
		grouped := groupMedia(snap.Media)
		push += len(grouped)
		conn += len(snap.Sessions)
		for _, g := range grouped {
			pull += int(asFloat(g["totalReaderCount"]))
			inBps += asFloat(g["in_bps"])
			outBps += asFloat(g["out_bps"])
		}
	}
	hm := collectHost("/")
	h.hist.add(model.MetricSample{
		T: time.Now().Unix(), Push: push, Pull: pull, Conn: conn,
		CPU: hm.CPUPercent, Mem: hm.MemPercent, NetUtil: hm.NetUtilPct,
		NetRxBps: hm.NetRxBps, NetTxBps: hm.NetTxBps,
		InBps: uint64(inBps), OutBps: uint64(outBps),
	})
}

func (h *Hub) History(rng string) map[string]any {
	from, to, step, rng := historyWindow(rng, time.Now())
	var samples []model.MetricSample
	if h != nil && h.hist != nil {
		samples = h.hist.queryFilled(from, to, step)
	} else {
		samples = fillMetricGrid(from, to, step, nil)
	}
	return map[string]any{"code": 0, "range": rng, "from": from.Unix(), "to": to.Unix(), "points": samples}
}

func applyLiveStats(out *nodeOut, grouped []map[string]any) {
	if out == nil {
		return
	}
	counts := map[string]int{}
	for _, g := range grouped {
		out.Viewers += int(asFloat(g["totalReaderCount"]))
		out.InBps += asFloat(g["in_bps"])
		out.OutBps += asFloat(g["out_bps"])
		out.BytesSpeed += asFloat(g["bytesSpeed"])
		if asTruthy(g["isRecordingHLS"]) || asTruthy(g["isRecordingMP4"]) {
			out.Recording++
		}
		if asString(g["status"]) == "wait" {
			out.Waiting++
		}
		schemas, _ := g["schemas"].([]string)
		for _, schema := range schemas {
			schema = strings.TrimSpace(schema)
			if schema != "" {
				counts[schema]++
			}
		}
	}
	out.Protocols = SortedProtoShares(counts)
	stat := out.Statistic
	out.MediaSource = statCount(stat, "MediaSource")
	out.Muxer = statCount(stat, "MultiMediaSourceMuxer")
	out.Socket = statCount(stat, "Socket")
	out.TcpServer = statCount(stat, "TcpServer")
	out.TcpSession = statCount(stat, "TcpSession")
	out.UdpServer = statCount(stat, "UdpServer")
	out.UdpSession = statCount(stat, "UdpSession")
	out.Buffer = statCount(stat, "Buffer")
	out.Frame = statCount(stat, "Frame")
	out.RtpPacket = statCount(stat, "RtpPacket")
	out.RtmpPacket = statCount(stat, "RtmpPacket")
}

func statCount(m map[string]any, key string) int {
	if len(m) == 0 || key == "" {
		return 0
	}
	if v, ok := m[key]; ok {
		return int(asFloat(v))
	}
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return int(asFloat(v))
		}
	}
	return 0
}

func SortedProtoShares(counts map[string]int) []ProtoShare {
	out := make([]ProtoShare, 0, len(counts))
	for name, n := range counts {
		out = append(out, ProtoShare{Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
