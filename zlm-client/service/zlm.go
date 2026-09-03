package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"zlm-admin/core/config"
)

type zlmClient struct {
	http *http.Client
}

func newZLM() *zlmClient {
	return &zlmClient{http: &http.Client{Timeout: 4 * time.Second}}
}

var zlmSecretQueryPattern = regexp.MustCompile(`(?i)[?&]?secret=[^&\s"'<>]*`)

type sanitizedZLMError struct {
	message string
	cause   error
}

func (e sanitizedZLMError) Error() string { return e.message }
func (e sanitizedZLMError) Unwrap() error { return e.cause }

func sanitizeZLMTransportError(node config.Node, err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	message := err.Error()
	if node.Secret != "" {
		message = strings.ReplaceAll(message, node.Secret, "[REDACTED]")
	}
	message = zlmSecretQueryPattern.ReplaceAllString(message, "[REDACTED]")
	var cause error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		cause = context.Canceled
	}
	return sanitizedZLMError{message: message, cause: cause}
}

func (c *zlmClient) call(node config.Node, api string, extra url.Values) (map[string]any, error) {
	u, err := url.Parse(strings.TrimRight(node.API, "/") + "/index/api/" + api)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("secret", node.Secret)
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	defer resp.Body.Close()
	out, err := parseZLMResponse(node, api, resp)
	return out, sanitizeZLMTransportError(node, err)
}

func parseZLMResponse(node config.Node, api string, resp *http.Response) (map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	trim := bytes.TrimSpace(body)
	if len(trim) == 0 {
		return nil, fmt.Errorf("%s: 空响应 HTTP %d (api=%s)", api, resp.StatusCode, node.API)
	}
	if trim[0] == '<' || resp.StatusCode >= 400 && !json.Valid(trim) {
		kind := "非 JSON"
		low := strings.ToLower(string(trim[:min(len(trim), 200)]))
		if strings.Contains(low, "nginx") || strings.Contains(low, "bad gateway") {
			kind = "nginx/网关页面"
		} else if strings.Contains(low, "html") {
			kind = "HTML"
		}
		return nil, fmt.Errorf("%s: %s HTTP %d，不是 ZLM API (api=%s)", api, kind, resp.StatusCode, node.API)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%s: %w (api=%s)", api, err, node.API)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return out, fmt.Errorf("%s: HTTP %d (api=%s)", api, resp.StatusCode, node.API)
	}
	if code := asFloat(out["code"]); code != 0 {
		msg := asString(out["msg"])
		if msg == "" {
			msg = asString(out["message"])
		}
		return out, fmt.Errorf("%s: zlm code=%v %s (api=%s)", api, int(code), msg, node.API)
	}
	return out, nil
}

func (c *zlmClient) probeAPI(node *config.Node) error {
	ApplyZLMIni(node)
	configured := strings.TrimRight(node.API, "/")
	playPort := node.HTTPPort
	if playPort <= 0 {
		playPort = 8090
	}
	bases := make([]string, 0, 4)
	if configured != "" {
		bases = append(bases, configured)
	}
	bases = append(bases, fmt.Sprintf("http://127.0.0.1:%d", playPort))
	tried := make([]string, 0, len(bases))
	seen := map[string]bool{}
	var lastErr error
	for _, base := range bases {
		if seen[base] {
			continue
		}
		seen[base] = true
		tried = append(tried, base)
		node.API = base
		if _, err := c.call(*node, "getApiList", nil); err == nil {
			node.HTTPPort = playPort
			return nil
		} else {
			lastErr = err
		}
	}
	if configured != "" {
		node.API = configured
	}
	node.HTTPPort = playPort
	if lastErr == nil {
		lastErr = fmt.Errorf("未配置可用 API 地址")
	}
	return fmt.Errorf("无法访问 ZLM API（已试 %s）: %v", strings.Join(tried, ", "), lastErr)
}

