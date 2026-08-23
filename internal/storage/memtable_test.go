package storage

import (
	"fmt"
	"sync"
	"testing"
)

func TestMemtablePutGet(t *testing.T) {
	m := NewMemtable()
	m.Put(Record{Seq: 1, Kind: KindSet, Key: []byte("k"), Value: []byte("v")})

	got, ok := m.Get([]byte("k"))
	if !ok {
		t.Fatal("expected key present")
	}
	if string(got.Value) != "v" {
		t.Fatalf("got %q", got.Value)
	}
	if _, ok := m.Get([]byte("missing")); ok {
		t.Fatal("unexpected key found")
	}
}

func TestMemtableOverwrite(t *testing.T) {
	m := NewMemtable()
	m.Put(Record{Seq: 1, Kind: KindSet, Key: []byte("k"), Value: []byte("old")})
	m.Put(Record{Seq: 2, Kind: KindSet, Key: []byte("k"), Value: []byte("newer")})

	got, _ := m.Get([]byte("k"))
	if string(got.Value) != "newer" {
		t.Fatalf("expected overwrite, got %q", got.Value)
	}
	if got.Seq != 2 {
		t.Fatalf("expected seq 2, got %d", got.Seq)
	}
	if m.Len() != 1 {
		t.Fatalf("expected 1 distinct key, got %d", m.Len())
	}
}

func TestMemtableTombstone(t *testing.T) {
	m := NewMemtable()
	m.Put(Record{Seq: 1, Kind: KindSet, Key: []byte("k"), Value: []byte("v")})
	m.Put(Record{Seq: 2, Kind: KindDelete, Key: []byte("k")})

	got, ok := m.Get([]byte("k"))
	if !ok {
		t.Fatal("tombstone should still be present as a record")
	}
	if got.Kind != KindDelete {
		t.Fatalf("expected tombstone, got kind %d", got.Kind)
	}
}

func TestMemtableScanOrdered(t *testing.T) {
	m := NewMemtable()
	for _, k := range []string{"c", "a", "d", "b"} {
		m.Put(Record{Seq: 1, Kind: KindSet, Key: []byte(k), Value: []byte(k)})
	}
	var got []string
	m.Scan(func(r Record) bool {
		got = append(got, string(r.Key))
		return true
	})
	if fmt.Sprint(got) != fmt.Sprint([]string{"a", "b", "c", "d"}) {
		t.Fatalf("scan out of order: %v", got)
	}
}

func TestMemtableScanEarlyStop(t *testing.T) {
	m := NewMemtable()
	for _, k := range []string{"a", "b", "c"} {
		m.Put(Record{Seq: 1, Kind: KindSet, Key: []byte(k)})
	}
	count := 0
	m.Scan(func(r Record) bool {
		count++
		return count < 2
	})
	if count != 2 {
		t.Fatalf("expected early stop at 2, got %d", count)
	}
}

func TestMemtableConcurrent(t *testing.T) {
	m := NewMemtable()
	const writers = 8
	const perWriter = 500

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("w%d-k%d", w, i)
				m.Put(Record{Seq: uint64(i), Kind: KindSet, Key: []byte(key), Value: []byte("v")})
				m.Get([]byte(key))
			}
		}(w)
	}
	wg.Wait()

	if m.Len() != writers*perWriter {
		t.Fatalf("expected %d keys, got %d", writers*perWriter, m.Len())
	}
}
