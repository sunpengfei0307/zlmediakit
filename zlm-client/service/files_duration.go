package service

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	tfhdDefaultDuration = 0x000008
	trunDataOffset      = 0x000001
	trunFirstFlags      = 0x000004
	trunSampleDuration  = 0x000100
	trunSampleSize      = 0x000200
	trunSampleFlags     = 0x000400
	trunSampleCTO       = 0x000800
)

func mp4HasFastStart(r io.ReadSeeker, size int64) bool {
	moovAt, mdatAt, err := mp4TopBoxOffsets(r, size)
	if err != nil || moovAt < 0 || mdatAt < 0 {
		return false
	}
	return moovAt < mdatAt
}

func mp4TopBoxOffsets(r io.ReadSeeker, size int64) (moovAt, mdatAt int64, err error) {
	moovAt, mdatAt = -1, -1
	if r == nil || size < 8 {
		return moovAt, mdatAt, fmt.Errorf("short mp4")
	}
	if _, err = r.Seek(0, io.SeekStart); err != nil {
		return moovAt, mdatAt, err
	}
	var pos int64
	for pos+8 <= size {
		var hdr [8]byte
		if _, err = io.ReadFull(r, hdr[:]); err != nil {
			return moovAt, mdatAt, err
		}
		boxSize := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		if boxSize == 1 {
			var big [8]byte
			if _, err = io.ReadFull(r, big[:]); err != nil {
				return moovAt, mdatAt, err
			}
			boxSize = binary.BigEndian.Uint64(big[:])
		}
		if boxSize < 8 {
			return moovAt, mdatAt, fmt.Errorf("bad mp4 box")
		}
		switch typ {
		case "moov":
			if moovAt < 0 {
				moovAt = pos
			}
		case "mdat":
			if mdatAt < 0 {
				mdatAt = pos
			}
		}
		if moovAt >= 0 && mdatAt >= 0 {
			return moovAt, mdatAt, nil
		}
		next := pos + int64(boxSize)
		if boxSize == 0 {
			break
		}
		if next <= pos || next > size {
			return moovAt, mdatAt, fmt.Errorf("bad mp4 box")
		}
		if _, err = r.Seek(next, io.SeekStart); err != nil {
			return moovAt, mdatAt, err
		}
		pos = next
	}
	return moovAt, mdatAt, nil
}

func fileDurationSec(abs, ext string) float64 {
	switch strings.ToLower(ext) {
	case ".mp4":
		sec, _ := mp4DurationSec(abs)
		return sec
	case ".m3u8":
		sec, _ := m3u8DurationSec(abs)
		return sec
	default:
		return 0
	}
}

func mp4DurationSec(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	st := &mp4DurState{tracks: map[uint32]uint64{}}
	if err = readMp4Duration(f, info.Size(), st); err != nil && st.scale == 0 && st.best() == 0 {
		return 0, err
	}
	if st.scale == 0 || st.best() == 0 {
		return 0, fmt.Errorf("mp4 duration missing")
	}
	return float64(st.best()) / float64(st.scale), nil
}

type mp4DurState struct {
	scale    uint64
	movie    uint64
	defDur   uint32
	curTrack uint32
	tracks   map[uint32]uint64
}

func (s *mp4DurState) noteScale(v uint64) {
	if v > 0 && s.scale == 0 {
		s.scale = v
	}
}

func (s *mp4DurState) noteDur(v uint64) {
	if v > 0 && v != 0xffffffff && v != ^uint64(0) && v > s.movie {
		s.movie = v
	}
}

func (s *mp4DurState) best() uint64 {
	best := s.movie
	for _, d := range s.tracks {
		if d > best {
			best = d
		}
	}
	return best
}

func readMp4Duration(r io.ReadSeeker, remaining int64, st *mp4DurState) error {
	for remaining >= 8 {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return err
		}
		remaining -= 8
		size := uint64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := string(hdr[4:8])
		hdrExtra := int64(0)
		if size == 1 {
			var big [8]byte
			if _, err := io.ReadFull(r, big[:]); err != nil {
				return err
			}
			remaining -= 8
			size = binary.BigEndian.Uint64(big[:])
			hdrExtra = 8
		}
		var payload int64
		if size == 0 {
			payload = remaining
		} else {
			payload = int64(size) - 8 - hdrExtra
		}
		if payload < 0 || payload > remaining {
			return fmt.Errorf("bad mp4 box")
		}
		switch typ {
		case "moov", "trak", "mdia", "mvex", "moof", "traf":
			if err := readMp4Duration(r, payload, st); err != nil && st.best() == 0 && st.scale == 0 {
				return err
			}
			remaining -= payload
			continue
		case "mvhd", "mdhd":
			buf := make([]byte, payload)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			if s, d, e := parseMvhd(buf); e == nil {
				st.noteScale(s)
				st.noteDur(d)
			}
			remaining -= payload
			continue
		case "tkhd":
			buf := make([]byte, payload)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			st.noteDur(parseTkhdDuration(buf))
			remaining -= payload
			continue
		case "mehd":
			buf := make([]byte, payload)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			st.noteDur(parseMehd(buf))
			remaining -= payload
			continue
		case "tfhd":
			buf := make([]byte, payload)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			id, def := parseTfhd(buf)
			if id != 0 {
				st.curTrack = id
			}
			st.defDur = def
			remaining -= payload
			continue
		case "trun":
			buf := make([]byte, payload)
			if _, err := io.ReadFull(r, buf); err != nil {
				return err
			}
			if st.curTrack != 0 {
				st.tracks[st.curTrack] += parseTrunDuration(buf, st.defDur)
			}
			remaining -= payload
			continue
		}
		if _, err := r.Seek(payload, io.SeekCurrent); err != nil {
			return err
		}
		remaining -= payload
	}
	return nil
}