func (c *zlmClient) fetchAll(node *config.Node) nodeSnapshot {
	if err := c.probeAPI(node); err != nil {
		return nodeSnapshot{ID: node.ID, Name: node.Name, API: node.API, At: time.Now(), Error: err.Error()}
	}
	snap := nodeSnapshot{ID: node.ID, Name: node.Name, API: node.API, At: time.Now()}
	type named struct {
		key string
		fn  func() (map[string]any, error)
	}
	jobs := []named{
		{"media", func() (map[string]any, error) { return c.call(*node, "getMediaList", nil) }},
		{"sessions", func() (map[string]any, error) { return c.call(*node, "getAllSession", nil) }},
		{"statistic", func() (map[string]any, error) { return c.call(*node, "getStatistic", nil) }},
		{"threads", func() (map[string]any, error) { return c.call(*node, "getThreadsLoad", nil) }},
		{"work_threads", func() (map[string]any, error) { return c.call(*node, "getWorkThreadsLoad", nil) }},
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := make([]string, 0)
	for _, j := range jobs {
		j := j
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := j.fn()
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, j.key+": "+err.Error())
				snap.Online = false
				return
			}
			switch j.key {
			case "media":
				snap.Media = asSlice(v["data"])
			case "sessions":
				snap.Sessions = asSlice(v["data"])
			case "statistic":
				if m, ok := v["data"].(map[string]any); ok {
					snap.Statistic = m
				}
			case "threads":
				snap.Threads = asSlice(v["data"])
			case "work_threads":
				snap.WorkThreads = asSlice(v["data"])
			}
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		snap.Online = true
	} else {
		snap.Error = strings.Join(errs, "; ")
		// still online if at least media or statistic succeeded
		if snap.Statistic != nil || len(snap.Media) > 0 || len(snap.Sessions) > 0 {
			snap.Online = true
		}
	}
	return snap
}

type nodeSnapshot struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	API         string           `json:"api"`
	Online      bool             `json:"online"`
	Error       string           `json:"error,omitempty"`
	At          time.Time        `json:"at"`
	Media       []map[string]any `json:"media"`
	Sessions    []map[string]any `json:"sessions"`
	Statistic   map[string]any   `json:"statistic"`
	Threads     []map[string]any `json:"threads"`
	WorkThreads []map[string]any `json:"work_threads"`
}

func asSlice(v any) []map[string]any {
	switch arr := v.(type) {
	case []map[string]any:
		return arr
	case []any:
		out := make([]map[string]any, 0, len(arr))
		for _, it := range arr {
			if m, ok := it.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	default:
		return nil
	}
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	case json.Number:
		return t.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprintf("%v", v)
	}
}

func asTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case json.Number:
		n, _ := t.Int64()
		return n != 0
	case string:
		s := strings.TrimSpace(strings.ToLower(t))
		return s == "1" || s == "true" || s == "yes"
	default:
		return false
	}
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, _ := t.Float64()
		return f
	case string:
		var f float64
		fmt.Sscanf(t, "%f", &f)
		return f
	default:
		return 0
	}
}

func prettyCodec(name string) string {
	switch strings.ToLower(name) {
	case "mpeg4-generic", "mpeg4_generic", "mp4a":
		return "AAC"
	case "h264":
		return "H264"
	case "h265", "hevc":
		return "H265"
	case "aac":
		return "AAC"
	case "opus":
		return "Opus"
	case "g711a", "pcma":
		return "G711A"
	case "g711u", "pcmu":
		return "G711U"
	case "vp8":
		return "VP8"
	case "vp9":
		return "VP9"
	case "av1":
		return "AV1"
	default:
		if name == "" {
			return "-"
		}
		return name
	}
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && asString(v) != "" {
			return v
		}
	}
	return nil
}

func trackCodecName(t map[string]any) string {
	if name := asString(firstPresent(t, "codec_id_name", "codecIdName")); name != "" {
		return name
	}
	switch int(asFloat(firstPresent(t, "codec_id", "codecId"))) {
	case 0:
		return "H264"
	case 1:
		return "H265"
	case 2:
		return "AAC"
	case 3:
		return "G711A"
	case 4:
		return "G711U"
	case 5:
		return "Opus"
	default:
		return ""
	}
}

