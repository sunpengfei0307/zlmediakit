package service

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	reDashInit      = regexp.MustCompile(`(?i)^init-stream(\d+)\.(m4s|mp4)$`)
	reDashChunk     = regexp.MustCompile(`(?i)^chunk-stream(\d+)-.+\.m4s$`)
	reStreamDir     = regexp.MustCompile(`(?i)^stream\d+$`)
	reDashStreamRel = regexp.MustCompile(`(?i)/stream\d+/`)
)

func isInitSegment(name string) bool {
	low := strings.ToLower(name)
	return low == "init.mp4" || reDashInit.MatchString(name)
}

func dashStreamKey(name string) (key, kind string) {
	name = filepath.Base(filepath.FromSlash(strings.ReplaceAll(name, "\\", "/")))
	if m := reDashInit.FindStringSubmatch(name); len(m) > 1 {
		return m[1], "init"
	}
	if m := reDashChunk.FindStringSubmatch(name); len(m) > 1 {
		return m[1], "chunk"
	}
	return "", ""
}

func isStreamDir(name string) bool {
	return reStreamDir.MatchString(name)
}

func isDashFile(name, rel, ext string) bool {
	lowRel := strings.ToLower(filepath.ToSlash(rel))
	if ext == ".mpd" || strings.Contains(lowRel, "/dash/") {
		return true
	}
	if (ext == ".m4s" || ext == ".mp4") && reDashStreamRel.MatchString(lowRel) {
		return true
	}
	k, _ := dashStreamKey(name)
	return k != ""
}

func isHLSfmp4File(name, rel, ext string) bool {
	low := strings.ToLower(name)
	lowRel := strings.ToLower(rel)
	if isDashFile(name, rel, ext) {
		return false
	}
	if strings.Contains(low, "hls.fmp4") || strings.Contains(lowRel, "hls.fmp4") {
		return true
	}
	if low == "init.mp4" || ext == ".m4s" {
		return true
	}
	return false
}

func reclassifyByDir(files []MediaFile) {
	byDir := map[string][]int{}
	for i, f := range files {
		byDir[f.Dir] = append(byDir[f.Dir], i)
	}
	for _, idxs := range byDir {
		hasHLS := false
		for _, i := range idxs {
			if files[i].Ext == ".m3u8" {
				hasHLS = true
				break
			}
		}
		for _, i := range idxs {
			f := files[i]
			if f.Role == "rec_mp4" || f.Role == "rec_flv" {
				continue
			}
			if isDashFile(f.Name, f.Path, f.Ext) {
				files[i].Proto = "dash"
				files[i].Role = "live_dash"
				continue
			}
			if hasHLS && (f.Ext == ".m4s" || f.Ext == ".ts" || f.Ext == ".m3u8" || isInitSegment(f.Name) || (f.Ext == ".mp4" && f.Place != "record")) {
				files[i].Proto = "hls"
				if f.Ext == ".ts" || f.Ext == ".m3u8" {
					files[i].Role = "live_hls"
				} else {
					files[i].Role = "live_fmp4"
				}
			}
		}
	}
}

func pickFMP4Parts(clicked string, names []string) (initName string, segs []string) {
	if strings.HasSuffix(strings.ToLower(clicked), ".mpd") {
		clicked = "init-stream0.m4s"
	}
	key, _ := dashStreamKey(clicked)
	if key != "" {
		for _, n := range names {
			k, t := dashStreamKey(n)
			if k != key {
				continue
			}
			if t == "init" {
				initName = n
			}
			if t == "chunk" {
				segs = append(segs, n)
			}
		}
		sort.Strings(segs)
		return initName, segs
	}
	low := strings.ToLower(clicked)
	isInit := low == "init.mp4"
	isM4s := strings.HasSuffix(low, ".m4s")
	isFragMp4 := strings.HasSuffix(low, ".mp4") && !isInit && !strings.Contains(low, "hls")
	if !isInit && !isM4s && !isFragMp4 {
		return "", nil
	}
	for _, n := range names {
		if strings.EqualFold(n, "init.mp4") {
			initName = n
			break
		}
	}
	for _, n := range names {
		ln := strings.ToLower(n)
		if initName != "" && strings.EqualFold(n, initName) {
			continue
		}
		if k, _ := dashStreamKey(n); k != "" {
			continue
		}
		if strings.HasSuffix(ln, ".m4s") {
			segs = append(segs, n)
			continue
		}
		if isFragMp4 && strings.HasSuffix(ln, ".mp4") && ln != "init.mp4" && !strings.Contains(ln, "hls") {
			segs = append(segs, n)
		}
	}
	sort.Strings(segs)
	return initName, segs
}

func listDashPackNames(packAbs string) []string {
	ents, err := os.ReadDir(packAbs)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			if !isStreamDir(e.Name()) {
				continue
			}
			kids, err := os.ReadDir(filepath.Join(packAbs, e.Name()))
			if err != nil {
				continue
			}
			for _, k := range kids {
				if !k.IsDir() {
					names = append(names, e.Name()+"/"+k.Name())
				}
			}
			continue
		}
		names = append(names, e.Name())
	}
	return names
}

func (h *Hub) ServeFMP4List(w http.ResponseWriter, r *http.Request, nodeID, rel string) {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		http.Error(w, `{"code":-1,"msg":"unknown node"}`, http.StatusNotFound)
		return
	}
	abs, err := resolveMediaFile(n, rel)
	if err != nil {
		http.Error(w, `{"code":-1,"msg":"`+err.Error()+`"}`, http.StatusNotFound)
		return
	}
	fileDir := filepath.Dir(abs)
	packAbs := fileDir
	if isStreamDir(filepath.Base(fileDir)) {
		packAbs = filepath.Dir(fileDir)
	}
	names := listDashPackNames(packAbs)
	clicked := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(abs), filepath.ToSlash(packAbs)), "/"))
	initName, segs := pickFMP4Parts(clicked, names)
	if initName == "" && len(segs) == 0 {
		http.Error(w, `{"code":-1,"msg":"no fmp4 init/segments"}`, http.StatusNotFound)
		return
	}
	packRel := filepath.ToSlash(relToNode(n, packAbs))
	if packRel == "." {
		packRel = ""
	}
	join := func(name string) string {
		name = strings.TrimPrefix(filepath.ToSlash(name), "/")
		if packRel == "" {
			return mediaPublicPath(nodeID, name)
		}
		return mediaPublicPath(nodeID, packRel+"/"+name)
	}
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:7\n#EXT-X-TARGETDURATION:10\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n")
	if initName != "" {
		fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s\"\n", join(initName))
	}
	for _, s := range segs {
		b.WriteString("#EXTINF:2.000,\n")
		b.WriteString(join(s))
		b.WriteByte('\n')
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b.Bytes())
}
