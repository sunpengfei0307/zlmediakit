package service

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"zlm-admin/model"
)

func TestAuditServicePersistsAndLoadsEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zlm-admin.kv")
	kv := mustOpenKV(t, path)
	audit, err := NewAuditService(kv)
	if err != nil {
		t.Fatal(err)
	}
	entry := AuditEntry{
		Node: "node-1", User: "admin", Action: "stop_stream",
		Target: "live/camera", Success: true, Message: "stopped",
	}
	if err := audit.Record(entry); err != nil {
		t.Fatal(err)
	}
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}

	loadedKV := mustOpenKV(t, path)
	defer loadedKV.Close()
	loaded, err := NewAuditService(loadedKV)
	if err != nil {
		t.Fatal(err)
	}
	entries := loaded.List()
	if len(entries) != 1 {
		t.Fatalf("entries=%d", len(entries))
	}
	got := entries[0]
	if got.Node != entry.Node || got.User != entry.User || got.Action != entry.Action ||
		got.Target != entry.Target || got.Success != entry.Success || got.Message != entry.Message {
		t.Fatalf("entry=%+v", got)
	}
	if got.Timestamp.IsZero() {
		t.Fatal("timestamp was not assigned")
	}
}

func TestAuditServiceConcurrentRecordIsSafe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zlm-admin.kv")
	kv := mustOpenKV(t, path)
	audit, err := NewAuditService(kv)
	if err != nil {
		t.Fatal(err)
	}

	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- audit.Record(AuditEntry{
				Node: "node-1", User: "admin", Action: "test",
				Target: fmt.Sprintf("target-%d", i), Success: true, Timestamp: time.Now(),
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := len(audit.List()); got != count {
		t.Fatalf("entries=%d want=%d", got, count)
	}
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := mustOpenKV(t, path)
	defer reopened.Close()
	reloaded, err := NewAuditService(reopened)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(reloaded.List()); got != count {
		t.Fatalf("persisted entries=%d want=%d", got, count)
	}
}

func TestAuditServiceIgnoresLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "operation-audit.json")
	legacy := []AuditEntry{{
		Node: "node-1", User: "admin", Action: "kick",
		Target: "sess-1", Success: true, Message: "ok",
		Timestamp: time.Unix(1710000000, 0),
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	kv := mustOpenKV(t, filepath.Join(dir, "zlm-admin.kv"))
	defer kv.Close()
	audit, err := NewAuditService(kv)
	if err != nil {
		t.Fatal(err)
	}
	if got := audit.List(); len(got) != 0 {
		t.Fatalf("legacy json should not be imported: %+v", got)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatal("legacy json must stay in place")
	}
}

func TestHistoryPersistsAndReloadsSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zlm-admin.kv")
	kv := mustOpenKV(t, path)
	h := newHistory(kv)
	sample := model.MetricSample{T: time.Now().Unix(), Push: 2, Pull: 4, Conn: 6, CPU: 1.5, Mem: 2.5, NetUtil: 3.5, NetRxBps: 8, NetTxBps: 9}
	h.add(sample)
	if err := kv.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := mustOpenKV(t, path)
	defer reopened.Close()
	loaded := newHistory(reopened)
	got := loaded.query(time.Hour, time.Second)
	if len(got) != 1 {
		t.Fatalf("samples=%d", len(got))
	}
	it := got[0]
	if it.Push != sample.Push || it.Pull != sample.Pull || it.Conn != sample.Conn ||
		it.CPU != sample.CPU || it.Mem != sample.Mem || it.NetRxBps != sample.NetRxBps || it.NetTxBps != sample.NetTxBps {
		t.Fatalf("sample=%+v", it)
	}
}

func TestDecodeMetricAcceptsLegacy60Bytes(t *testing.T) {
	old := make([]byte, 60)
	binary.BigEndian.PutUint64(old[0:8], 1700000000)
	binary.BigEndian.PutUint32(old[8:12], 2)
	binary.BigEndian.PutUint32(old[12:16], 4)
	binary.BigEndian.PutUint32(old[16:20], 6)
	binary.BigEndian.PutUint64(old[44:52], 11)
	binary.BigEndian.PutUint64(old[52:60], 22)
	got, ok := decodeMetric(old)
	if !ok || got.Push != 2 || got.Pull != 4 || got.Conn != 6 || got.NetRxBps != 11 || got.NetTxBps != 22 {
		t.Fatalf("legacy decode=%+v ok=%v", got, ok)
	}
	if got.InBps != 0 || got.OutBps != 0 {
		t.Fatalf("legacy extra fields should stay zero: %+v", got)
	}
	fresh := encodeMetric(model.MetricSample{T: 1700000001, InBps: 100, OutBps: 200, NetRxBps: 3, NetTxBps: 4})
	got, ok = decodeMetric(fresh)
	if !ok || got.InBps != 100 || got.OutBps != 200 || got.NetRxBps != 3 {
		t.Fatalf("new decode=%+v ok=%v", got, ok)
	}
}

func TestHistoryIgnoresLegacyJSON(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "metrics-history.json")
	sample := model.MetricSample{T: time.Now().Unix(), Push: 1, Pull: 2, Conn: 3, CPU: 0.2}
	raw, err := json.Marshal([]model.MetricSample{sample})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(jsonPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	kv := mustOpenKV(t, filepath.Join(dir, "zlm-admin.kv"))
	defer kv.Close()
	h := newHistory(kv)
	got := h.query(time.Hour, time.Second)
	if len(got) != 0 {
		t.Fatalf("legacy json should not be imported: %+v", got)
	}
	if _, err := os.Stat(jsonPath); err != nil {
		t.Fatal("legacy json must stay in place")
	}
}

func TestLocalKVPutGetRoundTrip(t *testing.T) {
	kv := mustOpenKV(t, filepath.Join(t.TempDir(), "zlm-admin.kv"))
	defer kv.Close()
	if err := kv.Put(kvBucketMetrics, []byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	n, err := kv.Count(kvBucketMetrics)
	if err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	var got []byte
	if err := kv.ForEach(kvBucketMetrics, func(k, v []byte) error {
		got = append([]byte(nil), v...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if string(got) != "1" {
		t.Fatalf("value=%q", got)
	}
}

func mustOpenKV(t *testing.T, path string) *LocalKV {
	t.Helper()
	kv, err := OpenLocalKV(path)
	if err != nil {
		t.Fatal(err)
	}
	return kv
}
