package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"zlm-admin/model"

	"github.com/nxadm/tail"
)

const (
	logSnapMaxBytes = 96 << 10
	logSnapMaxLines = 1200
)

func listLogFiles(dir string) ([]model.LogFileInfo, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]model.LogFileInfo, 0)
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".log") && !strings.HasSuffix(name, ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, model.LogFileInfo{
			Name:    name,
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime > out[j].ModTime })
	return out, nil
}

func safeLogPath(dir, name string) (string, error) {
	if name == "" {
		files, err := listLogFiles(dir)
		if err != nil {
			return "", err
		}
		if len(files) == 0 {
			return "", os.ErrNotExist
		}
		name = files[0].Name
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return "", os.ErrPermission
	}
	return filepath.Join(dir, name), nil
}

func lineLevel(line string) string {
	if i := strings.Index(line, " ["); i > 0 {
		for j := i - 1; j >= 0; j-- {
			c := line[j]
			if c == ' ' || c == '\t' {
				if j+1 < i {
					sw := line[j+1 : i]
					if len(sw) == 1 {
						switch sw[0] {
						case 'D', 'I', 'W', 'E':
							return sw
						}
					}
				}
				break
			}
		}
	}
	if strings.Contains(line, " E ") {
		return "E"
	}
	if strings.Contains(line, " W ") {
		return "W"
	}
	if strings.Contains(line, " D ") {
		return "D"
	}
	return "I"
}

func allowLevel(line, lv string) bool {
	if lv == "" {
		return true
	}
	return strings.Contains(lv, lineLevel(line))
}

func readTailLines(path string, maxBytes int64, maxLines int, lv string) (lines []string, size int64, err error) {
	if maxBytes <= 0 {
		maxBytes = logSnapMaxBytes
	}
	if maxLines <= 0 {
		maxLines = logSnapMaxLines
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	size = st.Size()
	start := int64(0)
	if size > maxBytes {
		start = size - maxBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, size, err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, size, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, maxBytes+4096))
	if err != nil {
		return nil, size, err
	}
	if start > 0 {
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	raw := strings.Split(string(buf), "\n")
	out := make([]string, 0, maxLines)
	for _, line := range raw {
		if line == "" {
			continue
		}
		if !allowLevel(line, lv) {
			continue
		}
		out = append(out, line)
	}
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return out, size, nil
}

func followLog(ctx context.Context, path string, offset int64, lv string, emit func(line string) error) error {
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if offset < 0 || offset > st.Size() {
		offset = st.Size()
	}
	t, err := tail.TailFile(path, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: true,
		Poll:      true,
		Location:  &tail.SeekInfo{Offset: offset, Whence: io.SeekStart},
		Logger:    tail.DiscardingLogger,
	})
	if err != nil {
		return err
	}
	defer t.Cleanup()
	go func() {
		<-ctx.Done()
		t.Kill(nil)
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-t.Lines:
			if !ok {
				return nil
			}
			if line == nil {
				continue
			}
			if line.Err != nil {
				return line.Err
			}
			if line.Text == "" || !allowLevel(line.Text, lv) {
				continue
			}
			if err := emit(line.Text); err != nil {
				return err
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, v any) error {
	var data []byte
	switch x := v.(type) {
	case string:
		data = []byte(x)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		data = b
	}
	if event != "" && event != "message" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	return nil
}

func writeSSEPing(w http.ResponseWriter) error {
	_, err := io.WriteString(w, ": ping\n\n")
	return err
}
