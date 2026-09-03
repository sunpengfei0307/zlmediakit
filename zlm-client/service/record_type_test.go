package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"zlm-admin/core/config"
)

func TestZlmRecordType(t *testing.T) {
	got, err := zlmRecordType("hls", "")
	if err != nil || got != "0" {
		t.Fatalf("hls -> %s %v", got, err)
	}
	got, err = zlmRecordType("mp4", "")
	if err != nil || got != "1" {
		t.Fatalf("mp4 -> %s %v", got, err)
	}
	if _, err := zlmRecordType("flv", ""); err == nil {
		t.Fatal("flv should be rejected")
	}
	if _, err := zlmRecordType("", "flv"); err == nil {
		t.Fatal("type=flv should be rejected")
	}
	got, err = zlmRecordType("", "0")
	if err != nil || got != "0" {
		t.Fatalf("type=0 -> %s %v", got, err)
	}
}

func TestMediaLookupRestoresAbs(t *testing.T) {
	n := config.Node{Root: "/opt/zlm"}
	cands := mediaLookupPaths(n, "data/zlm/mp4/record/live/cam/a.mp4")
	want := filepath.Clean("/data/zlm/mp4/record/live/cam/a.mp4")
	found := false
	for _, p := range cands {
		if p == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("missing reconstructed abs %s in %v", want, cands)
	}
}

func TestMediaPublicPathAbsUsesFileQuery(t *testing.T) {
	got := mediaPublicPath("zlm-1", "/data/zlm/mp4/a.mp4")
	if !strings.Contains(got, "/file?path=") {
		t.Fatalf("abs path should use file query: %s", got)
	}
	got = mediaPublicPath("zlm-1", "www/live/a.mp4")
	if !strings.Contains(got, "/media/www/live/") {
		t.Fatalf("rel path should use media: %s", got)
	}
}

func TestResolveMediaFileRelAndAbs(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "mp4", "record", "live", "cam", "a.mp4")
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fp, []byte("ftyp"), 0644); err != nil {
		t.Fatal(err)
	}
	n := config.Node{Root: dir, MP4Save: filepath.Join(dir, "mp4")}
	got, err := resolveMediaFile(n, "mp4/record/live/cam/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if got != fp && filepath.Clean(got) != filepath.Clean(fp) {
		t.Fatalf("rel got %s want %s", got, fp)
	}
	got, err = resolveMediaFile(n, fp)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(got) != filepath.Clean(fp) {
		t.Fatalf("abs got %s want %s", got, fp)
	}
}
