package logger

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNextName(t *testing.T) {
	dir := t.TempDir()
	r := newRotator(filepath.Join(dir, "zlm-admin.log"), 200, 100, 30, false)
	got := r.nextName("2026-08-19")
	want := filepath.Join(dir, "zlm-admin.2026-08-19.log")
	if got != want {
		t.Fatalf("first backup: got %s want %s", got, want)
	}
	if err := os.WriteFile(want, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	got = r.nextName("2026-08-19")
	want = filepath.Join(dir, "zlm-admin.2026-08-19.1.log")
	if got != want {
		t.Fatalf("second backup: got %s want %s", got, want)
	}
}

func TestBackupPattern(t *testing.T) {
	r := newRotator("/tmp/zlm-admin.log", 200, 10, 30, false)
	re := r.backupPattern()
	cases := map[string]bool{
		"zlm-admin.log":               false,
		"zlm-admin.2026-08-19.log":    true,
		"zlm-admin.2026-08-19.1.log":  true,
		"zlm-admin.2026-08-19.12.log": true,
		"zlm-admin.2026-08-19.log.gz": true,
		"gin.2026-08-19.log":          false,
		"zlm-admin-2026-08-19T00.log": false,
	}
	for name, ok := range cases {
		if (re.FindStringSubmatch(name) != nil) != ok {
			t.Fatalf("%s match=%v want=%v", name, re.FindStringSubmatch(name) != nil, ok)
		}
	}
}

func TestSizeRotateSameDay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "zlm-admin.log")
	r := newRotator(path, 1, 10, 30, false)
	r.maxSize = 64
	chunk := bytes.Repeat([]byte("x"), 50)
	if _, err := r.Write(chunk); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write(chunk); err != nil {
		t.Fatal(err)
	}
	_ = r.Sync()
	_ = r.Close()
	day := time.Now().Format("2006-01-02")
	archived := filepath.Join(dir, "zlm-admin."+day+".log")
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("missing same-day shard %s: %v", archived, err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("active file missing: %v", err)
	}
	if st.Size() != 50 {
		t.Fatalf("active size=%d want 50", st.Size())
	}
}
