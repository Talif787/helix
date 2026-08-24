package storage

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func openEngine(t *testing.T, dir string, memMax int) *Engine {
	t.Helper()
	e, err := Open(Options{DataDir: dir, MemtableMaxBytes: memMax})
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	return e
}

func TestEngineFlushPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 256) // small threshold to force several flushes

	for i := 0; i < 200; i++ {
		if err := e.Put([]byte(fmt.Sprintf("key%04d", i)), []byte(fmt.Sprintf("val%04d", i))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if e.Stats().SSTables == 0 {
		t.Fatal("expected at least one sstable after flush")
	}
	for i := 0; i < 200; i++ {
		v, err := e.Get([]byte(fmt.Sprintf("key%04d", i)))
		if err != nil {
			t.Fatalf("get key%04d: %v", i, err)
		}
		if string(v) != fmt.Sprintf("val%04d", i) {
			t.Fatalf("key%04d = %q", i, v)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: data must be recovered from the manifest and SSTables.
	e2 := openEngine(t, dir, 256)
	defer e2.Close()
	if e2.Stats().SSTables == 0 {
		t.Fatal("expected sstables recovered from manifest")
	}
	for i := 0; i < 200; i++ {
		v, err := e2.Get([]byte(fmt.Sprintf("key%04d", i)))
		if err != nil {
			t.Fatalf("reopen get key%04d: %v", i, err)
		}
		if string(v) != fmt.Sprintf("val%04d", i) {
			t.Fatalf("reopen key%04d = %q", i, v)
		}
	}
}

func TestEngineMergedReadPrecedence(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 1<<20) // large threshold; flushes only when we ask
	defer e.Close()

	// v1 lands in an SSTable.
	if err := e.Put([]byte("k"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}

	// v2 is newer and lives in the active memtable; it must win over the SSTable.
	if err := e.Put([]byte("k"), []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if v, err := e.Get([]byte("k")); err != nil || string(v) != "v2" {
		t.Fatalf("expected v2, got %q err=%v", v, err)
	}

	// A tombstone in the memtable shadows the SSTable value.
	if err := e.Delete([]byte("k")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get([]byte("k")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// After flushing the tombstone, the newer SSTable must still shadow the older one.
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get([]byte("k")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound from newer tombstone sstable, got %v", err)
	}
}

func TestEngineRecoversSSTablesAndActiveWAL(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 1<<20) // large threshold; control flushing explicitly

	for i := 0; i < 50; i++ {
		if err := e.Put([]byte(fmt.Sprintf("flushed%02d", i)), []byte("f")); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Flush(); err != nil { // these 50 become an SSTable
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ { // these stay in the active memtable and its WAL
		if err := e.Put([]byte(fmt.Sprintf("active%02d", i)), []byte("a")); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.Stats().Keys; got != 20 {
		t.Fatalf("expected 20 keys in active memtable, got %d", got)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openEngine(t, dir, 1<<20)
	defer e2.Close()
	if e2.Stats().SSTables == 0 {
		t.Fatal("expected recovered sstables")
	}
	for i := 0; i < 50; i++ {
		if _, err := e2.Get([]byte(fmt.Sprintf("flushed%02d", i))); err != nil {
			t.Fatalf("lost flushed%02d after reopen: %v", i, err)
		}
	}
	for i := 0; i < 20; i++ {
		if _, err := e2.Get([]byte(fmt.Sprintf("active%02d", i))); err != nil {
			t.Fatalf("lost active%02d (WAL replay) after reopen: %v", i, err)
		}
	}
}

func TestEngineConcurrentWritesWithFlush(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 512) // small threshold to exercise rotation under load
	defer e.Close()

	const n = 2000
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := e.Put([]byte(fmt.Sprintf("k%05d", i)), []byte("v")); err != nil {
				t.Errorf("put: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if err := e.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	missing := 0
	for i := 0; i < n; i++ {
		if _, err := e.Get([]byte(fmt.Sprintf("k%05d", i))); err != nil {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("%d of %d keys missing after concurrent writes and flush", missing, n)
	}
}

func TestEngineSeqMonotonicAcrossFlush(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 1<<20)

	if err := e.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := e.Put([]byte("b"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil { // both records now live only in an SSTable
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopen with an empty active WAL. The sequence counter must not reset to zero;
	// it is restored from the manifest.
	e2 := openEngine(t, dir, 1<<20)
	defer e2.Close()
	if got := e2.Stats().NextSeq; got != 3 {
		t.Fatalf("expected next seq 3 after flush and reopen, got %d", got)
	}
	if err := e2.Put([]byte("c"), []byte("3")); err != nil {
		t.Fatal(err)
	}
	if got := e2.Stats().NextSeq; got != 4 {
		t.Fatalf("expected next seq 4 after one more write, got %d", got)
	}
}

func TestEngineDeleteAcrossFlushStaysDeleted(t *testing.T) {
	dir := t.TempDir()
	e := openEngine(t, dir, 1<<20)

	if err := e.Put([]byte("gone"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := e.Delete([]byte("gone")); err != nil {
		t.Fatal(err)
	}
	if err := e.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Get([]byte("gone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted key to stay deleted through flush, got %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}

	e2 := openEngine(t, dir, 1<<20)
	defer e2.Close()
	if _, err := e2.Get([]byte("gone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted key to stay deleted after reopen, got %v", err)
	}
}
