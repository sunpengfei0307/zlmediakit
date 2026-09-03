package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
	"zlm-admin/core/config"
)

const (
	SessCookie    = "zlm_admin"
	sessDays      = 14
	defaultUser   = "admin"
	defaultPass   = "rEDnoOH3TYZ4NNxmgy4PkSLiyzVTaR7r"
	sessVersion   = "v1"
)

func AdminUser() string {
	return defaultUser
}

func AdminPass() string {
	if config.C != nil && len(config.C.Nodes) > 0 {
		n := config.C.Nodes[0]
		ApplyZLMIni(&n)
		if s := strings.TrimSpace(n.Secret); s != "" {
			return s
		}
	}
	return defaultPass
}

func sessKey() []byte {
	s := ""
	if config.C != nil {
		s = strings.TrimSpace(config.C.Basic.Skey)
	}
	if s == "" {
		s = "zlm-admin|" + AdminPass()
	}
	return []byte(s)
}

func IssueSession(user string, now time.Time) (token string, exp time.Time) {
	user = strings.TrimSpace(user)
	if user == "" {
		user = AdminUser()
	}
	exp = now.Add(sessDays * 24 * time.Hour)
	payload := sessVersion + "." + user + "." + strconv.FormatInt(exp.Unix(), 10)
	mac := hmac.New(sha256.New, sessKey())
	_, _ = mac.Write([]byte(payload))
	return payload + "." + hex.EncodeToString(mac.Sum(nil)), exp
}

func ParseSession(token string, now time.Time) (user string, ok bool) {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != sessVersion {
		return "", false
	}
	payload := parts[0] + "." + parts[1] + "." + parts[2]
	want, err := hex.DecodeString(parts[3])
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, sessKey())
	_, _ = mac.Write([]byte(payload))
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || now.Unix() > exp {
		return "", false
	}
	if parts[1] != AdminUser() {
		return "", false
	}
	return parts[1], true
}

func CheckLogin(user, pass string) error {
	u := []byte(strings.TrimSpace(user))
	p := []byte(pass)
	wantU := []byte(AdminUser())
	wantP := []byte(AdminPass())
	if subtle.ConstantTimeCompare(u, wantU) != 1 || subtle.ConstantTimeCompare(p, wantP) != 1 {
		return fmt.Errorf("用户或密码错误")
	}
	return nil
}

func AuthSkip(path string) bool {
	switch path {
	case "/login", "/logout", "/admin.crt", "/hls.min.js", "/mpegts.min.js", "/favicon.ico":
		return true
	}
	if strings.HasPrefix(path, "/static/") {
		return true
	}
	if strings.HasPrefix(path, "/hook/") || strings.HasPrefix(path, "/index/hook/") {
		return true
	}
	return false
}