func applyTracks(g map[string]any, tracks any) {
	ready := true
	hasTrack := false
	for _, t := range asSlice(tracks) {
		hasTrack = true
		if b, ok := t["ready"].(bool); ok && !b {
			ready = false
		}
		switch int(asFloat(t["codec_type"])) {
		case 0:
			g["video_codec"] = prettyCodec(trackCodecName(t))
			g["width"] = asFloat(firstPresent(t, "width", "Width"))
			g["height"] = asFloat(firstPresent(t, "height", "Height"))
			g["fps"] = asFloat(t["fps"])
			g["gop_size"] = asFloat(t["gop_size"])
			g["gop_interval_ms"] = asFloat(t["gop_interval_ms"])
			g["video_frames"] = asFloat(t["frames"])
			g["key_frames"] = asFloat(t["key_frames"])
			g["video_duration_ms"] = asFloat(t["duration"])
		case 1:
			g["audio_codec"] = prettyCodec(trackCodecName(t))
			g["sample_rate"] = asFloat(t["sample_rate"])
			g["channels"] = asFloat(t["channels"])
			g["sample_bit"] = asFloat(t["sample_bit"])
			g["audio_frames"] = asFloat(t["frames"])
			g["audio_duration_ms"] = asFloat(t["duration"])
		}
	}
	if hasTrack {
		if ready {
			g["status"] = "active"
		} else {
			g["status"] = "wait"
		}
		g["tracks"] = tracks
	}
}

func isOriginSchema(schema, originTypeStr string) bool {
	schema = strings.ToLower(schema)
	ot := strings.ToLower(originTypeStr)
	if schema == "" {
		return false
	}
	return strings.Contains(ot, schema) || schema == "rtc" && strings.Contains(ot, "webrtc")
}

