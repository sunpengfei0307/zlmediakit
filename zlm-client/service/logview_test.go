package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"zlm-admin/core/config"
	"zlm-admin/model"
)

func TestParseLogFileID(t *testing.T) {
	src, name, err := parseLogFileID("")
	if err != nil || src != "" || name != "" {
		t.Fatalf("empty: %q %q %v", src, name, err)
	}
	src, name, err = parseLogFileID("zlm-admin.log")
	if err != nil || src != logSrcClient || name != "zlm-admin.log" {
		t.Fatalf("plain: %q %q %v", src, name, err)
	}
	src, name, err = parseLogFileID("server/2026-09-04_00.log")
	if err != nil || src != logSrcServer || name != "2026-09-04_00.log" {
		t.Fatalf("server: %q %q %v", src, name, err)
	}
	if _, _, err = parseLogFileID("server/../etc/passwd"); err == nil {
		t.Fatal("traversal should fail")
	}
	if _, _, err = parseLogFileID("other/a.log"); err == nil {
		t.Fatal("unknown source should fail")
	}
}

func TestLogRoots(t *testing.T) {
	prev := config.C
	config.C = &config.Setup{Loger: config.Loger{Path: "/data/zlm/log"}}
	t.Cleanup(func() { config.C = prev })

	c, s := logRoots(config.Node{LogDir: "/data/zlm/log"})
	if c != "/data/zlm/log" || filepath.ToSlash(s) != "/data/zlm/log/zlm-server" {
		t.Fatalf("root=%s server=%s", c, s)
	}
	c, s = logRoots(config.Node{LogDir: "/data/zlm/log/zlm-server"})
	if filepath.ToSlash(c) != "/data/zlm/log" || filepath.ToSlash(s) != "/data/zlm/log/zlm-server" {
		t.Fatalf("nested root=%s server=%s", c, s)
	}
}

func TestResolveLogFileDefaultsClientLatest(t *testing.T) {
	dir := t.TempDir()
	client := dir
	server := filepath.Join(dir, "zlm-server")
	if err := os.MkdirAll(server, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(p string, age time.Duration) {
		if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(-age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(client, "gin.log"), time.Hour)
	write(filepath.Join(client, "zlm-admin.log"), time.Minute)
	write(filepath.Join(server, "ffmpeg.log"), time.Second)

	p, src, name, _, err := resolveLogFile(client, server, "", "")
	if err != nil || src != logSrcClient || name != "zlm-admin.log" {
		t.Fatalf("default client latest: path=%s src=%s name=%s err=%v", p, src, name, err)
	}
	p, src, name, _, err = resolveLogFile(client, server, "", logSrcServer)
	if err != nil || src != logSrcServer || name != "ffmpeg.log" {
		t.Fatalf("default server: path=%s src=%s name=%s err=%v", p, src, name, err)
	}
	files := append(tagLogFiles(logSrcClient, client), tagLogFiles(logSrcServer, server)...)
	if len(files) != 3 {
		t.Fatalf("files=%+v", files)
	}
	var got model.LogFileInfo
	for _, f := range files {
		if f.ID == "client/zlm-admin.log" {
			got = f
		}
	}
	if got.Source != logSrcClient || got.Name != "zlm-admin.log" {
		t.Fatalf("tagged=%+v", got)
	}
}
