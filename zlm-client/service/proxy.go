package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"
	"zlm-admin/core/config"
)

func zlmHTTPBase(n config.Node) *url.URL {
	raw := strings.TrimSpace(n.API)
	if raw == "" {
		raw = "http://127.0.0.1:8090"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return &url.URL{Scheme: "http", Host: "127.0.0.1:8090"}
	}
	u.Path = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u
}

func livePublicPath(nodeID, rel string) string {
	rel = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/api/node/" + url.PathEscape(nodeID) + "/zlm/" + strings.Join(parts, "/")
}

func rewriteLivePlaylist(nodeID, rel string, body []byte) []byte {
	dir := path.Dir(path.Clean("/" + strings.TrimPrefix(rel, "/")))
	if dir == "/" || dir == "." {
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
				return `URI="` + resolveLiveURI(nodeID, dir, sub[1]) + `"`
			})
			continue
		}
		lines[i] = resolveLiveURI(nodeID, dir, trim)
	}
	return []byte(strings.Join(lines, "\n"))
}

func resolveLiveURI(nodeID, dir, uri string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" || strings.HasPrefix(uri, "/api/node/") {
		return uri
	}
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") {
		u, err := url.Parse(uri)
		if err != nil {
			return uri
		}
		rel := strings.TrimPrefix(u.Path, "/")
		if u.RawQuery != "" {
			return livePublicPath(nodeID, rel) + "?" + u.RawQuery
		}
		return livePublicPath(nodeID, rel)
	}
	if strings.HasPrefix(uri, "/") {
		return livePublicPath(nodeID, strings.TrimPrefix(uri, "/"))
	}
	if dir != "" {
		return livePublicPath(nodeID, path.Join(strings.TrimPrefix(dir, "/"), uri))
	}
	return livePublicPath(nodeID, uri)
}

func zlmProxyPrefix(nodeID string) string {
	return "/api/node/" + url.PathEscape(nodeID) + "/zlm"
}

func rewriteZLMCookiePath(raw, prefix string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts)+1)
	found := false
	for i, p := range parts {
		t := strings.TrimSpace(p)
		if i == 0 {
			out = append(out, t)
			continue
		}
		kv := strings.SplitN(t, "=", 2)
		if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), "domain") {
			continue
		}
		if len(kv) == 2 && strings.EqualFold(strings.TrimSpace(kv[0]), "path") {
			path := strings.TrimSpace(kv[1])
			if path == "" {
				path = "/"
			}
			if !strings.HasPrefix(path, "/") {
				path = "/" + path
			}
			if !strings.HasPrefix(path, prefix) {
				path = prefix + path
			}
			out = append(out, "Path="+path)
			found = true
			continue
		}
		out = append(out, t)
	}
	if !found {
		out = append(out, "Path="+prefix+"/")
	}
	return strings.Join(out, "; ")
}

func rewriteZLMSetCookie(nodeID string, h http.Header) {
	vals := h.Values("Set-Cookie")
	if len(vals) == 0 {
		return
	}
	h.Del("Set-Cookie")
	prefix := zlmProxyPrefix(nodeID)
	for _, c := range vals {
		h.Add("Set-Cookie", rewriteZLMCookiePath(c, prefix))
	}
}

func applyProxyCORS(h http.Header, origin string) {
	if origin == "" {
		h.Set("Access-Control-Allow-Origin", "*")
		return
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Add("Vary", "Origin")
}

func isZLMManagementPath(rel string) bool {
	decoded := strings.TrimSpace(rel)
	for i := 0; i < 4; i++ {
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return true
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	cleaned := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(decoded, "\\", "/")), "/")
	return cleaned == "index/api" || strings.HasPrefix(cleaned, "index/api/")
}

func (h *Hub) ProxyZLM(w http.ResponseWriter, r *http.Request, nodeID, rel string) {
	n, ok := h.nodeByID(nodeID)
	if !ok {
		http.Error(w, `{"code":-1,"msg":"unknown node"}`, http.StatusNotFound)
		return
	}
	rel = strings.TrimPrefix(rel, "/")
	if isZLMManagementPath(rel) {
		http.Error(w, `{"code":-1,"msg":"management API is not available through media proxy"}`, http.StatusForbidden)
		return
	}
	origin := r.Header.Get("Origin")
	target := zlmHTTPBase(n)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		applyProxyCORS(rw.Header(), origin)
		http.Error(rw, `{"code":-1,"msg":"`+err.Error()+`"}`, http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		applyProxyCORS(resp.Header, origin)
		resp.Header.Del("Content-Security-Policy")
		rewriteZLMSetCookie(nodeID, resp.Header)
		ct := strings.ToLower(resp.Header.Get("Content-Type") + " " + rel)
		if resp.Body == nil || (!strings.Contains(ct, "mpegurl") && !strings.HasSuffix(strings.ToLower(rel), ".m3u8")) {
			return nil
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if err != nil {
			return err
		}
		out := rewriteLivePlaylist(nodeID, rel, raw)
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		resp.Header.Set("Content-Type", "application/vnd.apple.mpegurl")
		resp.Header.Del("Content-Encoding")
		return nil
	}
	proxy.FlushInterval = -1
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = "/" + rel
		req.Host = target.Host
		req.Header.Set("Host", target.Host)
		req.Header.Del("Accept-Encoding")
		req.RequestURI = ""
	}
	applyProxyCORS(w.Header(), origin)
	proxy.ServeHTTP(w, r)
}
