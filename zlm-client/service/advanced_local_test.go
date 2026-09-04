package service

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"zlm-admin/core/config"
)

func TestAdvancedDeleteRecordDirectoryRemovesStreamFolder(t *testing.T) {
	root := t.TempDir()
	mp4 := filepath.Join(root, "mp4")
	streamDir := filepath.Join(mp4, "record", "live", "ls_zlm_h264_1080p")
	dayDir := filepath.Join(streamDir, "2026-09-04")
	if err := os.MkdirAll(dayDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dayDir, "clip.mp4"), []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _, _ := newAdvancedHub(t, func(string, url.Values) string { return `{"code":0}` })
	node := config.C.Nodes[0]
	node.Root, node.MP4Save = root, mp4
	withTestNode(t, node)

	got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteRecordDir, url.Values{
		"app": {"live"}, "stream": {"ls_zlm_h264_1080p"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("stream folder delete failed: %+v", got)
	}
	if _, err := os.Stat(streamDir); !os.IsNotExist(err) {
		t.Fatalf("mp4/record/live/ls_zlm_h264_1080p still exists: %v", err)
	}
}

func TestAdvancedDeleteSnapDirectoryRemovesStreamSnapFolder(t *testing.T) {
	root := t.TempDir()
	mp4 := filepath.Join(root, "mp4")
	snapDir := filepath.Join(root, "snap", "ls_zlm_h264_1080p")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "latest.jpg"), []byte("jpeg"), 0o644); err != nil {
		t.Fatal(err)
	}
	h, _, _ := newAdvancedHub(t, func(string, url.Values) string { return `{"code":0}` })
	node := config.C.Nodes[0]
	node.Root, node.MP4Save = root, mp4
	withTestNode(t, node)

	got := h.AdvancedOperation("node-1", "admin", AdvancedDeleteSnapDir, url.Values{
		"app": {"live"}, "stream": {"ls_zlm_h264_1080p"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("snap folder delete failed: %+v", got)
	}
	if _, err := os.Stat(snapDir); !os.IsNotExist(err) {
		t.Fatalf("snap/ls_zlm_h264_1080p still exists: %v", err)
	}
}
