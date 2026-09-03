package service

import (
	"testing"
	"time"
	"zlm-admin/model"
)

func TestHistoryWindowIncludesToday(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 5, 0, 0, time.Local)
	from, to, _, name := historyWindow("7d", now)
	if name != "7d" || !to.Equal(now) {
		t.Fatalf("7d name/to=%s %v", name, to)
	}
	if from.Year() != 2026 || from.Month() != 8 || from.Day() != 28 || from.Hour() != 0 {
		t.Fatalf("7d should start 6 days before today 00:00, got %v", from)
	}
	from, _, _, _ = historyWindow("3d", now)
	if from.Month() != 9 || from.Day() != 1 || from.Hour() != 0 {
		t.Fatalf("3d should start 2 days before today 00:00, got %v", from)
	}
	from, _, _, _ = historyWindow("1d", now)
	if from.Day() != 3 || from.Hour() != 0 {
		t.Fatalf("1d should start today 00:00, got %v", from)
	}
	from, to, _, name = historyWindow("1h", now)
	if name != "1h" || !from.Equal(now.Add(-time.Hour)) || !to.Equal(now) {
		t.Fatalf("1h window=%v %v", from, to)
	}
}

func TestHistoryQueryFilledPadsZerosAndKeepsTodaySample(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 0, 0, 0, time.Local)
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	h := &historyStore{items: []model.MetricSample{
		{T: now.Unix(), Push: 4, Pull: 2, Conn: 6, InBps: 100, OutBps: 50},
	}}
	got := h.queryFilled(from, now, time.Hour)
	if len(got) < 48 {
		t.Fatalf("3-day hourly grid too short: %d", len(got))
	}
	if got[0].T != from.Unix() || got[0].Push != 0 || got[0].InBps != 0 {
		t.Fatalf("first bucket must be zero-filled: %+v", got[0])
	}
	last := got[len(got)-1]
	if last.T != now.Unix() || last.Push != 4 || last.Pull != 2 || last.InBps != 100 {
		t.Fatalf("today sample must land on last bucket: %+v", last)
	}
}

func TestHubHistoryReturnsFilledCalendarRange(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	h := &Hub{hist: &historyStore{items: []model.MetricSample{{T: now.Unix(), Push: 1}}}}
	got := h.History("7d")
	pts, _ := got["points"].([]model.MetricSample)
	if len(pts) < 7 {
		t.Fatalf("7d points=%d", len(pts))
	}
	first := time.Unix(pts[0].T, 0).In(now.Location())
	want := today.AddDate(0, 0, -6)
	if first.Year() != want.Year() || first.YearDay() != want.YearDay() || first.Hour() != 0 {
		t.Fatalf("7d x-axis start=%v want %v", first, want)
	}
	if got["range"] != "7d" {
		t.Fatalf("range=%v", got["range"])
	}
}