func groupMedia(rows []map[string]any) []map[string]any {
	type key struct{ vhost, app, stream string }
	order := make([]key, 0)
	m := map[key]map[string]any{}
	for _, row := range rows {
		k := key{asString(row["vhost"]), asString(row["app"]), asString(row["stream"])}
		g, ok := m[k]
		if !ok {
			g = map[string]any{
				"vhost":            k.vhost,
				"app":              k.app,
				"stream":           k.stream,
				"schemas":          []string{},
				"originType":       row["originType"],
				"originTypeStr":    row["originTypeStr"],
				"originUrl":        row["originUrl"],
				"bytesSpeed":       0.0,
				"in_bps":           0.0,
				"in_bytes":         0.0,
				"readerCount":      0.0,
				"totalReaderCount": 0.0,
				"aliveSecond":      row["aliveSecond"],
				"createStamp":      row["createStamp"],
				"status":           "active",
				"schema_io":        map[string]any{},
			}
			if sock, ok := row["originSock"].(map[string]any); ok {
				g["originSock"] = sock
				g["origin_peer"] = asString(sock["peer_ip"]) + ":" + asString(sock["peer_port"])
			}
			m[k] = g
			order = append(order, k)
		}
		schemas := g["schemas"].([]string)
		schema := asString(row["schema"])
		dup := false
		for _, s := range schemas {
			if s == schema {
				dup = true
				break
			}
		}
		if !dup && schema != "" {
			g["schemas"] = append(schemas, schema)
		}
		bps := asFloat(row["bytesSpeed"])
		total := asFloat(row["totalBytes"])
		if io, ok := g["schema_io"].(map[string]any); ok {
			io[schema] = map[string]any{
				"bps":     bps,
				"bytes":   total,
				"readers": asFloat(row["readerCount"]),
			}
		}
		if isOriginSchema(schema, asString(row["originTypeStr"])) || asFloat(g["in_bps"]) == 0 {
			g["in_bps"] = bps
			g["in_bytes"] = total
			g["bytesSpeed"] = bps
		}
		if asFloat(row["aliveSecond"]) > asFloat(g["aliveSecond"]) {
			g["aliveSecond"] = row["aliveSecond"]
		}
		rc := asFloat(row["totalReaderCount"])
		if rc > asFloat(g["totalReaderCount"]) {
			g["totalReaderCount"] = rc
			g["readerCount"] = asFloat(row["readerCount"])
		}
		if rec, ok := row["isRecordingHLS"].(bool); ok {
			g["isRecordingHLS"] = rec
		}
		if rec, ok := row["isRecordingMP4"].(bool); ok {
			g["isRecordingMP4"] = rec
		}
		origin := isOriginSchema(schema, asString(row["originTypeStr"]))
		if origin {
			g["originType"] = row["originType"]
			g["originTypeStr"] = row["originTypeStr"]
			g["originUrl"] = row["originUrl"]
			g["createStamp"] = row["createStamp"]
			if sock, ok := row["originSock"].(map[string]any); ok {
				g["originSock"] = sock
				g["origin_peer"] = asString(sock["peer_ip"]) + ":" + asString(sock["peer_port"])
			}
		}
		if origin || !asTruthy(g["tracks_from_origin"]) {
			if origin || asString(g["video_codec"]) == "" || asString(g["video_codec"]) == "-" {
				applyTracks(g, row["tracks"])
				if origin && asString(g["video_codec"]) != "" && asString(g["video_codec"]) != "-" {
					g["tracks_from_origin"] = true
				}
			}
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, k := range order {
		g := m[k]
		in := asFloat(g["in_bps"])
		ch := asFloat(g["channels"])
		if ch <= 0 {
			ch = 2
		}
		audioBps := 0.0
		if asString(g["audio_codec"]) != "" && asString(g["audio_codec"]) != "-" {
			audioBps = 16000 * (ch / 2) // AAC/立体声约 128kbps，按声道比例估算
		}
		if audioBps > in*0.4 {
			audioBps = in * 0.08
		}
		g["audio_bps"] = audioBps
		g["video_bps"] = in - audioBps
		if asFloat(g["video_bps"]) < 0 {
			g["video_bps"] = 0
		}
		readers := asFloat(g["totalReaderCount"])
		g["out_bps"] = in * readers
		g["read_size"] = asFloat(g["in_bytes"])
		av := asFloat(g["audio_duration_ms"]) - asFloat(g["video_duration_ms"])
		g["av_diff_ms"] = av
		originSchema := ""
		all := g["schemas"].([]string)
		ot := asString(g["originTypeStr"])
		for _, schema := range all {
			if isOriginSchema(schema, ot) {
				originSchema = schema
				break
			}
		}
		if originSchema == "" {
			low := strings.ToLower(ot)
			for _, cand := range []string{"rtmp", "rtsp", "rtc", "srt", "rtp"} {
				if strings.Contains(low, cand) {
					originSchema = cand
					break
				}
			}
		}
		g["origin_schema"] = originSchema
		pull := make([]string, 0, len(all))
		for _, schema := range all {
			if schema != originSchema {
				pull = append(pull, schema)
			}
		}
		g["pull_schemas"] = pull
		out = append(out, g)
	}
	return out
}

func mediaInfoSchemas(g map[string]any) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 6)
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(asString(g["origin_schema"]))
	ot := asString(g["originTypeStr"])
	for _, cand := range []string{"rtmp", "rtsp", "rtc", "srt", "rtp"} {
		if strings.Contains(strings.ToLower(ot), cand) {
			add(cand)
		}
	}
	if arr, ok := g["schemas"].([]string); ok {
		for _, s := range arr {
			if isOriginSchema(s, ot) {
				add(s)
			}
		}
		for _, s := range arr {
			add(s)
		}
	}
	return out
}

func tracksFromMediaInfo(v map[string]any) any {
	if v == nil {
		return nil
	}
	if tracks := v["tracks"]; tracks != nil {
		return tracks
	}
	switch data := v["data"].(type) {
	case map[string]any:
		return data["tracks"]
	case []any:
		return data
	}
	return nil
}

func (c *zlmClient) refreshGroupedTracks(node config.Node, grouped []map[string]any) {
	if c == nil || len(grouped) == 0 {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, g := range grouped {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			for _, schema := range mediaInfoSchemas(g) {
				vals := url.Values{
					"schema": {schema},
					"vhost":  {asString(g["vhost"])},
					"app":    {asString(g["app"])},
					"stream": {asString(g["stream"])},
				}
				if vals.Get("vhost") == "" {
					vals.Set("vhost", "__defaultVhost__")
				}
				v, err := c.call(node, "getMediaInfo", vals)
				if err != nil {
					continue
				}
				tracks := tracksFromMediaInfo(v)
				if tracks == nil {
					continue
				}
				applyTracks(g, tracks)
				if asString(g["video_codec"]) != "" && asString(g["video_codec"]) != "-" {
					g["tracks_from_origin"] = true
					return
				}
			}
		}()
	}
	wg.Wait()
}

