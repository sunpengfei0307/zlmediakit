package service

import (
	"strings"
	"testing"
	"zlm-admin/core/config"
)

func TestDashFFmpegMediaSegName(t *testing.T) {
	n := config.Node{WWW: "/data/zlm", RTMPPort: 1935}
	out := dashOutputFile(n, "", "live", "ls_zlm_h264_1080p")
	wantOut := "/data/zlm/live/ls_zlm_h264_1080p/dash.mpd"
	if out != wantOut {
		t.Fatalf("out=%s", out)
	}
	dir, args := dashFFmpegArgs("rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p", out)
	if !strings.HasSuffix(dir, "/live/ls_zlm_h264_1080p") {
		t.Fatalf("dir=%s", dir)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "init-stream$RepresentationID$.m4s") {
		t.Fatalf("init name missing: %s", joined)
	}
	if !strings.Contains(joined, "stream$RepresentationID$/chunk-stream$RepresentationID$-$Number%05d$.m4s") {
		t.Fatalf("media name missing: %s", joined)
	}
	if !strings.Contains(joined, "extra_window_size") {
		t.Fatalf("dash extra_window_size missing: %s", joined)
	}
	if strings.Contains(joined, "delete_removed") {
		t.Fatal("delete_removed is not in FFmpeg 4.4")
	}
	cmd := dashFFmpegCmd("rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p", out)
	if !strings.Contains(cmd, "mkdir -p "+dir+"/stream0 "+dir+"/stream1") {
		t.Fatalf("mkdir missing: %s", cmd)
	}
	if !strings.Contains(cmd, " -i rtmp://127.0.0.1:1935/live/ls_zlm_h264_1080p") {
		t.Fatalf("input missing: %s", cmd)
	}
}

func TestDashStreamKeyNested(t *testing.T) {
	k, kind := dashStreamKey("stream0/chunk-stream0-00001.m4s")
	if k != "0" || kind != "chunk" {
		t.Fatalf("got key=%s kind=%s", k, kind)
	}
	k, kind = dashStreamKey(`stream1\init-stream1.m4s`)
	if k != "1" || kind != "init" {
		t.Fatalf("got key=%s kind=%s", k, kind)
	}
}

func TestDashOutputMigratesWWW(t *testing.T) {
	n := config.Node{WWW: "/data/sunpf/tools/zlm-admin/zlm-server/www"}
	got := dashOutputFile(n, "", "live", "cam")
	if got != "/data/zlm/live/cam/dash.mpd" {
		t.Fatalf("got %s", got)
	}
	n.WWW = ""
	got = dashOutputFile(n, "", "live", "cam")
	if got != "/data/zlm/live/cam/dash.mpd" {
		t.Fatalf("empty www got %s", got)
	}
	n.EnableVhost = true
	got = dashOutputFile(n, "", "live", "cam")
	if got != "/data/zlm/__defaultVhost__/live/cam/dash.mpd" {
		t.Fatalf("vhost got %s", got)
	}
}
