package service

import "testing"

func TestClassifyRelDashVsHLS(t *testing.T) {
	_, _, _, _, _, proto := classifyRel("www/live/cam/hls.fmp4.m3u8", "hls.fmp4.m3u8", ".m3u8")
	if proto != "hls" {
		t.Fatalf("hls.fmp4.m3u8 proto=%s", proto)
	}
	_, _, _, _, _, proto = classifyRel("www/live/cam/init.mp4", "init.mp4", ".mp4")
	if proto != "hls" {
		t.Fatalf("init.mp4 proto=%s", proto)
	}
	_, _, _, _, _, proto = classifyRel("www/live/cam/seg-1.m4s", "seg-1.m4s", ".m4s")
	if proto != "hls" {
		t.Fatalf("hls m4s proto=%s", proto)
	}
	_, _, _, _, _, proto = classifyRel("www/live/cam/chunk-stream0-00186.m4s", "chunk-stream0-00186.m4s", ".m4s")
	if proto != "dash" {
		t.Fatalf("dash chunk proto=%s", proto)
	}
	_, _, _, _, _, proto = classifyRel("www/live/cam/stream0/chunk-stream0-00001.m4s", "chunk-stream0-00001.m4s", ".m4s")
	if proto != "dash" {
		t.Fatalf("nested dash chunk proto=%s", proto)
	}
	role, _, _, _, _, proto := classifyRel("www/record/live/cam/2026-08-20/a.mp4", "a.mp4", ".mp4")
	if proto != "record" || role != "rec_mp4" {
		t.Fatalf("record got role=%s proto=%s", role, proto)
	}
	role, _, app, stream, _, proto := classifyRel("mp4/rec/live/cam/event-live-cam-20260901-113000.mp4", "event-live-cam-20260901-113000.mp4", ".mp4")
	if proto != "record" || role != "rec_event" || app != "live" || stream != "cam" {
		t.Fatalf("event clip got role=%s proto=%s app=%s stream=%s", role, proto, app, stream)
	}
}

func TestMatchRoleSeparatesOrdinaryRecordFromEvent(t *testing.T) {
	if !matchRole("rec_mp4", "record") || !matchRole("rec_hls", "record") || matchRole("rec_event", "record") {
		t.Fatal("ordinary record filter must not include event clips")
	}
	if !matchRole("rec_event", "event") || matchRole("rec_mp4", "event") {
		t.Fatal("event filter must only include event clips")
	}
}

func TestReclassifyKeepsDashOutOfHTTPMP4(t *testing.T) {
	files := []MediaFile{
		{Name: "hls.fmp4.m3u8", Ext: ".m3u8", Dir: "www/live/cam", Path: "www/live/cam/hls.fmp4.m3u8", Proto: "hls", Role: "live_hls"},
		{Name: "init.mp4", Ext: ".mp4", Dir: "www/live/cam", Path: "www/live/cam/init.mp4", Proto: "fmp4", Role: "live_fmp4"},
		{Name: "chunk-stream0-00186.m4s", Ext: ".m4s", Dir: "www/live/cam", Path: "www/live/cam/chunk-stream0-00186.m4s", Proto: "fmp4", Role: "live_fmp4"},
		{Name: "init-stream0.m4s", Ext: ".m4s", Dir: "www/live/cam", Path: "www/live/cam/init-stream0.m4s", Proto: "fmp4", Role: "live_fmp4"},
	}
	reclassifyByDir(files)
	got := map[string]string{}
	for _, f := range files {
		got[f.Name] = f.Proto
	}
	if got["init.mp4"] != "hls" {
		t.Fatalf("init.mp4 proto=%s", got["init.mp4"])
	}
	if got["chunk-stream0-00186.m4s"] != "dash" || got["init-stream0.m4s"] != "dash" {
		t.Fatalf("dash files: %+v", got)
	}
}

func TestPickFMP4Parts(t *testing.T) {
	names := []string{"init.mp4", "seg-0.m4s", "seg-1.m4s", "chunk-stream0-00186.m4s", "init-stream0.m4s", "chunk-stream1-00186.m4s", "init-stream1.m4s"}
	initName, segs := pickFMP4Parts("seg-1.m4s", names)
	if initName != "init.mp4" || len(segs) != 2 {
		t.Fatalf("hls concat init=%s segs=%v", initName, segs)
	}
	initName, segs = pickFMP4Parts("chunk-stream0-00186.m4s", names)
	if initName != "init-stream0.m4s" || len(segs) != 1 || segs[0] != "chunk-stream0-00186.m4s" {
		t.Fatalf("dash0 init=%s segs=%v", initName, segs)
	}
	initName, segs = pickFMP4Parts("dash.mpd", names)
	if initName != "init-stream0.m4s" {
		t.Fatalf("mpd init=%s segs=%v", initName, segs)
	}
	nested := []string{"dash.mpd", "init-stream0.m4s", "init-stream1.m4s", "stream0/chunk-stream0-00001.m4s", "stream1/chunk-stream1-00001.m4s"}
	initName, segs = pickFMP4Parts("stream0/chunk-stream0-00001.m4s", nested)
	if initName != "init-stream0.m4s" || len(segs) != 1 || segs[0] != "stream0/chunk-stream0-00001.m4s" {
		t.Fatalf("nested dash init=%s segs=%v", initName, segs)
	}
}

func TestFileProtoOptionsNoFLV(t *testing.T) {
	// covered in controler; keep helper sanity here
	if isDashFile("http-flv.flv", "a.flv", ".flv") {
		t.Fatal("flv is not dash")
	}
}
