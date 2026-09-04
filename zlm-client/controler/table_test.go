package controler

import (
	"net/url"
	"strings"
	"testing"
	"zlm-admin/service"
)

func TestPaginateAndSortMediaFiles(t *testing.T) {
	files := []service.MediaFile{
		{Name: "b.mp4", Path: "b", Size: 20, ModTime: "2026-09-01 12:00:00", Role: "rec_mp4", Ext: ".mp4", DurationSec: 8},
		{Name: "a.mp4", Path: "a", Size: 10, ModTime: "2026-09-01 13:00:00", Role: "rec_hls", Ext: ".m3u8", DurationSec: 20},
		{Name: "c.ts", Path: "c", Size: 30, ModTime: "2026-09-01 11:00:00", Role: "rec_hls", Ext: ".ts", DurationSec: 3},
	}
	byName := sortMediaFiles(append([]service.MediaFile{}, files...), "name", "asc")
	if byName[0].Name != "a.mp4" || byName[2].Name != "c.ts" {
		t.Fatalf("name sort: %+v", byName)
	}
	byDur := sortMediaFiles(append([]service.MediaFile{}, files...), "dur", "desc")
	if byDur[0].Name != "a.mp4" || byDur[2].Name != "c.ts" {
		t.Fatalf("duration sort: %+v", byDur)
	}
	filtered := filterMediaFiles(files, "hls")
	if len(filtered) != 2 {
		t.Fatalf("filter hls got %d", len(filtered))
	}
	page, p, size := paginateMediaFiles(byName, 2, 2)
	if p != 2 || size != 2 || len(page) != 1 || page[0].Name != "c.ts" {
		t.Fatalf("page=%d size=%d files=%+v", p, size, page)
	}
}

func TestFilterMediaFilesByPanelKeepsRecordAndEventApart(t *testing.T) {
	files := []service.MediaFile{
		{Name: "2026-09-03-11-14-50-0.mp4", Role: "rec_mp4", Ext: ".mp4"},
		{Name: "event-live-cam-20260903-111450.mp4", Role: "rec_event", Ext: ".mp4"},
		{Name: "index.m3u8", Role: "rec_hls", Ext: ".m3u8"},
	}
	rec := filterMediaFilesByPanel(files, "record")
	if len(rec) != 2 || rec[0].Role != "rec_mp4" || rec[1].Role != "rec_hls" {
		t.Fatalf("record panel=%+v", rec)
	}
	ev := filterMediaFilesByPanel(files, "event")
	if len(ev) != 1 || ev[0].Role != "rec_event" {
		t.Fatalf("event panel=%+v", ev)
	}
	if got := filterMediaFilesByPanel(files, ""); len(got) != 2 {
		t.Fatalf("default panel should be ordinary recordings: %+v", got)
	}
}

func TestParseStreamSID(t *testing.T) {
	node, vhost, app, stream, ok := parseStreamSID("zlm-1|__defaultVhost__|live|ls_zlm_h264_1080p")
	if !ok || node != "zlm-1" || vhost != "__defaultVhost__" || app != "live" || stream != "ls_zlm_h264_1080p" {
		t.Fatalf("got %s %s %s %s ok=%v", node, vhost, app, stream, ok)
	}
	if _, _, _, _, ok := parseStreamSID("bad"); ok {
		t.Fatal("short sid must fail")
	}
}

func TestBuildPagerAndWithQuery(t *testing.T) {
	q := url.Values{"app": {"live"}, "sort": {"mtime"}, "dir": {"desc"}}
	got := withQuery("/files", q, "panel", "event", "page", "1")
	for _, part := range []string{"/files?", "app=live", "panel=event", "page=1", "sort=mtime"} {
		if !strings.Contains(got, part) {
			t.Fatalf("withQuery missing %q in %s", part, got)
		}
	}
	pager := buildPager("/files", q, 45, 2, 20)
	if pager.Page != 2 || pager.Pages != 3 || pager.Total != 45 || pager.PrevURL == "" || pager.NextURL == "" {
		t.Fatalf("pager: %+v", pager)
	}
	if pager.Path != "/files" || pager.Query.Get("app") != "live" || len(pager.SizeOpts) != 3 {
		t.Fatalf("pager extras: path=%s query=%v opts=%+v", pager.Path, pager.Query, pager.SizeOpts)
	}
	if !pager.SizeOpts[0].On || pager.SizeOpts[0].N != 20 {
		t.Fatalf("default size opt: %+v", pager.SizeOpts)
	}
	wide := buildPager("/files", q, 1843, 14, 50)
	if wide.Pages != 37 || wide.Size != 50 || !wide.SizeOpts[1].On {
		t.Fatalf("size=50 pager: %+v", wide)
	}
	sortURL := sortHeaderURL("/files", q, "mtime", "mtime", "desc")
	for _, part := range []string{"sort=mtime", "dir=asc", "page=1"} {
		if !strings.Contains(sortURL, part) {
			t.Fatalf("sort toggle missing %q in %s", part, sortURL)
		}
	}
}

func TestSortStreamMapsByViewers(t *testing.T) {
	rows := []map[string]any{
		{"app": "live", "stream": "a", "totalReaderCount": 1},
		{"app": "live", "stream": "b", "totalReaderCount": 9},
	}
	got := sortStreamMaps(rows, "viewers", "desc")
	if asStr(got[0]["stream"]) != "b" {
		t.Fatalf("viewers desc: %+v", got)
	}
}

func TestSessionsSortAndGroupByAssociatedStream(t *testing.T) {
	rows := []map[string]any{
		{"id": "3", "role": "HTTP", "media_key": ""},
		{"id": "2", "role": "拉流", "app": "live", "stream": "cam", "media_key": "live/cam"},
		{"id": "1", "role": "推流", "app": "live", "stream": "cam", "media_key": "live/cam"},
		{"id": "4", "role": "拉流", "media_key": "vod/clip"},
	}
	sorted := sortSessionMaps(append([]map[string]any{}, rows...), "media", "asc")
	if asStr(sorted[0]["id"]) != "1" || asStr(sorted[1]["id"]) != "2" || asStr(sorted[2]["id"]) != "4" || asStr(sorted[3]["id"]) != "3" {
		t.Fatalf("order=%v", sorted)
	}
	groups := groupSessionsByMedia(sorted)
	if len(groups) != 3 || asStr(groups[0]["Key"]) != "live/cam" || asStr(groups[1]["Key"]) != "vod/clip" || asStr(groups[2]["Key"]) != "未关联" {
		t.Fatalf("groups=%+v", groups)
	}
	if groups[0]["Count"] != 2 {
		t.Fatalf("live/cam count=%v", groups[0]["Count"])
	}
}