func parseMvhd(buf []byte) (timescale, duration uint64, err error) {
	if len(buf) < 20 {
		return 0, 0, fmt.Errorf("short mvhd")
	}
	if buf[0] == 0 {
		return uint64(binary.BigEndian.Uint32(buf[12:16])), uint64(binary.BigEndian.Uint32(buf[16:20])), nil
	}
	if len(buf) < 32 {
		return 0, 0, fmt.Errorf("short mvhd v1")
	}
	return uint64(binary.BigEndian.Uint32(buf[20:24])), binary.BigEndian.Uint64(buf[24:32]), nil
}

func parseTkhdDuration(buf []byte) uint64 {
	if len(buf) < 24 {
		return 0
	}
	if buf[0] == 0 {
		return uint64(binary.BigEndian.Uint32(buf[20:24]))
	}
	if len(buf) < 36 {
		return 0
	}
	return binary.BigEndian.Uint64(buf[28:36])
}

func parseMehd(buf []byte) uint64 {
	if len(buf) < 8 {
		return 0
	}
	if buf[0] == 1 {
		if len(buf) < 12 {
			return 0
		}
		return binary.BigEndian.Uint64(buf[4:12])
	}
	return uint64(binary.BigEndian.Uint32(buf[4:8]))
}

func parseTfhd(buf []byte) (trackID, defaultDuration uint32) {
	if len(buf) < 8 {
		return 0, 0
	}
	flags := binary.BigEndian.Uint32(buf[0:4]) & 0xffffff
	off := 4
	trackID = binary.BigEndian.Uint32(buf[off : off+4])
	off += 4
	if flags&0x000001 != 0 {
		off += 8
	}
	if flags&0x000002 != 0 {
		off += 4
	}
	if flags&tfhdDefaultDuration != 0 && off+4 <= len(buf) {
		defaultDuration = binary.BigEndian.Uint32(buf[off : off+4])
	}
	return trackID, defaultDuration
}

func parseTrunDuration(buf []byte, defaultDuration uint32) uint64 {
	if len(buf) < 8 {
		return 0
	}
	flags := binary.BigEndian.Uint32(buf[0:4]) & 0xffffff
	count := binary.BigEndian.Uint32(buf[4:8])
	off := 8
	if flags&trunDataOffset != 0 {
		off += 4
	}
	if flags&trunFirstFlags != 0 {
		off += 4
	}
	if flags&trunSampleDuration == 0 {
		return uint64(count) * uint64(defaultDuration)
	}
	step := 4
	if flags&trunSampleSize != 0 {
		step += 4
	}
	if flags&trunSampleFlags != 0 {
		step += 4
	}
	if flags&trunSampleCTO != 0 {
		step += 4
	}
	var sum uint64
	for i := uint32(0); i < count; i++ {
		if off+4 > len(buf) {
			break
		}
		sum += uint64(binary.BigEndian.Uint32(buf[off : off+4]))
		off += step
	}
	return sum
}

func m3u8DurationSec(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var sum float64
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#EXTINF:") {
			continue
		}
		line = strings.TrimPrefix(line, "#EXTINF:")
		if i := strings.IndexByte(line, ','); i >= 0 {
			line = line[:i]
		}
		v, perr := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if perr == nil && v > 0 {
			sum += v
		}
	}
	return sum, sc.Err()
}

var zlmRecordName = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}-\d{2}-\d{2}-\d{2})-\d+\.mp4$`)

func parseZLMRecordStart(name string) (time.Time, bool) {
	m := zlmRecordName.FindStringSubmatch(strings.ToLower(filepath.Base(name)))
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02-15-04-05", m[1], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func fillMissingDurations(files []MediaFile) {
	type item struct {
		i int
		t time.Time
	}
	starts := make([]item, 0, len(files))
	for i, f := range files {
		if t, ok := parseZLMRecordStart(f.Name); ok {
			starts = append(starts, item{i, t})
		}
	}
	sort.Slice(starts, func(a, b int) bool { return starts[a].t.Before(starts[b].t) })
	for n, cur := range starts {
		if files[cur.i].DurationSec > 0 {
			continue
		}
		if n+1 < len(starts) {
			sec := starts[n+1].t.Sub(cur.t).Seconds()
			if sec > 0 && sec < 24*3600 {
				files[cur.i].DurationSec = sec
			}
			continue
		}
		if mt, err := time.Parse(time.RFC3339, files[cur.i].ModTime); err == nil {
			sec := mt.Sub(cur.t).Seconds()
			if sec > 1 && sec < 24*3600 {
				files[cur.i].DurationSec = sec
			}
		}
	}
}
