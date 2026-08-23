package storage

import (
	"bytes"
	"math/rand"
	"sync"
	"time"
)

const (
	skipMaxLevel      = 16
	skipProbability   = 0.25
	skipEntryOverhead = 48 // rough per-node pointer and header cost, for size accounting
)

// node is a single skip-list tower. next[i] points to the successor at level i.
type node struct {
	key  []byte
	rec  Record
	next []*node
}

// Memtable is an in-memory, key-ordered map holding the latest record per key.
// It is safe for concurrent use: reads take a read lock, writes take a write lock.
// A skip list is used rather than a map so that entries can be flushed to an
// SSTable in sorted order in a later phase, and so range scans are possible.
type Memtable struct {
	mu     sync.RWMutex
	head   *node
	level  int
	length int
	approx int64
	rnd    *rand.Rand
}

// NewMemtable returns an empty memtable.
func NewMemtable() *Memtable {
	return &Memtable{
		head:  &node{next: make([]*node, skipMaxLevel)},
		level: 1,
		rnd:   rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (m *Memtable) randomLevel() int {
	lvl := 1
	for lvl < skipMaxLevel && m.rnd.Float64() < skipProbability {
		lvl++
	}
	return lvl
}

// Put inserts or overwrites the record for rec.Key. The memtable takes ownership
// of rec's Key and Value slices, which the caller must not mutate afterwards.
func (m *Memtable) Put(rec Record) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var update [skipMaxLevel]*node
	x := m.head
	for i := m.level - 1; i >= 0; i-- {
		for x.next[i] != nil && bytes.Compare(x.next[i].key, rec.Key) < 0 {
			x = x.next[i]
		}
		update[i] = x
	}

	if cand := x.next[0]; cand != nil && bytes.Equal(cand.key, rec.Key) {
		m.approx += int64(len(rec.Value)) - int64(len(cand.rec.Value))
		cand.rec = rec
		return
	}

	lvl := m.randomLevel()
	if lvl > m.level {
		for i := m.level; i < lvl; i++ {
			update[i] = m.head
		}
		m.level = lvl
	}

	n := &node{key: rec.Key, rec: rec, next: make([]*node, lvl)}
	for i := 0; i < lvl; i++ {
		n.next[i] = update[i].next[i]
		update[i].next[i] = n
	}
	m.length++
	m.approx += int64(len(rec.Key)) + int64(len(rec.Value)) + skipEntryOverhead
}

// Get returns the latest record for key and whether it was present. A returned
// record may be a tombstone (Kind == KindDelete); callers interpret that as absence.
func (m *Memtable) Get(key []byte) (Record, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	x := m.head
	for i := m.level - 1; i >= 0; i-- {
		for x.next[i] != nil && bytes.Compare(x.next[i].key, key) < 0 {
			x = x.next[i]
		}
	}
	if cand := x.next[0]; cand != nil && bytes.Equal(cand.key, key) {
		return cand.rec, true
	}
	return Record{}, false
}

// Scan invokes fn for every record in ascending key order until fn returns false
// or the memtable is exhausted. The lock is held for the duration of the scan.
func (m *Memtable) Scan(fn func(Record) bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for x := m.head.next[0]; x != nil; x = x.next[0] {
		if !fn(x.rec) {
			return
		}
	}
}

// Len returns the number of distinct keys currently held.
func (m *Memtable) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.length
}

// ApproxSize returns an estimate of the memtable's in-memory footprint in bytes.
// It drives the flush threshold once SSTables are introduced.
func (m *Memtable) ApproxSize() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.approx
}
