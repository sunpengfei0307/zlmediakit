package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, p string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestCleanUnusedMediaDirs(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"hls", "rec", "ts", "flv", "dash", "__defaultVhost__", "mp4", "snap", "live"} {
		if err := os.MkdirAll(filepath.Join(base, name, "keepme"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(base, "mp4", "record", "live", "cam", "a.mp4"), time.Now())
	writeFile(t, filepath.Join(base, "live", "cam", "hls.fmp4.m3u8"), time.Now())

	removed := CleanUnusedMediaDirs(base, false)
	if len(removed) != 6 {
		t.Fatalf("removed=%v", removed)
	}
	for _, name := range []string{"hls", "rec", "ts", "flv", "dash", "__defaultVhost__"} {
		if _, err := os.Stat(filepath.Join(base, name)); !os.IsNotExist(err) {
			t.Fatalf("%s should be gone: %v", name, err)
		}
	}
	for _, name := range []string{"mp4", "snap", "live"} {
		if _, err := os.Stat(filepath.Join(base, name)); err != nil {
			t.Fatalf("%s should stay: %v", name, err)
		}
	}
}

func TestCleanUnusedKeepsVhostWhenEnabled(t *testing.T) {
	base := t.TempDir()
	p := filepath.Join(base, "__defaultVhost__", "live")
	if err := os.MkdirAll(p, 0755); err != nil {
		t.Fatal(err)
	}
	removed := CleanUnusedMediaDirs(base, true)
	for _, r := range removed {
		if filepath.Base(r) == "__defaultVhost__" {
			t.Fatalf("vhost dir deleted while enabled: %v", removed)
		}
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}

func TestSweepMediaRootLiveVsRecord(t *testing.T) {
	base := t.TempDir()
	now := time.Now()
	old := now.Add(-20 * time.Minute)
	fresh := now.Add(-10 * time.Second)

	writeFile(t, filepath.Join(base, "hls", "stale.ts"), old)
	writeFile(t, filepath.Join(base, "mp4", "record", "live", "cam", "rec.mp4"), old)
	writeFile(t, filepath.Join(base, "snap", "cam.jpg"), old)
	writeFile(t, filepath.Join(base, "live", "dead", "old.ts"), old)
	writeFile(t, filepath.Join(base, "live", "dead", "hls.fmp4.m3u8"), old)
	writeFile(t, filepath.Join(base, "live", "cam", "init.mp4"), old)
	writeFile(t, filepath.Join(base, "live", "cam", "hls.fmp4.m3u8"), fresh)
	writeFile(t, filepath.Join(base, "live", "cam", "00001.ts"), old)
	writeFile(t, filepath.Join(base, "live", "cam", "00099.ts"), fresh)
	writeFile(t, filepath.Join(base, "live", "cam", "2026-08-01", "old.ts"), old)
	writeFile(t, filepath.Join(base, "live", "cam", "stream0", "chunk-stream0-00001.m4s"), old)

	res := SweepMediaRoot(base, false, 10*time.Minute, now)
	if _, err := os.Stat(filepath.Join(base, "hls")); !os.IsNotExist(err) {
		t.Fatal("unused hls dir should be gone")
	}
	if _, err := os.Stat(filepath.Join(base, "mp4", "record", "live", "cam", "rec.mp4")); err != nil {
		t.Fatal("recording must stay")
	}
	if _, err := os.Stat(filepath.Join(base, "snap", "cam.jpg")); err != nil {
		t.Fatal("snap must stay")
	}
	if _, err := os.Stat(filepath.Join(base, "live", "dead")); !os.IsNotExist(err) {
		t.Fatal("stale live stream dir should be gone")
	}
	if _, err := os.Stat(filepath.Join(base, "live", "cam", "init.mp4")); err != nil {
		t.Fatal("init.mp4 of active stream must stay")
	}
	if _, err := os.Stat(filepath.Join(base, "live", "cam", "00099.ts")); err != nil {
		t.Fatal("fresh live ts must stay")
	}
	if _, err := os.Stat(filepath.Join(base, "live", "cam", "00001.ts")); !os.IsNotExist(err) {
		t.Fatal("old live ts should be gone")
	}
	if _, err := os.Stat(filepath.Join(base, "live", "cam", "2026-08-01")); !os.IsNotExist(err) {
		t.Fatal("old date folder should be gone")
	}
	if res.RemovedDirs == 0 {
		t.Fatalf("expected removed dirs: %+v", res)
	}
}

func TestClampLiveKeepSec(t *testing.T) {
	if ClampLiveKeepSec(0) != 600 || ClampLiveKeepSec(-1) != 600 {
		t.Fatal("default")
	}
	if ClampLiveKeepSec(10) != 30 {
		t.Fatal("min")
	}
	if ClampLiveKeepSec(90000) != 86400 {
		t.Fatal("max")
	}
	if ParseLiveKeepSec("300") != 300 {
		t.Fatal("parse")
	}
	if _, err := ParseLiveKeepSecStrict(""); err == nil {
		t.Fatal("empty should fail")
	}
	if _, err := ParseLiveKeepSecStrict("abc"); err == nil {
		t.Fatal("nan should fail")
	}
	if _, err := ParseLiveKeepSecStrict("10"); err == nil {
		t.Fatal("below min should fail")
	}
	if n, err := ParseLiveKeepSecStrict("600"); err != nil || n != 600 {
		t.Fatalf("strict 600: %v %v", n, err)
	}
}
