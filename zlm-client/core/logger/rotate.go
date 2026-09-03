package logger

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rotator struct {
	filename   string
	maxSize    int64
	maxBackups int
	maxAge     int
	compress   bool

	mu   sync.Mutex
	file *os.File
	size int64
	day  string

	millMu sync.Mutex
	millWg sync.WaitGroup
}

type backupFile struct {
	path  string
	date  string
	index int
}

func newRotator(filename string, maxSizeMB, maxBackups, maxAge int, compress bool) *rotator {
	if maxSizeMB <= 0 {
		maxSizeMB = 200
	}
	return &rotator{
		filename:   filename,
		maxSize:    int64(maxSizeMB) * 1024 * 1024,
		maxBackups: maxBackups,
		maxAge:     maxAge,
		compress:   compress,
	}
}

func (r *rotator) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ready(int64(len(p))); err != nil {
		return 0, err
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotator) Sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	return r.file.Sync()
}

func (r *rotator) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var err error
	if r.file != nil {
		err = r.file.Close()
		r.file = nil
	}
	r.millWg.Wait()
	return err
}

func today() string {
	return time.Now().Format("2006-01-02")
}

func (r *rotator) ready(add int64) error {
	now := today()
	if r.file == nil {
		if err := r.open(now); err != nil {
			return err
		}
	}
	if r.day != now {
		if r.size > 0 {
			if err := r.rotate(); err != nil {
				return err
			}
		} else {
			r.day = now
		}
	}
	if r.maxSize > 0 && r.size > 0 && r.size+add > r.maxSize {
		if err := r.rotate(); err != nil {
			return err
		}
	}
	if r.file == nil {
		return r.open(now)
	}
	return nil
}

func (r *rotator) open(day string) error {
	if err := os.MkdirAll(filepath.Dir(r.filename), 0755); err != nil {
		return err
	}
	st, err := os.Stat(r.filename)
	if err == nil && st.Size() > 0 {
		fileDay := st.ModTime().Format("2006-01-02")
		needRotate := fileDay != day || (r.maxSize > 0 && st.Size() >= r.maxSize)
		if needRotate {
			r.day = fileDay
			if err := os.Rename(r.filename, r.nextName(fileDay)); err != nil {
				return err
			}
			r.kickMill()
		}
	}
	f, err := os.OpenFile(r.filename, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	r.file = f
	r.size = info.Size()
	r.day = day
	return nil
}

func (r *rotator) rotate() error {
	if r.file != nil {
		_ = r.file.Sync()
		_ = r.file.Close()
		r.file = nil
	}
	if _, err := os.Stat(r.filename); err != nil {
		r.size = 0
		return r.open(today())
	}
	dst := r.nextName(r.day)
	if err := os.Rename(r.filename, dst); err != nil {
		return err
	}
	r.size = 0
	r.kickMill()
	return r.open(today())
}

func (r *rotator) nextName(date string) string {
	dir := filepath.Dir(r.filename)
	base := strings.TrimSuffix(filepath.Base(r.filename), filepath.Ext(r.filename))
	ext := filepath.Ext(r.filename)
	plain := filepath.Join(dir, fmt.Sprintf("%s.%s%s", base, date, ext))
	if !exists(plain) && !exists(plain+".gz") {
		return plain
	}
	for i := 1; ; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s.%s.%d%s", base, date, i, ext))
		if !exists(p) && !exists(p+".gz") {
			return p
		}
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (r *rotator) kickMill() {
	r.millWg.Add(1)
	go func() {
		defer r.millWg.Done()
		r.mill()
	}()
}

func (r *rotator) mill() {
	r.millMu.Lock()
	defer r.millMu.Unlock()
	if r.compress {
		r.gzipOld()
	}
	r.cleanup()
}

func (r *rotator) backupPattern() *regexp.Regexp {
	base := strings.TrimSuffix(filepath.Base(r.filename), filepath.Ext(r.filename))
	ext := strings.TrimPrefix(filepath.Ext(r.filename), ".")
	// zlm-admin.2026-08-19.log  /  zlm-admin.2026-08-19.1.log  /  optional .gz
	return regexp.MustCompile(`^` + regexp.QuoteMeta(base) + `\.(\d{4}-\d{2}-\d{2})(?:\.(\d+))?\.` + regexp.QuoteMeta(ext) + `(?:\.gz)?$`)
}

func (r *rotator) listBackups() []backupFile {
	dir := filepath.Dir(r.filename)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	re := r.backupPattern()
	out := make([]backupFile, 0)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		m := re.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		idx := 0
		if m[2] != "" {
			idx, _ = strconv.Atoi(m[2])
		}
		out = append(out, backupFile{
			path:  filepath.Join(dir, e.Name()),
			date:  m[1],
			index: idx,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].date != out[j].date {
			return out[i].date < out[j].date
		}
		return out[i].index < out[j].index
	})
	return out
}

func (r *rotator) gzipOld() {
	for _, b := range r.listBackups() {
		if strings.HasSuffix(b.path, ".gz") {
			continue
		}
		if err := gzipFile(b.path); err == nil {
			_ = os.Remove(b.path)
		}
	}
}

func gzipFile(src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(src+".gz", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, copyErr := io.Copy(zw, in)
	closeErr := zw.Close()
	fileErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(src + ".gz")
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(src + ".gz")
		return closeErr
	}
	return fileErr
}

func (r *rotator) cleanup() {
	files := r.listBackups()
	if len(files) == 0 {
		return
	}
	keepFrom := ""
	if r.maxAge > 0 {
		keepFrom = time.Now().AddDate(0, 0, -r.maxAge).Format("2006-01-02")
	}
	drop := map[string]bool{}
	if keepFrom != "" {
		for _, f := range files {
			if f.date < keepFrom {
				drop[f.path] = true
			}
		}
	}
	remain := make([]backupFile, 0, len(files))
	for _, f := range files {
		if !drop[f.path] {
			remain = append(remain, f)
		}
	}
	if r.maxBackups > 0 && len(remain) > r.maxBackups {
		for _, f := range remain[:len(remain)-r.maxBackups] {
			drop[f.path] = true
		}
	}
	for path := range drop {
		_ = os.Remove(path)
	}
}
