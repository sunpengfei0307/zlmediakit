package service

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"
)

const hlsInitMaxBytes = 2 << 20

var (
	hlsInitHTTP = &http.Client{Timeout: 8 * time.Second}
	hlsInitBusy sync.Map
)

func extractFmp4Init(r io.Reader) ([]byte, error) {
	var out []byte
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil, err
		}
		size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		var payloadSize uint64
		switch {
		case size == 1:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(r, ext); err != nil {
				return nil, err
			}
			size = binary.BigEndian.Uint64(ext)
			if size < 16 {
				return nil, fmt.Errorf("bad fmp4 box %s", typ)
			}
			payloadSize = size - 16
			out = append(out, hdr...)
			out = append(out, ext...)
		case size < 8:
			return nil, fmt.Errorf("bad fmp4 box %s", typ)
		default:
			payloadSize = size - 8
			out = append(out, hdr...)
		}
		if payloadSize > hlsInitMaxBytes {
			return nil, fmt.Errorf("fmp4 box %s too large", typ)
		}
		payload := make([]byte, payloadSize)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
		out = append(out, payload...)
		if len(out) > hlsInitMaxBytes {
			return nil, fmt.Errorf("hls init too large")
		}
		if typ == "moov" {
			return out, nil
		}
	}
}

func hlsInitFilePath(n config.Node, vhost, app, stream string) string {
	base := mediaRootOf(n)
	if n.EnableVhost && vhost != "" && vhost != "__defaultVhost__" {
		return filepath.Join(base, vhost, app, stream, "init.mp4")
	}
	return filepath.Join(base, app, stream, "init.mp4")
}

func nodePlayBase(n config.Node) string {
	if u, err := url.Parse(strings.TrimSpace(n.API)); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return fmt.Sprintf("http://127.0.0.1:%d", nz(n.HTTPPort, 8090))
}

func liveFmp4URL(n config.Node, vhost, app, stream string) string {
	key := app + "/" + stream
	if vhost != "" && vhost != "__defaultVhost__" {
		key = vhost + "/" + app + "/" + stream
	}
	return strings.TrimRight(nodePlayBase(n), "/") + "/" + key + ".live.mp4"
}

func writeHlsInitFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (h *Hub) EnsureHLSInit(nodeID, vhost, app, stream string) map[string]any {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		return map[string]any{"code": -1, "msg": "unknown node"}
	}
	ApplyZLMIni(&n)
	app, stream = strings.TrimSpace(app), strings.TrimSpace(stream)
	if app == "" || stream == "" {
		return map[string]any{"code": -1, "msg": "missing app/stream"}
	}
	path := hlsInitFilePath(n, vhost, app, stream)
	key := nodeID + "|" + app + "|" + stream
	if _, loaded := hlsInitBusy.LoadOrStore(key, true); loaded {
		return map[string]any{"code": 0, "path": path, "msg": "hls init 正在生成"}
	}
	defer hlsInitBusy.Delete(key)

	src := liveFmp4URL(n, vhost, app, stream)
	req, err := http.NewRequest(http.MethodGet, src, nil)
	if err != nil {
		return map[string]any{"code": -1, "msg": err.Error()}
	}
	resp, err := hlsInitHTTP.Do(req)
	if err != nil {
		return map[string]any{"code": -1, "msg": "拉 HTTP-fMP4 失败: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return map[string]any{"code": -1, "msg": fmt.Sprintf("拉 HTTP-fMP4 HTTP %d", resp.StatusCode)}
	}
	data, err := extractFmp4Init(io.LimitReader(resp.Body, hlsInitMaxBytes+8))
	if err != nil {
		return map[string]any{"code": -1, "msg": "提取 init.mp4 失败: " + err.Error()}
	}
	if err := writeHlsInitFile(path, data); err != nil {
		return map[string]any{"code": -1, "msg": err.Error()}
	}
	logger.Infor("hls init wrote %s bytes=%d src=%s", path, len(data), src)
	return map[string]any{"code": 0, "path": path, "bytes": len(data), "msg": "已写入 init.mp4"}
}

func (h *Hub) EnsureHLSInitAll() {
	if h == nil || config.C == nil {
		return
	}
	for _, n := range append([]config.Node(nil), config.C.Nodes...) {
		v, err := h.zlm.call(n, "getMediaList", nil)
		if err != nil || v == nil {
			continue
		}
		seen := map[string]bool{}
		for _, row := range asSlice(v["data"]) {
			app, stream := asString(row["app"]), asString(row["stream"])
			if app == "" || stream == "" {
				continue
			}
			key := n.ID + "|" + app + "|" + stream
			if seen[key] {
				continue
			}
			seen[key] = true
			out := h.EnsureHLSInit(n.ID, asString(row["vhost"]), app, stream)
			if asFloat(out["code"]) != 0 {
				logger.Warnf("hls init %s/%s: %v", app, stream, out["msg"])
			}
		}
	}
}
