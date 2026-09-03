package service

import (
	"encoding/binary"
	"math"
	"sort"
	"sync"
	"time"
	"zlm-admin/model"
)

type historyStore struct {
	mu    sync.Mutex
	items []model.MetricSample
	kv    *LocalKV
}

const historyKeep = 7 * 24 * time.Hour

func newHistory(kv *LocalKV) *historyStore {
	h := &historyStore{kv: kv, items: make([]model.MetricSample, 0, 4096)}
	h.load()
	return h
}

func (h *historyStore) load() {
	if h.kv == nil {
		return
	}
	cut := time.Now().Add(-historyKeep).Unix()
	var items []model.MetricSample
	var stale [][]byte
	_ = h.kv.ForEach(kvBucketMetrics, func(k, v []byte) error {
		s, ok := decodeMetric(v)
		if !ok {
			return nil
		}
		if s.T < cut {
			stale = append(stale, k)
			return nil
		}
		items = append(items, s)
		return nil
	})
	h.items = items
	_ = h.kv.DeleteKeys(kvBucketMetrics, stale)
}

func (h *historyStore) add(s model.MetricSample) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cut := time.Now().Add(-historyKeep).Unix()
	h.items = append(h.items, s)
	i := 0
	for _, it := range h.items {
		if it.T >= cut {
			h.items[i] = it
			i++
		}
	}
	h.items = h.items[:i]
	if h.kv != nil {
		_ = h.kv.Put(kvBucketMetrics, metricKey(s.T), encodeMetric(s))
	}
}

func (h *historyStore) save() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.kv == nil {
		return
	}
	cut := time.Now().Add(-historyKeep).Unix()
	var stale [][]byte
	_ = h.kv.ForEach(kvBucketMetrics, func(k, v []byte) error {
		s, ok := decodeMetric(v)
		if !ok || s.T < cut {
			stale = append(stale, k)
		}
		return nil
	})
	_ = h.kv.DeleteKeys(kvBucketMetrics, stale)
	_ = h.kv.Sync()
}

func historyWindow(rng string, now time.Time) (from, to time.Time, step time.Duration, name string) {
	if now.IsZero() {
		now = time.Now()
	}
	now = now.In(time.Local)
	to = now
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch rng {
	case "1h", "hour":
		return now.Add(-time.Hour), now, time.Minute, "1h"
	case "3d":
		return today.AddDate(0, 0, -2), now, 10 * time.Minute, "3d"
	case "7d":
		return today.AddDate(0, 0, -6), now, 30 * time.Minute, "7d"
	default:
		return today, now, 2 * time.Minute, "1d"
	}
}

func fillMetricGrid(from, to time.Time, step time.Duration, samples []model.MetricSample) []model.MetricSample {
	if step <= 0 {
		step = time.Minute
	}
	stepSec := int64(step.Seconds())
	if stepSec < 1 {
		stepSec = 1
	}
	fromSec := from.Unix() / stepSec * stepSec
	toSec := to.Unix() / stepSec * stepSec
	if toSec < fromSec {
		toSec = fromSec
	}
	n := int((toSec-fromSec)/stepSec) + 1
	if n < 1 {
		n = 1
	}
	items := append([]model.MetricSample(nil), samples...)
	sort.Slice(items, func(i, j int) bool { return items[i].T < items[j].T })
	by := make(map[int64]model.MetricSample, len(items))
	for _, it := range items {
		if it.T < fromSec || it.T > toSec+stepSec-1 {
			continue
		}
		b := it.T / stepSec * stepSec
		it.T = b
		by[b] = it
	}
	out := make([]model.MetricSample, n)
	for i := 0; i < n; i++ {
		t := fromSec + int64(i)*stepSec
		if s, ok := by[t]; ok {
			out[i] = s
			continue
		}
		out[i] = model.MetricSample{T: t}
	}
	return out
}

func (h *historyStore) query(span time.Duration, step time.Duration) []model.MetricSample {
	h.mu.Lock()
	defer h.mu.Unlock()
	from := time.Now().Add(-span).Unix()
	if step <= 0 {
		step = 15 * time.Second
	}
	stepSec := int64(step.Seconds())
	if stepSec < 1 {
		stepSec = 1
	}
	var out []model.MetricSample
	var last model.MetricSample
	var has bool
	var bucket int64 = -1
	flush := func() {
		if has {
			out = append(out, last)
		}
	}
	for _, it := range h.items {
		if it.T < from {
			continue
		}
		b := it.T / stepSec
		if bucket != -1 && b != bucket {
			flush()
			has = false
		}
		last = it
		last.T = b * stepSec
		has = true
		bucket = b
	}
	flush()
	return out
}

func (h *historyStore) queryFilled(from, to time.Time, step time.Duration) []model.MetricSample {
	var samples []model.MetricSample
	if h != nil {
		h.mu.Lock()
		samples = append([]model.MetricSample(nil), h.items...)
		h.mu.Unlock()
	}
	return fillMetricGrid(from, to, step, samples)
}

func metricKey(t int64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, uint64(t))
	return key
}

func encodeMetric(s model.MetricSample) []byte {
	out := make([]byte, 76)
	binary.BigEndian.PutUint64(out[0:8], uint64(s.T))
	binary.BigEndian.PutUint32(out[8:12], uint32(s.Push))
	binary.BigEndian.PutUint32(out[12:16], uint32(s.Pull))
	binary.BigEndian.PutUint32(out[16:20], uint32(s.Conn))
	binary.BigEndian.PutUint64(out[20:28], math.Float64bits(s.CPU))
	binary.BigEndian.PutUint64(out[28:36], math.Float64bits(s.Mem))
	binary.BigEndian.PutUint64(out[36:44], math.Float64bits(s.NetUtil))
	binary.BigEndian.PutUint64(out[44:52], s.NetRxBps)
	binary.BigEndian.PutUint64(out[52:60], s.NetTxBps)
	binary.BigEndian.PutUint64(out[60:68], s.InBps)
	binary.BigEndian.PutUint64(out[68:76], s.OutBps)
	return out
}

func decodeMetric(b []byte) (model.MetricSample, bool) {
	var s model.MetricSample
	if len(b) < 60 {
		return s, false
	}
	s.T = int64(binary.BigEndian.Uint64(b[0:8]))
	s.Push = int(binary.BigEndian.Uint32(b[8:12]))
	s.Pull = int(binary.BigEndian.Uint32(b[12:16]))
	s.Conn = int(binary.BigEndian.Uint32(b[16:20]))
	s.CPU = math.Float64frombits(binary.BigEndian.Uint64(b[20:28]))
	s.Mem = math.Float64frombits(binary.BigEndian.Uint64(b[28:36]))
	s.NetUtil = math.Float64frombits(binary.BigEndian.Uint64(b[36:44]))
	s.NetRxBps = binary.BigEndian.Uint64(b[44:52])
	s.NetTxBps = binary.BigEndian.Uint64(b[52:60])
	if len(b) >= 76 {
		s.InBps = binary.BigEndian.Uint64(b[60:68])
		s.OutBps = binary.BigEndian.Uint64(b[68:76])
	}
	return s, true
}
