package service

import (
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// AuditEntry describes one administrative operation.
type AuditEntry struct {
	Node        string    `json:"node"`
	User        string    `json:"user"`
	Action      string    `json:"action"`
	Target      string    `json:"target"`
	Success     bool      `json:"success"`
	Message     string    `json:"message"`
	Timestamp   time.Time `json:"time"`
	OperationID string    `json:"operation_id,omitempty"`
	Phase       string    `json:"phase,omitempty"`
}

// AuditService is the persistence boundary used by administrative operations.
type AuditService interface {
	Record(AuditEntry) error
	List() []AuditEntry
}

type kvAuditService struct {
	mu      sync.Mutex
	kv      *LocalKV
	seq     atomic.Uint64
	entries []AuditEntry
}

// NewAuditService creates a single-node, disk-backed operation audit service.
func NewAuditService(kv *LocalKV) (AuditService, error) {
	if kv == nil {
		return nil, fmt.Errorf("audit store is nil")
	}
	a := &kvAuditService{kv: kv, entries: make([]AuditEntry, 0)}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *kvAuditService) load() error {
	var entries []AuditEntry
	var maxSeq uint64
	err := a.kv.ForEach(kvBucketAudit, func(k, v []byte) error {
		e, ok := decodeAudit(v)
		if !ok {
			return nil
		}
		if seq, ok := auditSeq(k); ok && seq > maxSeq {
			maxSeq = seq
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return err
	}
	a.entries = entries
	a.seq.Store(maxSeq)
	return nil
}

func (a *kvAuditService) Record(entry AuditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	seq := a.seq.Add(1)
	if err := a.kv.Put(kvBucketAudit, auditKey(entry.Timestamp, seq), encodeAudit(entry)); err != nil {
		return err
	}
	a.entries = append(a.entries, entry)
	return nil
}

func (a *kvAuditService) List() []AuditEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AuditEntry(nil), a.entries...)
}

func auditKey(ts time.Time, seq uint64) []byte {
	key := make([]byte, 16)
	binary.BigEndian.PutUint64(key[0:8], uint64(ts.UnixNano()))
	binary.BigEndian.PutUint64(key[8:16], seq)
	return key
}

func auditSeq(key []byte) (uint64, bool) {
	if len(key) < 16 {
		return 0, false
	}
	return binary.BigEndian.Uint64(key[8:16]), true
}

func encodeAudit(e AuditEntry) []byte {
	fields := []string{e.Node, e.User, e.Action, e.Target, e.Message, e.OperationID, e.Phase}
	n := 1 + 1 + 8
	for _, s := range fields {
		n += 4 + len(s)
	}
	buf := make([]byte, n)
	buf[0] = 1
	if e.Success {
		buf[1] = 1
	}
	binary.BigEndian.PutUint64(buf[2:10], uint64(e.Timestamp.UnixNano()))
	off := 10
	for _, s := range fields {
		binary.BigEndian.PutUint32(buf[off:off+4], uint32(len(s)))
		off += 4
		copy(buf[off:], s)
		off += len(s)
	}
	return buf
}

func decodeAudit(b []byte) (AuditEntry, bool) {
	var e AuditEntry
	if len(b) < 10 || b[0] != 1 {
		return e, false
	}
	e.Success = b[1] == 1
	e.Timestamp = time.Unix(0, int64(binary.BigEndian.Uint64(b[2:10])))
	off := 10
	read := func() (string, bool) {
		if off+4 > len(b) {
			return "", false
		}
		n := int(binary.BigEndian.Uint32(b[off : off+4]))
		off += 4
		if n < 0 || off+n > len(b) {
			return "", false
		}
		s := string(b[off : off+n])
		off += n
		return s, true
	}
	var ok bool
	if e.Node, ok = read(); !ok {
		return e, false
	}
	if e.User, ok = read(); !ok {
		return e, false
	}
	if e.Action, ok = read(); !ok {
		return e, false
	}
	if e.Target, ok = read(); !ok {
		return e, false
	}
	if e.Message, ok = read(); !ok {
		return e, false
	}
	if e.OperationID, ok = read(); !ok {
		return e, false
	}
	if e.Phase, ok = read(); !ok {
		return e, false
	}
	return e, true
}
