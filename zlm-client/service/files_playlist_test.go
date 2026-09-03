package service

import (
	"strings"
	"testing"
	"zlm-admin/core/config"
)

func TestResolvePlaylistURIKeepsAbsFileQuery(t *testing.T) {
	got := resolvePlaylistURI("zlm-1", "/data/zlm/live/cam", "init.mp4")
	if !strings.Contains(got, "/file?path=") || !strings.Contains(got, "%2Fdata%2Fzlm%2Flive%2Fcam%2Finit.mp4") {
		t.Fatalf("abs playlist fragment should keep /file?path=: %s", got)
	}
	got = resolvePlaylistURI("zlm-1", "/data/zlm/live/cam", "2026-09-02/11/07-32_360.mp4")
	if !strings.Contains(got, "/file?path=") || !strings.Contains(got, "07-32_360.mp4") {
		t.Fatalf("abs date-folder segment: %s", got)
	}
}

func TestRewritePlaylistAbsRewritesMapAndSeg(t *testing.T) {
	in := "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2,\n2026-09-02/11/07-32_360.mp4\n"
	out := string(rewritePlaylist("zlm-1", "/data/zlm/live/cam/hls.fmp4.m3u8", []byte(in)))
	if !strings.Contains(out, `URI="/api/node/zlm-1/file?path=`) {
		t.Fatalf("MAP not rewritten to file query:\n%s", out)
	}
	if strings.Contains(out, "/media/data/zlm/") {
		t.Fatalf("stripped abs path leaked to /media/:\n%s", out)
	}
	if !strings.Contains(out, "/api/node/zlm-1/file?path=") || !strings.Contains(out, "07-32_360.mp4") {
		t.Fatalf("segment not rewritten:\n%s", out)
	}
}

func TestLiveHLSProxyURLFromDiskPath(t *testing.T) {
	n := config.Node{ID: "zlm-1", WWW: "/data/zlm"}
	got := liveHLSProxyURL(n, "zlm-1", "/data/zlm/live/ls_zlm_h264_1080p/hls.fmp4.m3u8")
	want := "/api/node/zlm-1/zlm/live/ls_zlm_h264_1080p/hls.fmp4.m3u8"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if liveHLSProxyURL(n, "zlm-1", "/data/zlm/live/cam/vod.m3u8") != "" {
		t.Fatal("vod.m3u8 is not a live index")
	}
	if liveHLSProxyURL(n, "zlm-1", "live/cam/hls.m3u8") != "/api/node/zlm-1/zlm/live/cam/hls.m3u8" {
		t.Fatalf("relative live index: %s", liveHLSProxyURL(n, "zlm-1", "live/cam/hls.m3u8"))
	}
}

func TestAttachLiveHLSPlayURLs(t *testing.T) {
	n := config.Node{ID: "zlm-1", WWW: "/data/zlm"}
	files := []MediaFile{
		{Name: "hls.fmp4.m3u8", Dir: "/data/zlm/live/cam", Path: "/data/zlm/live/cam/hls.fmp4.m3u8", Ext: ".m3u8"},
		{Name: "seg.mp4", Dir: "/data/zlm/live/cam", Path: "/data/zlm/live/cam/seg.mp4", Ext: ".mp4"},
	}
	attachLiveHLSPlayURLs("zlm-1", n, files)
	want := "/api/node/zlm-1/zlm/live/cam/hls.fmp4.m3u8"
	if files[0].Playlist != want || files[1].Playlist != want {
		t.Fatalf("playlist=%q %q", files[0].Playlist, files[1].Playlist)
	}
}