func (c *zlmClient) groupedLiveMedia(node *config.Node) (nodeSnapshot, []map[string]any) {
	snap := c.fetchAll(node)
	grouped := groupMedia(snap.Media)
	c.refreshGroupedTracks(*node, grouped)
	return snap, grouped
}

func sessionName(row map[string]any) string {
	tid := asString(row["typeid"])
	tid = strings.TrimPrefix(tid, "mediakit::")
	tid = strings.TrimPrefix(tid, "toolkit::")
	tid = strings.ReplaceAll(tid, "SessionWithSSL<", "TLS/")
	tid = strings.TrimSuffix(tid, ">")
	tid = strings.TrimSuffix(tid, "Session")
	if tid == "" {
		tid = "conn"
	}
	return tid
}

func sessionMediaKey(app, stream string) string {
	app = strings.TrimSpace(app)
	stream = strings.TrimSpace(stream)
	switch {
	case app != "" && stream != "":
		return app + "/" + stream
	case stream != "":
		return stream
	default:
		return app
	}
}

func lookupPlayerMedia(players map[string][2]string, keys ...string) (string, string, bool) {
	if players == nil {
		return "", "", false
	}
	for _, key := range keys {
		if key == "" {
			continue
		}
		if pair, ok := players[key]; ok {
			return pair[0], pair[1], true
		}
	}
	return "", "", false
}

