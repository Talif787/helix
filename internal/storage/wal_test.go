package storage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func rec(seq uint64, kind Kind, key, val string) Record {
	return Record{Seq: seq, Kind: kind, Key: []byte(key), Value: []byte(val)}
}

func TestWALAppendAndReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	w, recs, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected empty log, got %d records", len(recs))
	}

	want := []Record{
		rec(1, KindSet, "a", "1"),
		rec(2, KindSet, "b", "2"),
		rec(3, KindDelete, "a", ""),
	}
	for i := range want {
		if err := w.Append(&want[i]); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, got, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("want %d records, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq || got[i].Kind != want[i].Kind ||
			string(got[i].Key) != string(want[i].Key) || string(got[i].Value) != string(want[i].Value) {
			t.Fatalf("record %d mismatch: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestWALTornTailIsTruncated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	w, _, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r1 := rec(1, KindSet, "k", "v")
	if err := w.Append(&r1); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Simulate a crash mid-append: a header claiming a payload that is not present.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("reopen for corruption: %v", err)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 1000)
	binary.LittleEndian.PutUint32(hdr[4:8], 12345)
	if _, err := f.Write(hdr[:]); err != nil {
		t.Fatalf("write torn header: %v", err)
	}
	if _, err := f.Write([]byte("only a few bytes")); err != nil {
		t.Fatalf("write partial payload: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupt: %v", err)
	}

	w2, recs, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("reopen after corruption: %v", err)
	}
	if len(recs) != 1 || string(recs[0].Key) != "k" {
		t.Fatalf("expected 1 recovered record, got %d: %+v", len(recs), recs)
	}

	// After truncation a fresh append must round-trip cleanly.
	r2 := rec(2, KindSet, "k2", "v2")
	if err := w2.Append(&r2); err != nil {
		t.Fatalf("append after truncate: %v", err)
	}
	if err := w2.Close(); err != nil {
		t.Fatalf("close2: %v", err)
	}

	_, recs2, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("final reopen: %v", err)
	}
	if len(recs2) != 2 {
		t.Fatalf("expected 2 records after clean append, got %d", len(recs2))
	}
}

func TestWALCorruptChecksumDropsTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.log")

	w, _, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r := rec(1, KindSet, "hello", "world")
	if err := w.Append(&r); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Flip the final payload byte so the stored checksum no longer matches.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	data[len(data)-1] ^= 0xFF
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, recs, err := OpenWAL(path, true)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("expected 0 records from a corrupt-only log, got %d", len(recs))
	}
}
