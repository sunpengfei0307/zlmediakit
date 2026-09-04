package service

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// CfgIssue is a validation finding for a config key.
type CfgIssue struct {
	Key   string `json:"key"`
	Msg   string `json:"msg"`
	Fatal bool   `json:"fatal"`
	Field string `json:"field,omitempty"`
}

func (i CfgIssue) String() string {
	if i.Key != "" {
		return i.Key + ": " + i.Msg
	}
	if i.Field != "" {
		return i.Field + ": " + i.Msg
	}
	return i.Msg
}

func issueMsgs(issues []CfgIssue, fatalOnly bool) []string {
	out := make([]string, 0, len(issues))
	for _, it := range issues {
		if fatalOnly && !it.Fatal {
			continue
		}
		if !fatalOnly && it.Fatal {
			continue
		}
		out = append(out, it.String())
	}
	return out
}

func HasFatalCfgIssue(issues []CfgIssue) bool {
	for _, it := range issues {
		if it.Fatal {
			return true
		}
	}
	return false
}

func IssueByKey(issues []CfgIssue) map[string]string {
	out := map[string]string{}
	for _, it := range issues {
		k := it.Key
		if k == "" {
			k = it.Field
		}
		if k == "" {
			continue
		}
		if out[k] == "" {
			out[k] = it.Msg
		}
	}
	return out
}

func FormatCfgIssues(issues []CfgIssue) string {
	if len(issues) == 0 {
		return ""
	}
	if HasFatalCfgIssue(issues) {
		return "参数不合规，未保存: " + strings.Join(issueMsgs(issues, true), "；")
	}
	return "注意: " + strings.Join(issueMsgs(issues, false), "；")
}

// ValidateZLMConfig checks changed ZLM getServerConfig keys before setServerConfig.
func ValidateZLMConfig(kv map[string]string) []CfgIssue {
	var out []CfgIssue
	for k, v := range kv {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" || k == "secret" || k == "api.secret" {
			continue
		}
		out = append(out, checkZLMKey(k, v)...)
	}
	return out
}

func checkZLMKey(k, v string) []CfgIssue {
	lk := strings.ToLower(k)
	name := lk
	if i := strings.LastIndex(lk, "."); i >= 0 {
		name = lk[i+1:]
	}
	add := func(fatal bool, msg string) []CfgIssue {
		return []CfgIssue{{Key: k, Msg: msg, Fatal: fatal}}
	}
	switch {
	case name == "level" && strings.HasPrefix(lk, "log."):
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 4 {
			return add(true, "日志等级须为 0-4（0=Trace 1=Debug 2=Info 3=Warn 4=Error）")
		}
	case name == "dir" && strings.HasPrefix(lk, "log."):
		if v != "" && !looksAbs(v) {
			return add(true, "日志目录须为绝对路径，例如 /data/zlm/log/zlm-server")
		}
	case name == "modify_stamp":
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 2 {
			return add(true, "须为 0 / 1 / 2")
		}
	case isBoolishKey(name, lk):
		if v != "0" && v != "1" && !strings.EqualFold(v, "true") && !strings.EqualFold(v, "false") {
			return add(true, "须为 0 或 1")
		}
	case isPortKey(lk, name):
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 65535 {
			return add(true, "端口须为 0-65535 的整数")
		}
	case strings.HasPrefix(lk, "hook.on_") && v != "":
		low := strings.ToLower(v)
		if !strings.HasPrefix(low, "http://") && !strings.HasPrefix(low, "https://") {
			return add(true, "Hook 地址须为 http:// 或 https://，或留空关闭")
		}
		if _, err := url.ParseRequestURI(v); err != nil {
			return add(true, "Hook URL 无效")
		}
	case isUintKey(name, lk):
		if v == "" {
			return add(true, "不能为空")
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return add(true, "须为整数")
		}
		if n < 0 {
			return add(true, "不能为负数")
		}
	case isPathKey(name, lk) && v != "":
		if strings.ContainsAny(v, "\x00") {
			return add(true, "路径含非法字符")
		}
		if !looksAbs(v) && !strings.HasPrefix(v, ".") && !strings.HasPrefix(v, "~") {
			return []CfgIssue{{Key: k, Msg: "建议使用绝对路径", Fatal: false}}
		}
	}
	return nil
}

func CfgPlaceholder(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	lk := strings.ToLower(k)
	name := lk
	if i := strings.LastIndex(lk, "."); i >= 0 {
		name = lk[i+1:]
	}
	switch {
	case name == "level" && strings.HasPrefix(lk, "log."):
		return "有效范围 0-4"
	case name == "dir" && strings.HasPrefix(lk, "log."):
		return "绝对路径，改后需重启"
	case name == "modify_stamp":
		return "有效范围 0 / 1 / 2"
	case isBoolishKey(name, lk):
		return "有效范围 0 或 1"
	case isPortKey(lk, name):
		return "有效范围 0-65535"
	case strings.HasPrefix(lk, "hook.on_"):
		return "http(s) 地址，可留空"
	case name == "deletedelaysec":
		return "有效范围 0-86400 秒"
	case isUintKey(name, lk):
		return "须为非负整数"
	case isPathKey(name, lk):
		return "须为绝对路径"
	}
	return ""
}

func isBoolishKey(name, lk string) bool {
	if strings.HasPrefix(name, "enable") || strings.HasSuffix(name, "_demand") || strings.HasSuffix(name, "demand") {
		return true
	}
	switch name {
	case "apidebug", "add_mute_audio", "auto_close", "dirmenu", "allow_cross_domains",
		"faststart", "fastregister", "enablefmp4", "segkeep", "directproxy", "lowlatency":
		return true
	}
	return strings.Contains(lk, ".enable")
}

