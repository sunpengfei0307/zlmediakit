package service

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"zlm-admin/core/config"
)

func TestExtractFmp4InitStopsAfterMoov(t *testing.T) {
	ftyp := fmp4Box("ftyp", []byte("isom"))
	moov := fmp4Box("moov", bytes.Repeat([]byte{1}, 24))
	moof := fmp4Box("moof", bytes.Repeat([]byte{2}, 16))
	src := append(append(append([]byte{}, ftyp...), moov...), moof...)
	got, err := extractFmp4Init(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, ftyp...), moov...)
	if !bytes.Equal(got, want) {
		t.Fatalf("init len=%d want=%d", len(got), len(want))
	}
}

func TestExtractFmp4InitRejectsTruncatedMoov(t *testing.T) {
	ftyp := fmp4Box("ftyp", []byte("isom"))
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], 100)
	copy(hdr[4:], "moov")
	_, err := extractFmp4Init(io.MultiReader(bytes.NewReader(ftyp), bytes.NewReader(hdr), bytes.NewReader([]byte{1, 2})))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHlsInitFilePathUsesMediaRoot(t *testing.T) {
	n := config.Node{WWW: "/data/zlm"}
	got := hlsInitFilePath(n, "__defaultVhost__", "live", "cam")
	want := filepath.Join("/data/zlm", "live", "cam", "init.mp4")
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestEnsureHLSInitWritesPublicInitFile(t *testing.T) {
	ftyp := fmp4Box("ftyp", []byte("isom"))
	moov := fmp4Box("moov", bytes.Repeat([]byte{9}, 32))
	payload := append(append(append([]byte{}, ftyp...), moov...), fmp4Box("moof", []byte{7})...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/live/cam.live.mp4" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	root := t.TempDir()
	old := config.C
	config.C = &config.Setup{Nodes: []config.Node{{
		ID: "node-1", API: srv.URL, HTTPPort: 8090, WWW: root, Root: root,
	}}}
	t.Cleanup(func() { config.C = old })

	h := &Hub{}
	out := h.EnsureHLSInit("node-1", "__defaultVhost__", "live", "cam")
	if asFloat(out["code"]) != 0 {
		t.Fatalf("ensure failed: %+v", out)
	}
	path := filepath.Join(root, "live", "cam", "init.mp4")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := append(append([]byte{}, ftyp...), moov...)
	if !bytes.Equal(got, want) {
		t.Fatalf("wrote %d bytes want %d", len(got), len(want))
	}
	info, _ := os.Stat(path)
	if info != nil && time.Since(info.ModTime()) > time.Minute {
		t.Fatal("init.mp4 mtime unexpected")
	}
}

func fmp4Box(typ string, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf[:4], uint32(8+len(payload)))
	copy(buf[4:8], typ)
	copy(buf[8:], payload)
	return buf
}
