package storage

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

func writeSSTable(t *testing.T, path string, records []Record) {
	t.Helper()
	w, err := NewSSTableWriter(path, len(records))
	if err != nil {
		t.Fatalf("new writer: %v", err)
	}
	for i := range records {
		if err := w.Add(records[i]); err != nil {
			t.Fatalf("add %q: %v", records[i].Key, err)
		}
	}
	if err := w.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestSSTableWriteReadGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "000001.sst")
	records := []Record{
		{Seq: 1, Kind: KindSet, Key: []byte("apple"), Value: []byte("red")},
		{Seq: 2, Kind: KindSet, Key: []byte("banana"), Value: []byte("yellow")},
		{Seq: 4, Kind: KindDelete, Key: []byte("cherry")},
		{Seq: 3, Kind: KindSet, Key: []byte("date"), Value: []byte("brown")},
	}
	writeSSTable(t, path, records)

	st, err := OpenSSTable(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	got, ok, err := st.Get([]byte("banana"))
	if err != nil || !ok {
		t.Fatalf("banana: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.Value, []byte("yellow")) {
		t.Fatalf("banana value: %q", got.Value)
	}

	tomb, ok, err := st.Get([]byte("cherry"))
	if err != nil || !ok {
		t.Fatalf("cherry: ok=%v err=%v", ok, err)
	}
	if tomb.Kind != KindDelete {
		t.Fatalf("cherry should be a tombstone, got kind %d", tomb.Kind)
	}

	if _, ok, _ := st.Get([]byte("fig")); ok {
		t.Fatal("fig should be absent")
	}
	if _, ok, _ := st.Get([]byte("aardvark")); ok {
		t.Fatal("aardvark (before first key) should be absent")
	}
}

func TestSSTableRejectsNonAscending(t *testing.T) {
	w, err := NewSSTableWriter(filepath.Join(t.TempDir(), "x.sst"), 4)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer w.Abort()
	if err := w.Add(Record{Seq: 1, Kind: KindSet, Key: []byte("b")}); err != nil {
		t.Fatalf("add b: %v", err)
	}
	if err := w.Add(Record{Seq: 2, Kind: KindSet, Key: []byte("a")}); err == nil {
		t.Fatal("expected error adding a descending key")
	}
}

func TestSSTableIterator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iter.sst")
	var records []Record
	for i := 0; i < 100; i++ {
		records = append(records, Record{
			Seq:   uint64(i + 1),
			Kind:  KindSet,
			Key:   []byte(fmt.Sprintf("key%03d", i)),
			Value: []byte(fmt.Sprintf("val%03d", i)),
		})
	}
	writeSSTable(t, path, records)

	st, err := OpenSSTable(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	it := st.Iterator()
	count := 0
	var prev []byte
	for {
		r, ok := it.Next()
		if !ok {
			break
		}
		if prev != nil && bytes.Compare(r.Key, prev) <= 0 {
			t.Fatalf("iterator out of order at %q after %q", r.Key, prev)
		}
		prev = r.Key
		count++
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if count != 100 {
		t.Fatalf("expected 100 records, iterated %d", count)
	}
}

func TestSSTableAcrossManyBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.sst")
	const n = 5000
	var records []Record
	for i := 0; i < n; i++ {
		records = append(records, Record{
			Seq:   uint64(i + 1),
			Kind:  KindSet,
			Key:   []byte(fmt.Sprintf("key%08d", i)),
			Value: bytes.Repeat([]byte("v"), 40),
		})
	}
	writeSSTable(t, path, records)

	st, err := OpenSSTable(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if st.blockCount() < 2 {
		t.Fatalf("expected multiple blocks, got %d", st.blockCount())
	}

	// Probe keys in the first, middle, and last blocks.
	for _, i := range []int{0, 1, n / 2, n - 2, n - 1} {
		key := []byte(fmt.Sprintf("key%08d", i))
		got, ok, err := st.Get(key)
		if err != nil || !ok {
			t.Fatalf("get %s: ok=%v err=%v", key, ok, err)
		}
		if len(got.Value) != 40 {
			t.Fatalf("get %s: unexpected value length %d", key, len(got.Value))
		}
	}
}

func TestSSTableEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sst")
	writeSSTable(t, path, nil)

	st, err := OpenSSTable(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	if _, ok, _ := st.Get([]byte("anything")); ok {
		t.Fatal("empty table should contain nothing")
	}
	if _, ok := st.Iterator().Next(); ok {
		t.Fatal("empty table iterator should yield nothing")
	}
}

func TestSSTableBinaryValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bin.sst")
	val := []byte{0x00, 0xff, 0x10, 0x00, 0x42}
	writeSSTable(t, path, []Record{{Seq: 1, Kind: KindSet, Key: []byte("k"), Value: val}})

	st, err := OpenSSTable(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()

	got, ok, err := st.Get([]byte("k"))
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.Value, val) {
		t.Fatalf("binary value round-trip failed: %v", got.Value)
	}
}
