package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeMediaJPEGUsesImageContentType(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "latest.jpg")
	if err := os.WriteFile(p, []byte{0xff, 0xd8, 0xff, 0xd9}, 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/file", nil)
	serveMedia(rec, req, "zlm-1", p, f, st)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "image/jpeg") {
		t.Fatalf("Content-Type=%q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("Cache-Control=%q", rec.Header().Get("Cache-Control"))
	}
}
