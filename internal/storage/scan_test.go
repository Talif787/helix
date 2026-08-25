package storage

import (
	"testing"
)

func TestEngineScanMergesAndSkipsTombstones(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(Options{DataDir: dir, MemtableMaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	for _, k := range []string{"a", "b", "c", "d"} {
		if err := e.Put([]byte(k), []byte("v-"+k)); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	if err := e.Delete([]byte("b")); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	if err := e.Flush(); err != nil { // push a, b(tombstone), c, d into an sstable
		t.Fatalf("flush: %v", err)
	}
	// A newer version of c and a new key e live in the active memtable, above the sstable.
	if err := e.Put([]byte("c"), []byte("v-c2")); err != nil {
		t.Fatalf("put c2: %v", err)
	}
	if err := e.Put([]byte("e"), []byte("v-e")); err != nil {
		t.Fatalf("put e: %v", err)
	}

	got := map[string]string{}
	var order []string
	if err := e.Scan(func(key, value []byte) bool {
		got[string(key)] = string(value)
		order = append(order, string(key))
		return true
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}

	want := map[string]string{"a": "v-a", "c": "v-c2", "d": "v-d", "e": "v-e"}
	if len(got) != len(want) {
		t.Fatalf("scan returned %v, want %v (b should be skipped as a tombstone)", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key %s: scan gave %q, want %q", k, got[k], v)
		}
	}
	for i := 1; i < len(order); i++ {
		if order[i-1] >= order[i] {
			t.Fatalf("scan did not return keys in ascending order: %v", order)
		}
	}
}

func TestEngineScanEarlyStop(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(Options{DataDir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()
	for _, k := range []string{"a", "b", "c"} {
		if err := e.Put([]byte(k), []byte("v")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	count := 0
	if err := e.Scan(func(key, value []byte) bool {
		count++
		return false // stop after the first key
	}); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if count != 1 {
		t.Fatalf("scan should have stopped after 1 key, visited %d", count)
	}
}
