package service

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestFMP4DurationFromTfhdTrun(t *testing.T) {
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(mp4, fakeFMP4WithTrunDuration(1000, 40, 25), 0o644); err != nil {
		t.Fatal(err)
	}
	sec, err := mp4DurationSec(mp4)
	if err != nil || sec < 0.99 || sec > 1.01 {
		t.Fatalf("fmp4 duration=%v err=%v", sec, err)
	}
}

func TestFillMissingDurationsFromZLMNames(t *testing.T) {
	files := []MediaFile{
		{Name: "2026-09-02-17-49-08-12.mp4", DurationSec: 0, ModTime: "2026-09-02T17:50:19+08:00"},
		{Name: "2026-09-02-17-50-19-13.mp4", DurationSec: 0, ModTime: "2026-09-02T17:51:33+08:00"},
	}
	fillMissingDurations(files)
	if files[0].DurationSec < 70 || files[0].DurationSec > 72 {
		t.Fatalf("first=%v", files[0].DurationSec)
	}
	if files[1].DurationSec < 73 || files[1].DurationSec > 75 {
		t.Fatalf("last=%v", files[1].DurationSec)
	}
}

func TestMP4FastStartDetectsMoovBeforeMdat(t *testing.T) {
	fast := append(fakeMP4WithDuration(1000, 1000), mp4Box("mdat", []byte("media"))...)
	slow := append(append(mp4Box("ftyp", append([]byte("isom"), make([]byte, 8)...)), mp4Box("mdat", []byte("media"))...), mp4Box("moov", []byte{0})...)
	if !mp4HasFastStart(bytes.NewReader(fast), int64(len(fast))) {
		t.Fatal("ftyp+moov+mdat should be faststart")
	}
	if mp4HasFastStart(bytes.NewReader(slow), int64(len(slow))) {
		t.Fatal("moov after mdat is not faststart")
	}
}

func TestMP4AndM3U8Duration(t *testing.T) {
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(mp4, fakeMP4WithDuration(1000, 12500), 0o644); err != nil {
		t.Fatal(err)
	}
	sec, err := mp4DurationSec(mp4)
	if err != nil || sec < 12.4 || sec > 12.6 {
		t.Fatalf("mp4 duration=%v err=%v", sec, err)
	}
	m3u8 := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(m3u8, []byte("#EXTM3U\n#EXTINF:9.5,\na.ts\n#EXTINF:2.5,\nb.ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := m3u8DurationSec(m3u8)
	if err != nil || got != 12 {
		t.Fatalf("m3u8 duration=%v err=%v", got, err)
	}
}

func fakeFMP4WithTrunDuration(scale, sampleDur, sampleCount uint32) []byte {
	mvhdPayload := make([]byte, 108)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], scale)
	mvhd := mp4Box("mvhd", mvhdPayload)
	moov := mp4Box("moov", mvhd)

	tfhdPayload := make([]byte, 16)
	binary.BigEndian.PutUint32(tfhdPayload[0:4], tfhdDefaultDuration) // flags in low 24 bits, version 0
	binary.BigEndian.PutUint32(tfhdPayload[4:8], 1)                   // track_ID
	binary.BigEndian.PutUint32(tfhdPayload[8:12], sampleDur)
	tfhd := mp4Box("tfhd", tfhdPayload)

	trunPayload := make([]byte, 8)
	binary.BigEndian.PutUint32(trunPayload[4:8], sampleCount)
	trun := mp4Box("trun", trunPayload)

	traf := mp4Box("traf", append(tfhd, trun...))
	moof := mp4Box("moof", traf)
	ftyp := mp4Box("ftyp", append([]byte("isom"), make([]byte, 8)...))
	return append(append(ftyp, moov...), moof...)
}

func fakeMP4WithDuration(scale, duration uint32) []byte {
	mvhdPayload := make([]byte, 108)
	binary.BigEndian.PutUint32(mvhdPayload[12:16], scale)
	binary.BigEndian.PutUint32(mvhdPayload[16:20], duration)
	mvhd := mp4Box("mvhd", mvhdPayload)
	moov := mp4Box("moov", mvhd)
	ftyp := mp4Box("ftyp", append([]byte("isom"), make([]byte, 8)...))
	return append(ftyp, moov...)
}

func mp4Box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}
