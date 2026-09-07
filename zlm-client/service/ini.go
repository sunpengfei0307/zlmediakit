package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"zlm-admin/core/config"
	"zlm-admin/core/logger"

	"gopkg.in/ini.v1"
)

func ApplyZLMIni(n *config.Node) {
	if n == nil || n.INI == "" {
		return
	}
	cfg, err := ini.LoadSources(ini.LoadOptions{
		IgnoreInlineComment:       true,
		UnescapeValueDoubleQuotes: true,
		SpaceBeforeInlineComment:  true,
		AllowBooleanKeys:          true,
		Insensitive:               true,
		SkipUnrecognizableLines:   true,
		AllowNonUniqueSections:    true,
	}, n.INI)
	if err != nil {
		logger.Warnf("parse zlm ini %s failed: %v", n.INI, err)
		return
	}
	if sec, err := cfg.GetSection("api"); err == nil {
		if v := sec.Key("secret").String(); v != "" {
			n.Secret = v
		}
	}
	for _, sec := range cfg.Sections() {
		if !strings.EqualFold(sec.Name(), "http") {
			continue
		}
		if p, err := sec.Key("port").Int(); err == nil && p > 0 {
			n.HTTPPort = p
			n.API = fmt.Sprintf("http://127.0.0.1:%d", p)
		}
		if p, err := sec.Key("sslport").Int(); err == nil && p > 0 {
			n.HTTPSPort = p
		}
	}
	if sec, err := cfg.GetSection("rtc"); err == nil {
		if p, err := sec.Key("port").Int(); err == nil && p > 0 {
			n.WebRTCPort = p
		}
	}
	if sec, err := cfg.GetSection("sip"); err == nil {
		if p, err := sec.Key("port").Int(); err == nil && p > 0 {
			n.SipPort = p
		}
	}
	base := n.Root
	if base == "" && n.INI != "" {
		base = filepath.Dir(n.INI)
		n.Root = base
	}
	abs := func(p string) string {
		p = strings.TrimSpace(p)
		if p == "" {
			return ""
		}
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		if base == "" {
			return filepath.Clean(p)
		}
		return filepath.Clean(filepath.Join(base, p))
	}
	if n.Bin == "" && base != "" {
		n.Bin = filepath.Join(base, "MediaServer")
	}
	if sec, err := cfg.GetSection("http"); err == nil {
		if v := sec.Key("rootpath").String(); v != "" {
			n.WWW = abs(v)
		}
	}
	if sec, err := cfg.GetSection("protocol"); err == nil {
		if v := sec.Key("mp4_save_path").String(); v != "" {
			n.MP4Save = abs(v)
		}
		if v := sec.Key("hls_save_path").String(); v != "" {
			n.HLSSave = abs(v)
		}
	}
	if sec, err := cfg.GetSection("ffmpeg"); err == nil {
		if v := strings.TrimSpace(sec.Key("bin").String()); v != "" {
			n.FFmpeg = abs(v)
		}
	}
	if sec, err := cfg.GetSection("general"); err == nil {
		if k := sec.Key("enablevhost"); k != nil && strings.TrimSpace(k.String()) != "" {
			n.EnableVhost = k.MustBool(false)
		}
	}
	if sec, err := cfg.GetSection("hls"); err == nil {
		if v, err := sec.Key("deletedelaysec").Int(); err == nil {
			n.LiveKeepSec = ClampLiveKeepSec(v)
		}
	}
	if n.WWW == "" {
		n.WWW = "/data/zlm"
	}
}

func persistBlockedZLMKeys(iniPath string, kv map[string]string) error {
	if strings.TrimSpace(iniPath) == "" || len(kv) == 0 {
		return nil
	}
	blocked := map[string]string{}
	for k, v := range kv {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "ffmpeg.bin" || lk == "ffmpeg.snap" {
			blocked[lk] = v
		}
	}
	if len(blocked) == 0 {
		return nil
	}
	return writeZLMIniKeys(iniPath, blocked)
}

func writeZLMIniKeys(iniPath string, kv map[string]string) error {
	raw, err := os.ReadFile(iniPath)
	if err != nil {
		return err
	}
	text := string(raw)
	for k, v := range kv {
		sec, key := k, k
		if i := strings.Index(k, "."); i > 0 {
			sec, key = k[:i], k[i+1:]
		}
		next, ok := replaceINIKey(text, sec, key, v)
		if !ok {
			next = appendINIKey(text, sec, key, v)
		}
		text = next
	}
	return os.WriteFile(iniPath, []byte(text), 0o644)
}

func replaceINIKey(text, section, key, value string) (string, bool) {
	lines := strings.Split(text, "\n")
	sec := "[" + section + "]"
	in := false
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") && strings.HasSuffix(trim, "]") {
			in = strings.EqualFold(trim, sec)
			continue
		}
		if !in || trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, ";") {
			continue
		}
		name, _, ok := strings.Cut(trim, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), key) {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		lines[i] = indent + key + "=" + value
		return strings.Join(lines, "\n"), true
	}
	return text, false
}

func appendINIKey(text, section, key, value string) string {
	block := "[" + section + "]\n" + key + "=" + value + "\n"
	if strings.TrimSpace(text) == "" {
		return block
	}
	sec := "[" + section + "]"
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.EqualFold(strings.TrimSpace(line), sec) {
			continue
		}
		insert := i + 1
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:insert]...)
		out = append(out, key+"="+value)
		out = append(out, lines[insert:]...)
		return strings.Join(out, "\n")
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + "\n" + block
}

func applyLocalFFmpegBin(nodeID string, kv map[string]string) {
	bin := ""
	for k, v := range kv {
		if strings.EqualFold(strings.TrimSpace(k), "ffmpeg.bin") {
			bin = strings.TrimSpace(v)
			break
		}
	}
	if bin == "" {
		return
	}
	ffmpegBin = ""
	if config.C == nil {
		return
	}
	config.C.Basic.FFmpeg = bin
	for i := range config.C.Nodes {
		if nodeID == "" || config.C.Nodes[i].ID == nodeID {
			config.C.Nodes[i].FFmpeg = bin
		}
	}
}
