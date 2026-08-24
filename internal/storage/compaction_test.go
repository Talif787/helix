package storage

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// waitFor polls cond until it is true or the timeout elapses, failing the test on
// timeout. Compaction runs on a background goroutine, so tests wait for its effect
// rather than assuming it has already happened.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func TestGetSizeTieredBucket(t *testing.T) {
	tables := []tableRef{
		{num: 1, size: 100},
		{num: 2, size: 110},
		{num: 3, size: 105},
		{num: 4, size: 10000}, // a much larger table in its own tier
	}
	bucket := getSizeTieredBucket(tables, 3)
	if len(bucket) != 3 {
		t.Fatalf("expected the three similar-size tables to bucket together, got %d", len(bucket))
	}
	for _, tr := range bucket {
		if tr.size > 1000 {
			t.Fatalf("large table should not join the small tier, saw size %d", tr.size)
		}
	}

	if getSizeTieredBucket(tables, 5) != nil {
		t.Fatal("no bucket should qualify when the threshold exceeds available tables")
	}
	if getSizeTieredBucket(tables, 1) != nil {
		t.Fatal("threshold below 2 should never trigger compaction")
	}
}

func TestCompactionReducesTableCount(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(Options{DataDir: dir, MemtableMaxBytes: 1 << 20, CompactionMinThreshold: 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	const rounds = 5
	const perRound = 20
	for r := 0; r < rounds; r++ {
		for i := 0; i < perRound; i++ {
			if err := e.Put([]byte(fmt.Sprintf("r%02d-k%03d", r, i)), []byte("value")); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
		if err := e.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	// Each flush makes one SSTable; with a threshold of 3, at least one compaction must
	// have merged some of them, leaving fewer tables than the number of flushes.
	waitFor(t, 5*time.Second, func() bool { return e.Stats().SSTables < rounds })

	for r := 0; r < rounds; r++ {
		for i := 0; i < perRound; i++ {
			key := fmt.Sprintf("r%02d-k%03d", r, i)
			v, err := e.Get([]byte(key))
			if err != nil {
				t.Fatalf("get %s after compaction: %v", key, err)
			}
			if string(v) != "value" {
				t.Fatalf("get %s: want value, got %q", key, v)
			}
		}
	}
}

func TestCompactionDropsTombstones(t *testing.T) {
	dir := t.TempDir()
	e, err := Open(Options{DataDir: dir, MemtableMaxBytes: 1 << 20, CompactionMinThreshold: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer e.Close()

	// First table: eight live keys.
	for i := 0; i < 8; i++ {
		if err := e.Put([]byte(fmt.Sprintf("k%d", i)), []byte("original")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush 1: %v", err)
	}

	// Second table of similar size: delete two keys, add six new ones. Similar size
	// means both tables land in one bucket, so the compaction is full and may drop
	// tombstones safely.
	_ = e.Delete([]byte("k2"))
	_ = e.Delete([]byte("k4"))
	for i := 100; i < 106; i++ {
		if err := e.Put([]byte(fmt.Sprintf("k%d", i)), []byte("later")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if err := e.Flush(); err != nil {
		t.Fatalf("flush 2: %v", err)
	}

	// A full compaction of the two tables should reduce them to a single table.
	waitFor(t, 5*time.Second, func() bool { return e.Stats().SSTables == 1 })

	// Deleted keys read as absent, live keys survive.
	if _, err := e.Get([]byte("k2")); err != ErrNotFound {
		t.Fatalf("k2 should be deleted, got err=%v", err)
	}
	if _, err := e.Get([]byte("k4")); err != ErrNotFound {
		t.Fatalf("k4 should be deleted, got err=%v", err)
	}
	if v, err := e.Get([]byte("k0")); err != nil || string(v) != "original" {
		t.Fatalf("k0 should survive: v=%q err=%v", v, err)
	}
	if v, err := e.Get([]byte("k100")); err != nil || string(v) != "later" {
		t.Fatalf("k100 should survive: v=%q err=%v", v, err)
	}

	// Inspect the surviving SSTable directly: the dropped tombstones must be physically
	// gone, not merely shadowed. Open it read-only, bypassing the engine.
	files, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected exactly one sstable after full compaction, got %d: %v", len(files), files)
	}
	st, err := OpenSSTable(files[0])
	if err != nil {
		t.Fatalf("open sstable: %v", err)
	}
	defer st.Close()

	if _, ok, _ := st.Get([]byte("k2")); ok {
		t.Fatal("k2 tombstone should have been dropped during full compaction")
	}
	if _, ok, _ := st.Get([]byte("k4")); ok {
		t.Fatal("k4 tombstone should have been dropped during full compaction")
	}
	if rec, ok, _ := st.Get([]byte("k0")); !ok || rec.Kind != KindSet || string(rec.Value) != "original" {
		t.Fatalf("k0 should be a live record in the compacted table: ok=%v rec=%+v", ok, rec)
	}
}