func (c *zlmClient) collectPlayers(node config.Node, streams []map[string]any) map[string][2]string {
	idx := map[string][2]string{}
	if c == nil || len(streams) == 0 {
		return idx
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	put := func(p map[string]any, app, stream string) {
		pair := [2]string{app, stream}
		if id := asString(p["identifier"]); id != "" {
			idx["ident:"+id] = pair
		}
		if id := asString(p["id"]); id != "" {
			idx["id:"+id] = pair
		}
		if ip := asString(p["peer_ip"]); ip != "" {
			idx["peer:"+ip+":"+asString(p["peer_port"])] = pair
		}
	}
	for _, s := range streams {
		app, stream := asString(s["app"]), asString(s["stream"])
		if app == "" || stream == "" {
			continue
		}
		vhost := asString(s["vhost"])
		if vhost == "" {
			vhost = "__defaultVhost__"
		}
		schemas, _ := s["schemas"].([]string)
		if len(schemas) == 0 {
			schemas = []string{"rtmp", "rtsp", "http", "fmp4", "ts", "hls", "rtc"}
		}
		for _, schema := range schemas {
			schema := schema
			wg.Add(1)
			go func() {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				v, err := c.call(node, "getMediaPlayerList", url.Values{
					"schema": {schema}, "vhost": {vhost}, "app": {app}, "stream": {stream},
				})
				if err != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				for _, p := range asSlice(v["data"]) {
					put(p, app, stream)
				}
			}()
		}
	}
	wg.Wait()
	return idx
}

func annotateSession(row map[string]any, streams []map[string]any, players map[string][2]string) {
	name := sessionName(row)
	ident := asString(row["identifier"])
	if ident == "" {
		ident = asString(row["id"])
	}
	lport := int(asFloat(row["local_port"]))
	peer := asString(row["peer_ip"]) + ":" + asString(row["peer_port"])
	app, stream := asString(row["app"]), asString(row["stream"])
	role, note := "其他", "未归类连接"
	isPub := false
	for _, s := range streams {
		sock, _ := s["originSock"].(map[string]any)
		oid := asString(sock["identifier"])
		opeer := asString(s["origin_peer"])
		if oid != "" && (ident == oid || asString(row["id"]) == oid) {
			isPub = true
			app, stream = asString(s["app"]), asString(s["stream"])
			break
		}
		if opeer != "" && opeer == peer {
			isPub = true
			app, stream = asString(s["app"]), asString(s["stream"])
			break
		}
	}
	if !isPub {
		if a, s, ok := lookupPlayerMedia(players, "ident:"+ident, "id:"+asString(row["id"]), "peer:"+peer); ok {
			app, stream = a, s
		}
	}
	switch {
	case strings.Contains(strings.ToLower(name), "rtmp"):
		if isPub {
			role, note = "推流", "RTMP 推流：编码器正在把音视频推到本机"
		} else {
			role, note = "拉流", "RTMP 播放：客户端正在从本机拉流"
		}
	case strings.Contains(strings.ToLower(name), "rtsp"):
		if isPub {
			role, note = "推流", "RTSP 推流/ANNOUNCE：源正在写入本机"
		} else {
			role, note = "拉流", "RTSP 播放：客户端正在 DESCRIBE/PLAY 拉流"
		}
	case strings.Contains(strings.ToLower(name), "webrtc") || strings.Contains(strings.ToLower(name), "rtc"):
		if isPub {
			role, note = "推流", "WebRTC 推流"
		} else {
			role, note = "拉流", "WebRTC 播放"
		}
	case strings.Contains(strings.ToLower(name), "srt"):
		if isPub {
			role, note = "推流", "SRT 推流"
		} else {
			role, note = "拉流", "SRT 拉流"
		}
	case strings.Contains(strings.ToLower(name), "http"):
		if app != "" {
			role, note = "拉流", "HTTP 播放：FLV/HLS/fMP4 等正在拉流"
		} else {
			role, note = "HTTP", "HTTP 连接：可能是 FLV/HLS 播放、管理页或 API"
			if lport == 80 || lport == 8090 || lport == 8080 {
				note = "HTTP 访问本机媒体端口，常见为 FLV/HLS 播放或打开网页"
			}
		}
	case strings.Contains(strings.ToLower(name), "ws") || strings.Contains(strings.ToLower(name), "websocket"):
		role, note = "拉流", "WebSocket 播放（如 WS-FLV）"
	default:
		if isPub {
			role, note = "推流", "与某条流的来源 socket 匹配，判定为推流端"
		} else if app != "" {
			role, note = "拉流", "与播放端列表匹配，判定为拉流"
		} else {
			note = "协议类型：" + name + "。本地端口 " + asString(lport)
		}
	}
	row["name"] = name
	row["role"] = role
	row["note"] = note
	row["app"] = app
	row["stream"] = stream
	row["media_key"] = sessionMediaKey(app, stream)
	row["is_publisher"] = isPub
}

var externOnce sync.Map

func (c *zlmClient) ensureExternIP(node config.Node, ip string) {
	if ip == "" || ip == "127.0.0.1" || ip == "localhost" || ip == "::1" {
		return
	}
	key := node.ID + "|" + ip
	if _, ok := externOnce.Load(key); ok {
		return
	}
	go func() {
		if _, err := c.call(node, "setServerConfig", url.Values{"rtc.externIP": {ip}}); err == nil {
			externOnce.Store(key, true)
		}
	}()
}

func rewriteSDP(sdp, publicIP string) string {
	if sdp == "" || publicIP == "" || publicIP == "127.0.0.1" || publicIP == "localhost" || publicIP == "::1" {
		return sdp
	}
	s := strings.ReplaceAll(sdp, "127.0.0.1", publicIP)
	s = strings.ReplaceAll(s, "0.0.0.0", publicIP)
	s = strings.ReplaceAll(s, "::1", publicIP)
	return s
}

func (c *zlmClient) callPOST(node config.Node, api string, extra url.Values) (map[string]any, error) {
	return c.callPOSTUsing(context.Background(), c.http, node, api, extra)
}

func (c *zlmClient) callPOSTWithTimeout(ctx context.Context, node config.Node, api string, extra url.Values, timeout time.Duration) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("POST timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := *c.http
	client.Timeout = 0
	return c.callPOSTUsing(ctx, &client, node, api, extra)
}

func (c *zlmClient) callPOSTUsing(ctx context.Context, client *http.Client, node config.Node, api string, extra url.Values) (map[string]any, error) {
	u, err := url.Parse(strings.TrimRight(node.API, "/") + "/index/api/" + api)
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	q := u.Query()
	q.Set("secret", node.Secret)
	u.RawQuery = q.Encode()
	if extra == nil {
		extra = url.Values{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(extra.Encode()))
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	defer resp.Body.Close()
	out, err := parseZLMResponse(node, api, resp)
	return out, sanitizeZLMTransportError(node, err)
}

func (c *zlmClient) callJSON(node config.Node, api string, body any) (map[string]any, error) {
	u, err := url.Parse(strings.TrimRight(node.API, "/") + "/index/api/" + api)
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	q := u.Query()
	q.Set("secret", node.Secret)
	u.RawQuery = q.Encode()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, sanitizeZLMTransportError(node, err)
	}
	defer resp.Body.Close()
	out, err := parseZLMResponse(node, api, resp)
	return out, sanitizeZLMTransportError(node, err)
}

func (c *zlmClient) webrtcPlay(node config.Node, app, stream, typ, offer, publicIP string) ([]byte, int, error) {
	c.ensureExternIP(node, publicIP)
	if typ == "" {
		typ = "play"
	}
	u, err := url.Parse(strings.TrimRight(node.API, "/") + "/index/api/webrtc")
	if err != nil {
		return nil, 500, err
	}
	q := u.Query()
	q.Set("secret", node.Secret)
	q.Set("app", app)
	q.Set("stream", stream)
	q.Set("type", typ)
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(offer))
	if err != nil {
		return nil, 500, err
	}
	req.Header.Set("Content-Type", "text/plain;charset=utf-8")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 502, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, 502, err
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		if sdp, ok := obj["sdp"].(string); ok && sdp != "" {
			obj["sdp"] = rewriteSDP(sdp, publicIP)
			if b, e := json.Marshal(obj); e == nil {
				body = b
			}
		}
	}
	return body, resp.StatusCode, nil
}

