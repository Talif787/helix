package storage

import (
	"bytes"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func newTestEngine(t *testing.T, dir string, sync bool) *Engine {
	t.Helper()
	e, err := Open(Options{DataDir: dir, SyncWrites: sync})
	if err != nil {
		t.Fatalf("open engine: %v", err)
	}
	return e
}

func TestEnginePutGetDelete(t *testing.T) {
	e := newTestEngine(t, t.TempDir(), false)
	defer e.Close()

	if err := e.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := e.Get([]byte("k"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("v")) {
		t.Fatalf("got %q", got)
	}

	if err := e.Delete([]byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := e.Get([]byte("k")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEngineValidation(t *testing.T) {
	e, err := Open(Options{DataDir: t.TempDir(), MaxKeyBytes: 4, MaxValueBytes: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	if err := e.Put(nil, []byte("v")); !errors.Is(err, ErrEmptyKey) {
		t.Fatalf("expected ErrEmptyKey, got %v", err)
	}
	if err := e.Put([]byte("toolong"), []byte("v")); !errors.Is(err, ErrKeyTooLarge) {
		t.Fatalf("expected ErrKeyTooLarge, got %v", err)
	}
	if err := e.Put([]byte("ok"), []byte("toolong")); !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("expected ErrValueTooLarge, got %v", err)
	}
}

func TestEngineDurabilityAcrossReopen(t *testing.T) {
	dir := t.TempDir()

	e := newTestEngine(t, dir, false)
	if err := e.Put([]byte("persist"), []byte("me")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := e.Put([]byte("gone"), []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := e.Delete([]byte("gone")); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	e2 := newTestEngine(t, dir, false)
	defer e2.Close()

	got, err := e2.Get([]byte("persist"))
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if !bytes.Equal(got, []byte("me")) {
		t.Fatalf("got %q", got)
	}
	if _, err := e2.Get([]byte("gone")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted key to remain deleted, got %v", err)
	}
}

func TestEngineSeqRecoversMonotonic(t *testing.T) {
	dir := t.TempDir()

	e := newTestEngine(t, dir, false)
	for i := 0; i < 5; i++ {
		if err := e.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if got := e.Stats().NextSeq; got != 6 {
		t.Fatalf("expected next seq 6, got %d", got)
	}
	e.Close()

	e2 := newTestEngine(t, dir, false)
	defer e2.Close()
	if got := e2.Stats().NextSeq; got != 6 {
		t.Fatalf("expected recovered next seq 6, got %d", got)
	}
}

func TestEngineClosedRejectsOps(t *testing.T) {
	e := newTestEngine(t, t.TempDir(), false)
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := e.Put([]byte("k"), []byte("v")); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on put, got %v", err)
	}
	if _, err := e.Get([]byte("k")); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed on get, got %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("second close should be a no-op, got %v", err)
	}
}

func TestEngineDefensiveCopy(t *testing.T) {
	e := newTestEngine(t, t.TempDir(), false)
	defer e.Close()

	key := []byte("k")
	val := []byte("original")
	if err := e.Put(key, val); err != nil {
		t.Fatalf("put: %v", err)
	}

	// Mutating caller buffers after the write must not affect stored state.
	copy(val, []byte("MUTATED!"))
	key[0] = 'z'

	got, err := e.Get([]byte("k"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, []byte("original")) {
		t.Fatalf("defensive copy failed, got %q", got)
	}
}

func TestEngineConcurrentWrites(t *testing.T) {
	e := newTestEngine(t, t.TempDir(), false)
	defer e.Close()

	const n = 1000
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			k := []byte(fmt.Sprintf("k%d", i))
			if err := e.Put(k, []byte("v")); err != nil {
				t.Errorf("put: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := e.Stats().Keys; got != n {
		t.Fatalf("expected %d keys, got %d", n, got)
	}
}
