package controler

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"
)

func tmplFuncs() template.FuncMap {
	return template.FuncMap{
		"fmtBytes":  fmtBytes,
		"fmtBps":    fmtBps,
		"fmtDur":    fmtDur,
		"fmtGop":    fmtGop,
		"fmtTime":   fmtTime,
		"pctBar":    pctBar,
		"asStr":     asStr,
		"asFloat":   asF,
		"asInt":     asI,
		"join":      strings.Join,
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"contains":  strings.Contains,
		"formVal":   formVal,
		"roleLabel": roleLabel,
		"fillClass": fillClass,
		"portKey":   configNeedsRestartTmpl,
		"cfgSpan":   cfgSpanClass,
		"cfgCols":   cfgColsClass,
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"now":  time.Now,
		"plus": func(a, b int) int { return a + b },
		"seqNo": func(page, size, i int) int {
			if page < 1 {
				page = 1
			}
			if size <= 0 {
				size = defaultPageSize
			}
			if i < 0 {
				i = 0
			}
			return (page-1)*size + i + 1
		},
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"default": func(def, v string) string {
			if strings.TrimSpace(v) == "" {
				return def
			}
			return v
		},
		"trunc": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"withQuery": withQuery,
		"sortURL":   sortHeaderURL,
	}
}

func asStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprint(v)
	}
}

func formVal(form any, key, fallback string) string {
	key = strings.TrimSpace(key)
	switch t := form.(type) {
	case url.Values:
		if v := strings.TrimSpace(t.Get(key)); v != "" {
			return v
		}
	case map[string]string:
		if v := strings.TrimSpace(t[key]); v != "" {
			return v
		}
	}
	return fallback
}

func asF(v any) float64 {
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

func asI(v any) int { return int(asF(v)) }

func fmtBytes(v any) string {
	n := asF(v)
	switch {
	case n < 1024:
		return fmt.Sprintf("%.0f B", n)
	case n < 1048576:
		return fmt.Sprintf("%.1f KB", n/1024)
	case n < 1073741824:
		return fmt.Sprintf("%.1f MB", n/1048576)
	default:
		return fmt.Sprintf("%.2f GB", n/1073741824)
	}
}

func fmtBps(v any) string {
	bits := asF(v) * 8
	switch {
	case bits < 1000:
		return fmt.Sprintf("%.0f bps", bits)
	case bits < 1e6:
		return fmt.Sprintf("%.1f kbps", bits/1e3)
	default:
		return fmt.Sprintf("%.2f Mbps", bits/1e6)
	}
}

func fmtDur(v any) string {
	sec := int(asF(v))
	if sec < 0 {
		sec = 0
	}
	h, m, s := sec/3600, (sec%3600)/60, sec%60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

func fmtGop(v any) string {
	ms := asF(v)
	if ms <= 0 {
		return "-"
	}
	sec := ms / 1000
	if sec < 1 {
		return fmt.Sprintf("%.1fs", sec)
	}
	return fmt.Sprintf("%.0fs", sec)
}

func fmtTime(s string) string {
	s = strings.ReplaceAll(s, "T", " ")
	s = strings.ReplaceAll(s, "+08:00", "")
	if i := strings.LastIndex(s, "."); i > 10 {
		if z := strings.Index(s[i:], "Z"); z >= 0 {
			s = s[:i] + s[i+z:]
		} else if len(s) > i+3 {
			s = s[:i]
		}
	}
	return s
}

func pctBar(v any) float64 {
	p := asF(v)
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

func fillClass(v any) string {
	p := pctBar(v)
	if p > 90 {
		return "bad"
	}
	if p > 70 {
		return "warn"
	}
	return ""
}

func configNeedsRestartTmpl(k string) bool {
	lk := strings.ToLower(k)
	return strings.Contains(lk, ".port") || strings.HasSuffix(lk, "sslport") ||
		strings.Contains(lk, "listen_ip") || strings.HasSuffix(lk, ".sockport") ||
		strings.HasSuffix(lk, ".tcpport")
}

func roleLabel(role, ext string) string {
	if ext == ".m3u8" || ext == ".mpd" {
		return "播放列表"
	}
	switch role {
	case "rec_event":
		return "事件截录"
	case "rec_mp4":
		return "MP4 录制"
	case "rec_flv":
		return "FLV 录制"
	case "rec_hls":
		return "HLS 录制"
	case "live_hls":
		return "直播切片"
	case "live_fmp4":
		return "直播 fMP4"
	case "live_flv":
		return "FLV"
	case "live_dash":
		if ext == ".mpd" {
			return "DASH 列表"
		}
		return "DASH 切片"
	case "live_snap":
		return "封面截图"
	case "other":
		return "其他"
	}
	if ext != "" {
		return strings.TrimPrefix(ext, ".")
	}
	return "-"
}

func cfgSpanClass(key, value string) string {
	k := strings.ToLower(key)
	n := len(value)
	pathish := strings.Contains(k, "path") || strings.Contains(k, "url") || strings.Contains(k, "file") ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, "http") || strings.HasPrefix(value, "rtmp") ||
		strings.HasPrefix(value, "rtsp")
	if strings.HasSuffix(k, ".cmd") || n > 80 {
		return "cfg-span-4"
	}
	if pathish || n > 28 || strings.HasPrefix(k, "hook.") || strings.Contains(k, "secret") || strings.Contains(k, "name") {
		return "cfg-span-2"
	}
	return ""
}

func cfgColsClass(items any) string {
	n, long := 0, 0
	switch t := items.(type) {
	case []cfgItemView:
		n = len(t)
		for _, it := range t {
			if cfgSpanClass(it.Key, it.Value) != "" {
				long++
			}
		}
	}
	if n == 0 {
		return "cfg-cols-4"
	}
	if long*2 >= n {
		return "cfg-cols-2"
	}
	if n <= 9 {
		return "cfg-cols-3"
	}
	return "cfg-cols-4"
}