func isPortKey(lk, name string) bool {
	return name == "port" || name == "sslport" || name == "sockport" || name == "tcpport" ||
		strings.HasSuffix(lk, ".port") || strings.Contains(lk, "listen_port")
}

func isUintKey(name, lk string) bool {
	if strings.HasPrefix(lk, "hook.on_") {
		return false
	}
	if strings.HasSuffix(name, "ms") || strings.HasSuffix(name, "sec") || strings.HasSuffix(name, "second") {
		return true
	}
	switch name {
	case "maxreqsize", "maxuploadsize", "sendbufsize", "filebufsize", "unready_frame_cache",
		"keepalivesecond", "timeoutsec", "retry", "window_size", "segdur", "segnum", "segretain",
		"deletedelaysec", "mergewritems", "continue_push_ms", "paced_sender_ms", "streamnonereaderdelayms",
		"max_second", "filerepeat":
		return true
	}
	return strings.Contains(lk, "bufsize") || strings.Contains(lk, "timeout")
}

func isPathKey(name, lk string) bool {
	return strings.Contains(name, "path") || strings.HasSuffix(name, "root") ||
		name == "bin" || name == "file" || name == "dir" || strings.Contains(lk, "save_path")
}

type OpsConfig struct {
	Root, Bin, API, INI, LogDir, Base, FFmpeg string
	Persist, EnableDash, EnableSnap           bool
	LiveKeepRaw, SnapIntervalRaw              string
	CheckLiveKeep, CheckSnap                  bool
}

func looksAbs(p string) bool {
	if p == "" {
		return false
	}
	if filepath.IsAbs(p) {
		return true
	}
	return strings.HasPrefix(strings.ReplaceAll(p, "\\", "/"), "/")
}

func ValidateOpsConfig(o OpsConfig) []CfgIssue {
	var out []CfgIssue
	checkAbs := func(field, v string, required bool) {
		v = strings.TrimSpace(v)
		if v == "" {
			if required {
				out = append(out, CfgIssue{Field: field, Msg: "不能为空", Fatal: true})
			}
			return
		}
		if !looksAbs(v) {
			out = append(out, CfgIssue{Field: field, Msg: "必须是绝对路径", Fatal: true})
		}
	}
	checkAbs("root", o.Root, true)
	checkAbs("base", o.Base, true)
	if o.Bin != "" && !looksAbs(o.Bin) {
		out = append(out, CfgIssue{Field: "bin", Msg: "必须是绝对路径", Fatal: true})
	}
	if o.INI != "" && !looksAbs(o.INI) {
		out = append(out, CfgIssue{Field: "ini", Msg: "必须是绝对路径", Fatal: true})
	}
	if o.LogDir != "" && !looksAbs(o.LogDir) {
		out = append(out, CfgIssue{Field: "log_dir", Msg: "必须是绝对路径", Fatal: true})
	}
	if api := strings.TrimSpace(o.API); api != "" {
		u, err := url.Parse(api)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			out = append(out, CfgIssue{Field: "api", Msg: "须为 http(s)://host:port", Fatal: true})
		}
	}
	if o.EnableDash {
		ff := strings.TrimSpace(o.FFmpeg)
		if ff == "" {
			out = append(out, CfgIssue{Field: "ffmpeg", Msg: "开启 DASH 时必须填写 ffmpeg 路径", Fatal: true})
		} else if st, err := os.Stat(ff); err != nil {
			out = append(out, CfgIssue{Field: "ffmpeg", Msg: "ffmpeg 不存在: " + ff, Fatal: true})
		} else if st.IsDir() {
			out = append(out, CfgIssue{Field: "ffmpeg", Msg: "ffmpeg 指向了目录", Fatal: true})
		}
	}
	if p := strings.TrimSpace(o.Root); p != "" && looksAbs(p) {
		if st, err := os.Stat(p); err != nil {
			out = append(out, CfgIssue{Field: "root", Msg: "目录不存在，仍会写入本地配置", Fatal: false})
		} else if !st.IsDir() {
			out = append(out, CfgIssue{Field: "root", Msg: "不是目录", Fatal: true})
		}
	}
	if p := strings.TrimSpace(o.Bin); p != "" && looksAbs(p) {
		if st, err := os.Stat(p); err != nil {
			out = append(out, CfgIssue{Field: "bin", Msg: "文件不存在: " + p, Fatal: false})
		} else if st.IsDir() {
			out = append(out, CfgIssue{Field: "bin", Msg: "指向了目录而非 MediaServer", Fatal: true})
		}
	}
	if o.CheckLiveKeep {
		if _, err := ParseLiveKeepSecStrict(o.LiveKeepRaw); err != nil {
			out = append(out, CfgIssue{Field: "live_keep_sec", Msg: err.Error(), Fatal: true})
		}
	}
	if o.CheckSnap {
		raw := strings.TrimSpace(o.SnapIntervalRaw)
		if raw == "" {
			out = append(out, CfgIssue{Field: "snap_interval", Msg: "不能为空", Fatal: true})
		} else if n, err := strconv.Atoi(raw); err != nil {
			out = append(out, CfgIssue{Field: "snap_interval", Msg: "必须是整数秒", Fatal: true})
		} else if n < minSnapInterval || n > maxSnapInterval {
			out = append(out, CfgIssue{Field: "snap_interval", Msg: fmt.Sprintf("有效范围 %d-%d 秒", minSnapInterval, maxSnapInterval), Fatal: true})
		}
	}
	return out
}
