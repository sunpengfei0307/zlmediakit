package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"zlm-admin/core/config"
)

const (
	mediaListMax   = 8000
	mediaWalkDepth = 8
)

var mediaExt = map[string]bool{
	".mp4": true, ".ts": true, ".m3u8": true, ".flv": true,
	".fmp4": true, ".m4s": true, ".mpd": true, ".mp3": true, ".aac": true, ".wav": true,
	".jpg": true, ".jpeg": true,
}

var skipDirName = map[string]bool{
	"webassist": true, "swagger": true, "webrtc": true, "readme": true,
	".git": true, "node_modules": true, "assets": true,
	"hls": true, "rec": true, "ts": true, "flv": true, "dash": true,
	"__defaultvhost__": true, ".cache": true,
}

func isZLMSnapCacheDir(name string) bool {
	if len(name) != 32 {
		return false
	}
	for i := 0; i < 32; i++ {
		c := name[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

type MediaFile struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	Dir      string `json:"dir"`
	Ext      string `json:"ext"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mtime"`
	Kind     string `json:"kind"`
	Role     string `json:"role"`
	Place    string `json:"place"`
	App      string `json:"app,omitempty"`
	Stream   string `json:"stream,omitempty"`
	Date     string `json:"date,omitempty"`
	Playlist  string `json:"playlist,omitempty"`
	Proto     string `json:"proto,omitempty"`
	Head      int    `json:"head,omitempty"`
	DurationSec float64 `json:"duration_sec,omitempty"`
	VodLoaded bool   `json:"vod_loaded,omitempty"`
	VodVhost  string `json:"vod_vhost,omitempty"`
	VodApp    string `json:"vod_app,omitempty"`
	VodStream string `json:"vod_stream,omitempty"`
	PlayURL   string `json:"play_url,omitempty"`
	PlaySID   string `json:"play_sid,omitempty"`
}

func nodeRoots(n config.Node) []string {
	seen := map[string]bool{}
	out := make([]string, 0, 4)
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(n.Root)
	add(n.WWW)
	add(n.MP4Save)
	add(n.HLSSave)
	add("/data/zlm")
	return out
}

func insideRoot(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	return rel != ".." && !strings.HasPrefix(rel, "../")
}

func relToNode(n config.Node, abs string) string {
	if n.Root != "" {
		if rel, err := filepath.Rel(n.Root, abs); err == nil && !strings.HasPrefix(filepath.ToSlash(rel), "../") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(abs)
}

func splitPathParts(rel string) []string {
	rel = filepath.ToSlash(strings.TrimPrefix(rel, "/"))
	parts := make([]string, 0, 8)
	for _, p := range strings.Split(rel, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

func isDateFolder(s string) bool {
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

func isMetaDir(s string) bool {
	switch strings.ToLower(s) {
	case "www", "record", "rec", "hls", "mp4", "ts", "flv", "data", "zlm", "dash", "snap", "event":
		return true
	default:
		return false
	}
}

func nonMetaParts(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if isMetaDir(p) || isDateFolder(p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

func addAppStream(apps map[string]map[string]bool, app, stream string) {
	app = strings.TrimSpace(app)
	stream = strings.TrimSpace(stream)
	if app == "" || stream == "" || isMetaDir(app) || isDateFolder(stream) {
		return
	}
	if skipDirName[strings.ToLower(app)] || skipDirName[strings.ToLower(stream)] {
		return
	}
	if _, ok := apps[app]; !ok {
		apps[app] = map[string]bool{}
	}
	apps[app][stream] = true
}

func noteDirApps(apps map[string]map[string]bool, rel string) {
	parts := nonMetaParts(splitPathParts(rel))
	if len(parts) >= 2 {
		addAppStream(apps, parts[0], parts[1])
	}
}

func compactApps(apps map[string]map[string]bool) map[string][]string {
	keys := make([]string, 0, len(apps))
	for a := range apps {
		keys = append(keys, a)
	}
	sort.Strings(keys)
	out := make(map[string][]string, len(keys))
	for _, a := range keys {
		streams := make([]string, 0, len(apps[a]))
		for s := range apps[a] {
			streams = append(streams, s)
		}
		sort.Strings(streams)
		out[a] = streams
	}
	return out
}

func classifyRel(rel, name, ext string) (role, place, app, stream, date, proto string) {
	parts := splitPathParts(rel)
	if len(parts) == 0 {
		return "other", "", "", "", "", "other"
	}
	dateIdx := -1
	rec := false
	for i, p := range parts {
		if isDateFolder(p) {
			dateIdx = i
			date = p
		}
		if strings.EqualFold(p, "record") || strings.EqualFold(p, "rec") {
			rec = true
		}
	}
	isInit := isInitSegment(name)
	dirs := parts
	if len(parts) > 0 {
		dirs = parts[:len(parts)-1]
	}
	named := nonMetaParts(dirs)
	if dateIdx >= 2 {
		app, stream = parts[dateIdx-2], parts[dateIdx-1]
	} else if len(named) >= 2 {
		app, stream = named[0], named[1]
	} else if len(named) == 1 {
		app = named[0]
	}
	low := strings.ToLower(name)
	lowRel := strings.ToLower(rel)
	if !isInit && ext == ".mp4" && (strings.HasPrefix(low, "event-") || strings.Contains(lowRel, "/event/")) {
		return "rec_event", "record", app, stream, date, "record"
	}
	if rec && ((ext == ".mp4" && !isInit) || ext == ".flv") {
		place = "record"
		if ext == ".flv" {
			return "rec_flv", place, app, stream, date, "record"
		}
		return "rec_mp4", place, app, stream, date, "record"
	}
	place = "live"
	switch {
	case ext == ".jpg" || ext == ".jpeg" || strings.Contains(lowRel, "/snap/"):
		if ext == ".jpg" || ext == ".jpeg" {
			names := make([]string, 0, 2)
			start := 0
			for i, p := range parts {
				if strings.EqualFold(p, "snap") {
					start = i + 1
				}
			}
			for _, p := range parts[start:] {
				if isDateFolder(p) || strings.EqualFold(p, name) {
					continue
				}
				names = append(names, p)
			}
			switch len(names) {
			case 0:
				app, stream = "", strings.TrimSuffix(name, ext)
				if strings.EqualFold(stream, "latest") {
					stream = ""
				}
			case 1:
				app, stream = "", names[0]
			default:
				app, stream = names[0], names[1]
			}
			return "live_snap", place, app, stream, date, "snap"
		}
		return "other", place, app, stream, date, "other"
	case ext == ".mpd" || strings.Contains(lowRel, "/dash/") || isDashFile(name, rel, ext):
		return "live_dash", place, app, stream, date, "dash"
	case ext == ".ts" || ext == ".m3u8" || isHLSfmp4File(name, rel, ext):
		if ext == ".m4s" || isInit || strings.Contains(low, "fmp4") {
			return "live_fmp4", place, app, stream, date, "hls"
		}
		return "live_hls", place, app, stream, date, "hls"
	case ext == ".mp4":
		return "live_fmp4", place, app, stream, date, "fmp4"
	default:
		return "other", place, app, stream, date, "other"
	}
}

func fileHeadRank(name, ext string) int {
	if ext == ".m3u8" || ext == ".mpd" {
		return 0
	}
	if isInitSegment(name) {
		return 1
	}
	return 2
}

func fileKind(ext string) string {
	switch ext {
	case ".mp4", ".flv", ".fmp4":
		return "mp4"
	case ".ts":
		return "ts"
	case ".m3u8":
		return "hls"
	case ".m4s":
		return "fmp4"
	case ".mpd":
		return "dash"
	case ".jpg", ".jpeg":
		return "snap"
	default:
		return strings.TrimPrefix(ext, ".")
	}
}

func streamSearchDirs(n config.Node, app, stream string) []string {
	if app == "" || stream == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		if seen[p] {
			return
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	suffixes := []string{
		filepath.Join(app, stream),
		filepath.Join("www", app, stream),
		filepath.Join("record", app, stream),
		filepath.Join("www", "record", app, stream),
		filepath.Join("hls", app, stream),
		filepath.Join("mp4", app, stream),
		filepath.Join("mp4", "record", app, stream),
		filepath.Join("mp4", "rec", app, stream),
		filepath.Join("flv", app, stream),
		filepath.Join("ts", app, stream),
		filepath.Join("rec", app, stream),
		filepath.Join("event", app, stream),
		filepath.Join("mp4", "event", app, stream),
		filepath.Join("dash", app, stream),
		filepath.Join("snap", stream),
		filepath.Join("snap", app, stream),
	}
	for _, root := range nodeRoots(n) {
		for _, s := range suffixes {
			add(filepath.Join(root, s))
		}
	}
	add(filepath.Join(snapRootOf(n), stream))
	add(filepath.Join(snapRootOf(n), app, stream))
	return out
}

func collectDiskApps(n config.Node, roots []string) map[string]map[string]bool {
	apps := map[string]map[string]bool{}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if skipDirName[strings.ToLower(d.Name())] || isZLMSnapCacheDir(d.Name()) {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(root, path)
			depth := 0
			if rel != "." {
				depth = strings.Count(filepath.ToSlash(rel), "/") + 1
			}
			if depth > mediaWalkDepth {
				return filepath.SkipDir
			}
			noteDirApps(apps, relToNode(n, path))
			if isDateFolder(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		})
	}
	return apps
}

func matchRole(role, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || want == "all" {
		return true
	}
	switch want {
	case "record":
		return role == "rec_mp4" || role == "rec_hls" || role == "rec_flv"
	case "event":
		return role == "rec_event"
	case "live":
		return strings.HasPrefix(role, "live_")
	default:
		return role == want
	}
}

type fileListOpt struct {
	Kind   string
	Role   string
	App    string
	Stream string
	Period string
}

func listMediaFiles(n config.Node, opt fileListOpt) ([]MediaFile, []string, map[string][]string, error) {
	roots := nodeRoots(n)
	if len(roots) == 0 {
		return nil, nil, nil, fmt.Errorf("未配置媒体目录")
	}
	opt.Kind = strings.ToLower(strings.TrimSpace(opt.Kind))
	opt.Role = strings.ToLower(strings.TrimSpace(opt.Role))
	opt.App = strings.TrimSpace(opt.App)
	opt.Stream = strings.TrimSpace(opt.Stream)
	opt.Period = strings.TrimSpace(opt.Period)
	apps := collectDiskApps(n, roots)
	if opt.App == "" || opt.Stream == "" {
		return nil, roots, compactApps(apps), nil
	}
	walkRoots := streamSearchDirs(n, opt.App, opt.Stream)
	if len(walkRoots) == 0 {
		walkRoots = roots
	}
	out := make([]MediaFile, 0, 64)
	var walkErr error
	seen := map[string]bool{}
	for _, root := range walkRoots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if skipDirName[strings.ToLower(name)] || isZLMSnapCacheDir(name) {
					return filepath.SkipDir
				}
				rel, _ := filepath.Rel(root, path)
				depth := 0
				if rel != "." {
					depth = strings.Count(filepath.ToSlash(rel), "/") + 1
				}
				if depth > mediaWalkDepth {
					return filepath.SkipDir
				}
				noteDirApps(apps, relToNode(n, path))
				return nil
			}
			ext := strings.ToLower(filepath.Ext(d.Name()))
			if !mediaExt[ext] {
				return nil
			}
			rel := relToNode(n, path)
			role, place, app, stream, date, proto := classifyRel(rel, d.Name(), ext)
			addAppStream(apps, app, stream)
			if len(out) >= mediaListMax {
				walkErr = fmt.Errorf("文件过多，已截断到 %d", mediaListMax)
				return filepath.SkipAll
			}
			k := fileKind(ext)
			if opt.Kind != "" && opt.Kind != "all" && k != opt.Kind {
				return nil
			}
			if !matchRole(role, opt.Role) {
				return nil
			}
			if opt.Stream != "" && stream != opt.Stream {
				return nil
			}
			if opt.App != "" && app != "" && app != opt.App {
				return nil
			}
			if opt.Period != "" && date != opt.Period && !strings.Contains(rel, opt.Period) {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			out = append(out, MediaFile{
				Path:        rel,
				Name:        d.Name(),
				Dir:         filepath.ToSlash(filepath.Dir(rel)),
				Ext:         ext,
				Size:        info.Size(),
				ModTime:     info.ModTime().Format(time.RFC3339),
				Kind:        k,
				Role:        role,
				Place:       place,
				App:         app,
				Stream:      stream,
				Date:        date,
				Proto:       proto,
				Head:        fileHeadRank(d.Name(), ext),
				DurationSec: fileDurationSec(path, ext),
			})
			return nil
		})
		if walkErr != nil {
			break
		}
	}
	attachPlaylists(out)
	reclassifyByDir(out)
	attachPlaylists(out)
	attachLiveHLSPlayURLs(n.ID, n, out)
	fillMissingDurations(out)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Head != out[j].Head {
			return out[i].Head < out[j].Head
		}
		return out[i].ModTime > out[j].ModTime
	})
	return out, roots, compactApps(apps), walkErr
}

func attachPlaylists(files []MediaFile) {
	byDir := map[string][]int{}
	for i, f := range files {
		byDir[f.Dir] = append(byDir[f.Dir], i)
	}
	for _, idxs := range byDir {
		pick := ""
		var m3u8s []string
		for _, i := range idxs {
			if files[i].Ext == ".m3u8" {
				m3u8s = append(m3u8s, files[i].Path)
			}
		}
		for _, p := range m3u8s {
			if strings.Contains(strings.ToLower(p), "fmp4") {
				pick = p
				break
			}
		}
		if pick == "" && len(m3u8s) > 0 {
			pick = m3u8s[0]
		}
		if pick == "" {
			continue
		}
		for _, i := range idxs {
			if files[i].Ext != ".m3u8" && files[i].Role != "rec_mp4" {
				files[i].Playlist = pick
			}
		}
	}
}

func mediaLookupPaths(n config.Node, rel string) []string {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.Contains(rel, "\x00") {
		return nil
	}
	slashRel := strings.TrimPrefix(rel, "/")
	if slashRel == "" || slashRel == "." || strings.HasPrefix(slashRel, "../") {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, 8)
	add := func(p string) {
		p = filepath.Clean(p)
		if p == "" || p == "." {
			return
		}
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	// mediaUrl / gin *filepath 会丢掉 Unix 绝对路径的前导 /，变成 data/zlm/...
	add("/" + slashRel)
	if filepath.IsAbs(rel) {
		add(rel)
	}
	clean := filepath.Clean(slashRel)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return out
	}
	for _, root := range nodeRoots(n) {
		add(filepath.Join(root, clean))
	}
	return out
}

func resolveMediaFile(n config.Node, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", os.ErrPermission
	}
	roots := nodeRoots(n)
	if len(roots) == 0 {
		return "", fmt.Errorf("未配置媒体目录")
	}
	for _, abs := range mediaLookupPaths(n, rel) {
		ok := false
		for _, root := range roots {
			if insideRoot(root, abs) {
				ok = true
				break
			}
		}
		if !ok {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(abs))
		if !mediaExt[ext] {
			return "", os.ErrPermission
		}
		return abs, nil
	}
	return "", os.ErrNotExist
}

func (h *Hub) ServeFile(w http.ResponseWriter, r *http.Request, nodeID, rel string) {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		http.Error(w, `{"code":-1,"msg":"unknown node"}`, http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("dl") != "1" && isLiveHLSIndex(rel) {
		if dest := liveHLSProxyURL(n, nodeID, rel); dest != "" {
			http.Redirect(w, r, dest, http.StatusFound)
			return
		}
	}
	abs, err := resolveMediaFile(n, rel)
	if err != nil {
		http.Error(w, `{"code":-1,"msg":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	f, err := os.Open(abs)
	if err != nil {
		http.Error(w, `{"code":-1,"msg":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.Error(w, `{"code":-1,"msg":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+st.Name()+`"`)
		http.ServeContent(w, r, st.Name(), st.ModTime(), f)
		return
	}
	serveMedia(w, r, nodeID, rel, f, st)
}

func (h *Hub) ServeMedia(w http.ResponseWriter, r *http.Request, nodeID, rel string) {
	rel = strings.TrimPrefix(rel, "/")
	h.ServeFile(w, r, nodeID, rel)
}

func serveMedia(w http.ResponseWriter, r *http.Request, nodeID, rel string, f *os.File, st os.FileInfo) {
	ext := strings.ToLower(filepath.Ext(st.Name()))
	switch ext {
	case ".m3u8":
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	case ".ts":
		w.Header().Set("Content-Type", "video/mp2t")
	case ".m4s":
		w.Header().Set("Content-Type", "video/iso.segment")
	case ".mpd":
		w.Header().Set("Content-Type", "application/dash+xml")
	case ".mp4", ".fmp4":
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Cache-Control", "public, max-age=30")
		if ext == ".mp4" {
			if ok := mp4HasFastStart(f, st.Size()); ok {
				w.Header().Set("X-MP4-FastStart", "1")
			} else {
				w.Header().Set("X-MP4-FastStart", "0")
			}
			_, _ = f.Seek(0, io.SeekStart)
		}
	case ".flv":
		w.Header().Set("Content-Type", "video/x-flv")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "no-cache")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-cache")
	case ".gif":
		w.Header().Set("Content-Type", "image/gif")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	}
	w.Header().Set("Access-Control-Allow-Origin", "*")
	if ext == ".m3u8" && r.URL.Query().Get("dl") != "1" {
		raw, err := io.ReadAll(io.LimitReader(f, 2<<20))
		if err != nil {
			http.Error(w, `{"code":-1,"msg":"read playlist"}`, http.StatusBadGateway)
			return
		}
		body := rewritePlaylist(nodeID, rel, raw)
		http.ServeContent(w, r, st.Name(), st.ModTime(), bytes.NewReader(body))
		return
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

var m3u8URI = regexp.MustCompile(`URI="([^"]+)"`)

func mediaPublicPath(nodeID, rel string) string {
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") {
		return "/api/node/" + url.PathEscape(nodeID) + "/file?path=" + url.QueryEscape(rel)
	}
	rel = strings.TrimPrefix(rel, "/")
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/api/node/" + url.PathEscape(nodeID) + "/media/" + strings.Join(parts, "/")
}

func rewritePlaylist(nodeID, rel string, body []byte) []byte {
	dir := path.Dir(filepath.ToSlash(rel))
	if dir == "." {
		dir = ""
	}
	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "#") {
			lines[i] = m3u8URI.ReplaceAllStringFunc(line, func(m string) string {
				sub := m3u8URI.FindStringSubmatch(m)
				if len(sub) < 2 {
					return m
				}
				return `URI="` + resolvePlaylistURI(nodeID, dir, sub[1]) + `"`
			})
			continue
		}
		lines[i] = resolvePlaylistURI(nodeID, dir, trim)
	}
	return []byte(strings.Join(lines, "\n"))
}

func resolvePlaylistURI(nodeID, dir, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" || strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "/api/node/") {
		return uri
	}
	rel := uri
	if dir != "" && !strings.HasPrefix(uri, "/") {
		rel = path.Join(dir, uri)
	}
	if strings.HasPrefix(rel, "/") {
		return mediaPublicPath(nodeID, path.Clean(rel))
	}
	rel = path.Clean(strings.TrimPrefix(rel, "/"))
	return mediaPublicPath(nodeID, rel)
}

func isLiveHLSIndex(name string) bool {
	switch strings.ToLower(filepath.Base(name)) {
	case "hls.m3u8", "hls.fmp4.m3u8":
		return true
	}
	return false
}

func httpRelFromAbs(n config.Node, abs string) string {
	abs = filepath.ToSlash(strings.TrimSpace(abs))
	if abs == "" {
		return ""
	}
	roots := []string{n.WWW, n.HLSSave, "/data/zlm"}
	for _, root := range roots {
		root = strings.TrimRight(filepath.ToSlash(strings.TrimSpace(root)), "/")
		if root == "" {
			continue
		}
		if abs == root {
			return ""
		}
		if strings.HasPrefix(abs, root+"/") {
			return strings.TrimPrefix(abs, root+"/")
		}
	}
	trimmed := strings.TrimPrefix(abs, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 3 && isLiveHLSIndex(parts[2]) {
		return trimmed
	}
	return ""
}

func liveHLSProxyURL(n config.Node, nodeID, filePath string) string {
	if !isLiveHLSIndex(filePath) {
		return ""
	}
	rel := httpRelFromAbs(n, filePath)
	if rel == "" {
		return ""
	}
	if nodeID == "" {
		nodeID = n.ID
	}
	if nodeID == "" {
		nodeID = "zlm-1"
	}
	return livePublicPath(nodeID, rel)
}

func attachLiveHLSPlayURLs(nodeID string, n config.Node, files []MediaFile) {
	byDir := map[string]string{}
	for _, f := range files {
		if url := liveHLSProxyURL(n, nodeID, f.Path); url != "" {
			byDir[f.Dir] = url
		}
	}
	for i := range files {
		if url, ok := byDir[files[i].Dir]; ok {
			files[i].Playlist = url
			continue
		}
		if url := liveHLSProxyURL(n, nodeID, files[i].Path); url != "" {
			files[i].Playlist = url
		}
	}
}
