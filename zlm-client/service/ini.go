package service

import (
	"fmt"
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
