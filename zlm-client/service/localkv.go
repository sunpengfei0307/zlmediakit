package service

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.etcd.io/bbolt"
)

const (
	kvBucketMetrics = "metrics"
	kvBucketAudit   = "audit"
)

// LocalKV is a process-local, disk-backed dictionary. One file, mmap, no extra daemon.
type LocalKV struct {
	db   *bbolt.DB
	path string
}

func OpenLocalKV(path string) (*LocalKV, error) {
	if path == "" {
		return nil, fmt.Errorf("kv path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := bbolt.Open(path, 0o644, &bbolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bbolt.Tx) error {
		for _, name := range []string{kvBucketMetrics, kvBucketAudit} {
			if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &LocalKV{db: db, path: path}, nil
}

func (s *LocalKV) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *LocalKV) Sync() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Sync()
}

func (s *LocalKV) Put(bucket string, key, val []byte) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("kv is closed")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("missing bucket %s", bucket)
		}
		return b.Put(key, val)
	})
}

func (s *LocalKV) PutMany(bucket string, keys, vals [][]byte) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("kv is closed")
	}
	if len(keys) != len(vals) {
		return fmt.Errorf("put many: key/value count mismatch")
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return fmt.Errorf("missing bucket %s", bucket)
		}
		for i := range keys {
			if err := b.Put(keys[i], vals[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *LocalKV) DeleteKeys(bucket string, keys [][]byte) error {
	if s == nil || s.db == nil || len(keys) == 0 {
		return nil
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		for _, key := range keys {
			if err := b.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *LocalKV) Count(bucket string) (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	n := 0
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		n = b.Stats().KeyN
		return nil
	})
	return n, err
}

func (s *LocalKV) ForEach(bucket string, fn func(k, v []byte) error) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			return fn(append([]byte(nil), k...), append([]byte(nil), v...))
		})
	})
}