type PlayLink struct {
	ID      string              `json:"id"`
	Label   string              `json:"label"`
	URL     string              `json:"url"`
	WebPlay bool                `json:"web_play"`
	Extra   []map[string]string `json:"extra,omitempty"`
}

func playLinks(host string, n config.Node, vhost, app, stream string) []PlayLink {
	if host == "" {
		host = "127.0.0.1"
	}
	httpPort := nz(n.HTTPPort, 8090)
	httpBase := fmt.Sprintf("http://%s:%d", host, httpPort)
	key := app + "/" + stream
	if vhost != "" && vhost != "__defaultVhost__" {
		key = vhost + "/" + app + "/" + stream
	}
	appQ := url.QueryEscape(app)
	streamQ := url.QueryEscape(stream)
	webrtcPlain := fmt.Sprintf("webrtc://%s/%s/%s", host, app, stream)
	webrtcPorted := fmt.Sprintf("webrtc://%s:%d/%s/%s", host, httpPort, app, stream)
	webrtcAPI := fmt.Sprintf("%s/index/api/webrtc?app=%s&stream=%s&type=play", httpBase, appQ, streamQ)
	whep := fmt.Sprintf("%s/index/api/whep?app=%s&stream=%s", httpBase, appQ, streamQ)
	rtcUDP := nz(n.WebRTCPort, 8000)
	sipPort := nz(n.SipPort, 5060)
	rtmpURL := fmt.Sprintf("rtmp://%s:%d/%s/%s", host, nz(n.RTMPPort, 1935), app, stream)
	rtmpLocal := fmt.Sprintf("rtmp://127.0.0.1:%d/%s/%s", nz(n.RTMPPort, 1935), app, stream)
	dashOut := dashOutputFile(n, vhost, app, stream)
	dashCmd := dashFFmpegCmd(rtmpLocal, dashOut)
	dashMPD := fmt.Sprintf("%s/%s/dash.mpd", httpBase, key)
	return []PlayLink{
		{ID: "http-flv", Label: "HTTP-FLV", URL: fmt.Sprintf("%s/%s.live.flv", httpBase, key), WebPlay: true},
		{ID: "ws-flv", Label: "WS-FLV", URL: fmt.Sprintf("ws://%s:%d/%s.live.flv", host, httpPort, key), WebPlay: true},
		{ID: "hls", Label: "HLS", URL: fmt.Sprintf("%s/%s/hls.m3u8", httpBase, key), WebPlay: true},
		{ID: "hls-fmp4", Label: "HLS-fMP4", URL: fmt.Sprintf("%s/%s/hls.fmp4.m3u8", httpBase, key), WebPlay: true},
		{ID: "http-ts", Label: "HTTP-TS", URL: fmt.Sprintf("%s/%s.live.ts", httpBase, key), WebPlay: true},
		{ID: "ws-ts", Label: "WS-TS", URL: fmt.Sprintf("ws://%s:%d/%s.live.ts", host, httpPort, key), WebPlay: true},
		{ID: "http-fmp4", Label: "HTTP-fMP4", URL: fmt.Sprintf("%s/%s.live.mp4", httpBase, key), WebPlay: true},
		{ID: "ws-fmp4", Label: "WS-fMP4", URL: fmt.Sprintf("ws://%s:%d/%s.live.mp4", host, httpPort, key), WebPlay: true},
		{ID: "webrtc", Label: "WebRTC", URL: webrtcPorted, WebPlay: true, Extra: []map[string]string{
			{"label": "webrtc://", "url": webrtcPlain},
			{"label": "webrtc://信令端口", "url": webrtcPorted},
			{"label": "HTTP 信令", "url": webrtcAPI},
			{"label": "WHEP", "url": whep, "hint": "需 POST SDP，不能当视频地址打开"},
			{"label": "ICE UDP/TCP", "url": fmt.Sprintf("%s:%d", host, rtcUDP), "hint": "浏览器到该地址的 UDP/TCP 需放行，否则信令成功也没有画面"},
		}},
		{ID: "rtsp", Label: "RTSP", URL: fmt.Sprintf("rtsp://%s:%d/%s/%s", host, nz(n.RTSPPort, 554), app, stream), WebPlay: false},
		{ID: "rtmp", Label: "RTMP", URL: rtmpURL, WebPlay: false},
		{ID: "srt", Label: "SRT", URL: fmt.Sprintf("srt://%s:%d?streamid=#!::r=%s/%s,m=request", host, nz(n.SRTPort, 9000), app, stream), WebPlay: false},
		{ID: "gb28181", Label: "GB28181", URL: fmt.Sprintf("sip:%s:%d/%s/%s", host, sipPort, app, stream), WebPlay: false, Extra: []map[string]string{
			{"label": "流 ID", "url": app + "/" + stream},
			{"label": "SIP", "url": fmt.Sprintf("%s:%d", host, sipPort)},
			{"label": "说明", "url": "浏览器不能播。由国标平台向该 SIP 口 INVITE，对应 ZLM 内 app/stream"},
		}},
		{ID: "dash", Label: "DASH", URL: dashMPD, WebPlay: true, Extra: []map[string]string{
			{"label": "ffplay", "url": "ffplay " + dashMPD},
			{"label": "本机 RTMP", "url": rtmpLocal},
			{"label": "FFmpeg 命令", "url": dashCmd},
			{"label": "落盘", "url": dashOut},
			{"label": "说明", "url": "ZLM 不直接出 DASH。默认关闭虚拟主机，MPD 写到 /data/zlm/{app}/{stream}/dash.mpd，打开 http://host:8090/ 即可浏览 live、mp4 等目录"},
		}},
	}
}

func dashOutputFile(n config.Node, vhost, app, stream string) string {
	root := strings.ReplaceAll(strings.TrimSpace(n.WWW), "\\", "/")
	root = strings.TrimRight(root, "/")
	if strings.HasSuffix(strings.ToLower(root), "/www") {
		root = defaultDashRoot
	}
	if root == "" {
		root = defaultDashRoot
	}
	if n.EnableVhost {
		if vhost == "" || vhost == "__defaultVhost__" {
			vhost = "__defaultVhost__"
		}
		root = root + "/" + vhost
	}
	return root + "/" + app + "/" + stream + "/dash.mpd"
}

func playURLs(host string, n config.Node, vhost, app, stream string) map[string]string {
	out := map[string]string{}
	for _, l := range playLinks(host, n, vhost, app, stream) {
		out[l.ID] = l.URL
	}
	return out
}

func zlmPlayPath(nodeID, key, file string) string {
	if nodeID == "" {
		nodeID = "zlm-1"
	}
	parts := strings.Split(strings.Trim(key, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/api/node/" + url.PathEscape(nodeID) + "/zlm/" + strings.Join(parts, "/") + "/" + file
}

func nz(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
