// Command sstdemo is a self-verifying demonstration of the Phase 2 Part 1 SSTable
// layer. It seeds dummy records, writes them to a real on-disk SSTable, reopens it,
// and exercises every read path (present key, absent key, tombstone, full ordered
// scan). It prints PASS and exits 0 when every check holds, or prints FAIL and
// exits 1 otherwise, so it can be used as a health check in scripts and CI.
//
// It is a development and verification tool, not part of the running system. The
// values used here are opaque bytes to Helix; the JSON strings are only for realism.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/talifpathan/helix/internal/storage"
)

type seedRecord struct {
	key   string
	value string
	del   bool
}

func main() {
	dir := flag.String("dir", "./data/demo", "directory to write the demo SSTable into")
	items := flag.Int("items", 5000, "number of generated item:* records (to span multiple blocks)")
	flag.Parse()

	if err := run(*dir, *items); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("PASS: all SSTable demo checks succeeded")
}

func run(dir string, items int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "000001.sst")

	seed := []seedRecord{
		{key: "user:1001", value: `{"name":"Ada Lovelace","email":"ada@example.com"}`},
		{key: "user:1002", value: `{"name":"Alan Turing","email":"alan@example.com"}`},
		{key: "user:1003", value: `{"name":"Grace Hopper","email":"grace@example.com"}`},
		{key: "user:2001", del: true}, // a deleted key, stored as a tombstone
	}
	for i := 0; i < items; i++ {
		seed = append(seed, seedRecord{
			key:   fmt.Sprintf("item:%08d", i),
			value: fmt.Sprintf("payload-%d", i),
		})
	}

	// SSTables require strictly ascending keys, so sort before writing.
	sort.Slice(seed, func(i, j int) bool { return seed[i].key < seed[j].key })

	w, err := storage.NewSSTableWriter(path, len(seed))
	if err != nil {
		return err
	}
	var seq uint64
	for _, e := range seed {
		seq++
		rec := storage.Record{Seq: seq, Kind: storage.KindSet, Key: []byte(e.key), Value: []byte(e.value)}
		if e.del {
			rec.Kind = storage.KindDelete
			rec.Value = nil
		}
		if err := w.Add(rec); err != nil {
			w.Abort()
			return fmt.Errorf("add %q: %w", e.key, err)
		}
	}
	if err := w.Finish(); err != nil {
		return err
	}

	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	fmt.Printf("wrote %d records to %s (%d bytes)\n", len(seed), path, fi.Size())

	st, err := storage.OpenSSTable(path)
	if err != nil {
		return err
	}
	defer st.Close()

	// Scenario 1: present keys return their exact values.
	present := []struct{ key, want string }{
		{"user:1001", `{"name":"Ada Lovelace","email":"ada@example.com"}`},
		{"user:1003", `{"name":"Grace Hopper","email":"grace@example.com"}`},
		{"item:00000000", "payload-0"},
	}
	for _, c := range present {
		got, ok, err := st.Get([]byte(c.key))
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("expected %q to be present", c.key)
		}
		if string(got.Value) != c.want {
			return fmt.Errorf("value mismatch for %q: got %q want %q", c.key, got.Value, c.want)
		}
		fmt.Printf("GET %-14s -> %s\n", c.key, got.Value)
	}

	// Scenario 2: an absent key is reported absent, ideally without a disk read.
	if _, ok, err := st.Get([]byte("user:9999")); err != nil {
		return err
	} else if ok {
		return errors.New("expected user:9999 to be absent")
	}
	fmt.Printf("GET %-14s -> (absent)\n", "user:9999")

	// Scenario 3: a deleted key returns a tombstone.
	rec, ok, err := st.Get([]byte("user:2001"))
	if err != nil {
		return err
	}
	if !ok || rec.Kind != storage.KindDelete {
		return fmt.Errorf("expected user:2001 to be a tombstone, ok=%v kind=%d", ok, rec.Kind)
	}
	fmt.Printf("GET %-14s -> (tombstone)\n", "user:2001")

	// Scenario 4: a full scan returns every record in strictly ascending order.
	it := st.Iterator()
	var (
		count       int
		first, last string
		prev        []byte
	)
	for {
		r, ok := it.Next()
		if !ok {
			break
		}
		if prev != nil && string(r.Key) <= string(prev) {
			return fmt.Errorf("iterator out of order at %q", r.Key)
		}
		prev = append(prev[:0], r.Key...)
		if count == 0 {
			first = string(r.Key)
		}
		last = string(r.Key)
		count++
	}
	if err := it.Err(); err != nil {
		return err
	}
	if count != len(seed) {
		return fmt.Errorf("iterator returned %d records, expected %d", count, len(seed))
	}
	fmt.Printf("SCAN %d records in order, first=%s last=%s\n", count, first, last)

	return nil
}
