package storage

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Default limits used when Options leaves a field unset.
const (
	DefaultMaxKeyBytes            = 64 << 10 // 64 KiB
	DefaultMaxValueBytes          = 1 << 20  // 1 MiB
	DefaultMemtableMaxBytes       = 4 << 20  // 4 MiB
	DefaultBlockCacheBytes        = 8 << 20  // 8 MiB
	DefaultCompactionMinThreshold = 4        // similar-size tables that trigger a compaction
	maxImmutablesBeforeStall      = 4        // writers block when this many memtables await flush
)

// Options configures an Engine. It is a plain value object so the storage package
// stays independent of the application configuration package.
type Options struct {
	DataDir                string
	SyncWrites             bool
	MaxKeyBytes            int
	MaxValueBytes          int
	MemtableMaxBytes       int
	BlockCacheBytes        int
	CompactionMinThreshold int
	Logger                 *slog.Logger
}

// Stats is a point-in-time snapshot of engine state.
type Stats struct {
	Keys        int   // keys in the active memtable
	ApproxBytes int64 // approximate size of the active memtable
	NextSeq     uint64
	SSTables    int
	Immutable   int // memtables sealed and awaiting flush
}

type flushJob struct {
	mem    *Memtable
	walNum uint64
}

type tableRef struct {
	num  uint64
	st   *SSTable
	size int64
}

// Engine is a durable, single-node, ordered key-value store built as a log-structured
// merge tree. Writes append to a per-memtable WAL segment and apply to the active
// memtable; a full memtable is sealed into an immutable queue and flushed to an SSTable
// by a background goroutine while a fresh memtable takes over. That same goroutine
// compacts similarly sized SSTables together to bound read amplification and, in a full
// compaction, drops tombstones to reclaim space. Reads merge the active memtable, the
// immutable memtables, and the SSTables; among SSTables the highest sequence number
// wins, since compaction means file number no longer tracks data recency.
//
// A read-write mutex guards engine state. Reads hold the read lock for their whole
// duration, so a compaction that swaps the table set (under the write lock) can then
// safely close and delete the replaced files: no reader can still be using them.
type Engine struct {
	dir                    string
	sync                   bool
	threshold              int64
	maxKey                 int
	maxVal                 int
	compactionMinThreshold int
	cache                  *BlockCache
	log                    *slog.Logger

	mu        sync.RWMutex
	flushCond *sync.Cond

	mem    *Memtable
	wal    *WAL
	walNum uint64
	imms   []*flushJob
	tables []tableRef

	seq            uint64
	nextFileNum    uint64
	lastFlushedWAL uint64
	flushErr       error
	closed         bool

	flushCh   chan struct{}
	stopCh    chan struct{}
	flusherWG sync.WaitGroup
}

func walName(n uint64) string { return fmt.Sprintf("%06d.wal", n) }
func sstName(n uint64) string { return fmt.Sprintf("%06d.sst", n) }

// Open initializes an engine rooted at opts.DataDir, recovering prior state from the
// manifest and any WAL segments left on disk.
func Open(opts Options) (*Engine, error) {
	if opts.DataDir == "" {
		return nil, ErrNoDataDir
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxKeyBytes <= 0 {
		opts.MaxKeyBytes = DefaultMaxKeyBytes
	}
	if opts.MaxValueBytes <= 0 {
		opts.MaxValueBytes = DefaultMaxValueBytes
	}
	threshold := int64(opts.MemtableMaxBytes)
	if threshold <= 0 {
		threshold = DefaultMemtableMaxBytes
	}
	blockCacheBytes := int64(opts.BlockCacheBytes)
	if blockCacheBytes <= 0 {
		blockCacheBytes = DefaultBlockCacheBytes
	}
	compThreshold := opts.CompactionMinThreshold
	if compThreshold < 2 {
		compThreshold = DefaultCompactionMinThreshold
	}
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return nil, err
	}

	man, _, err := loadManifest(opts.DataDir)
	if err != nil {
		return nil, err
	}

	e := &Engine{
		dir:                    opts.DataDir,
		sync:                   opts.SyncWrites,
		threshold:              threshold,
		maxKey:                 opts.MaxKeyBytes,
		maxVal:                 opts.MaxValueBytes,
		compactionMinThreshold: compThreshold,
		cache:                  NewBlockCache(blockCacheBytes),
		log:                    opts.Logger,
		lastFlushedWAL:         man.LastFlushedWAL,
		flushCh:                make(chan struct{}, 1),
		stopCh:                 make(chan struct{}),
	}
	e.flushCond = sync.NewCond(&e.mu)

	var maxNum uint64

	for _, num := range man.Tables {
		path := filepath.Join(opts.DataDir, sstName(num))
		st, err := OpenSSTableWithCache(path, num, e.cache)
		if err != nil {
			e.closeTables()
			return nil, fmt.Errorf("open sstable %d: %w", num, err)
		}
		var sz int64
		if fi, serr := os.Stat(path); serr == nil {
			sz = fi.Size()
		}
		e.tables = append(e.tables, tableRef{num: num, st: st, size: sz})
		if num > maxNum {
			maxNum = num
		}
	}

	walNums, err := scanWALSegments(opts.DataDir)
	if err != nil {
		e.closeTables()
		return nil, err
	}
	var unflushed []uint64
	for _, n := range walNums {
		if n <= man.LastFlushedWAL {
			_ = os.Remove(filepath.Join(opts.DataDir, walName(n)))
			continue
		}
		unflushed = append(unflushed, n)
	}

	var (
		maxSeq    uint64
		activeSet bool
	)
	for i, n := range unflushed {
		w, records, err := OpenWAL(filepath.Join(opts.DataDir, walName(n)), opts.SyncWrites)
		if err != nil {
			e.closeTables()
			return nil, err
		}
		mem := NewMemtable()
		for j := range records {
			mem.Put(records[j])
			if records[j].Seq > maxSeq {
				maxSeq = records[j].Seq
			}
		}
		if i == len(unflushed)-1 {
			e.mem = mem
			e.wal = w
			e.walNum = n
			activeSet = true
		} else {
			_ = w.Close()
			e.imms = append(e.imms, &flushJob{mem: mem, walNum: n})
		}
		if n > maxNum {
			maxNum = n
		}
	}

	e.nextFileNum = man.NextFileNum
	if maxNum+1 > e.nextFileNum {
		e.nextFileNum = maxNum + 1
	}
	if e.nextFileNum == 0 {
		e.nextFileNum = 1
	}

	if !activeSet {
		num := e.nextFileNum
		w, _, err := OpenWAL(filepath.Join(opts.DataDir, walName(num)), opts.SyncWrites)
		if err != nil {
			e.closeTables()
			return nil, err
		}
		e.nextFileNum++
		e.mem = NewMemtable()
		e.wal = w
		e.walNum = num
	}

	e.seq = maxSeq
	if man.LastSeq > e.seq {
		e.seq = man.LastSeq
	}

	e.flusherWG.Add(1)
	go e.flushLoop()
	if len(e.imms) > 0 || len(e.tables) >= e.compactionMinThreshold {
		e.signalFlush()
	}

	e.log.Info("storage engine opened",
		"data_dir", opts.DataDir,
		"sstables", len(e.tables),
		"recovered_immutables", len(e.imms),
		"active_wal", walName(e.walNum),
		"next_seq", e.seq+1,
		"sync_writes", opts.SyncWrites,
	)
	return e, nil
}

func (e *Engine) closeTables() {
	for _, t := range e.tables {
		_ = t.st.Close()
	}
}

func scanWALSegments(dir string) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var nums []uint64
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".wal") {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(name, ".wal"), 10, 64)
		if err != nil {
			continue
		}
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	return nums, nil
}

func (e *Engine) validate(key, value []byte, kind Kind) error {
	if len(key) == 0 {
		return ErrEmptyKey
	}
	if len(key) > e.maxKey {
		return ErrKeyTooLarge
	}
	if kind == KindSet && len(value) > e.maxVal {
		return ErrValueTooLarge
	}
	return nil
}

func (e *Engine) write(kind Kind, key, value []byte) error {
	if err := e.validate(key, value, kind); err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	for !e.closed && e.flushErr == nil && len(e.imms) >= maxImmutablesBeforeStall {
		e.flushCond.Wait()
	}
	if e.closed {
		return ErrClosed
	}
	if e.flushErr != nil {
		return e.flushErr
	}

	e.seq++
	rec := Record{Seq: e.seq, Kind: kind, Key: cloneBytes(key), Value: cloneBytes(value)}
	if err := e.wal.Append(&rec); err != nil {
		return err
	}
	e.mem.Put(rec)

	if e.mem.ApproxSize() >= e.threshold {
		if err := e.rotateLocked(); err != nil {
			return err
		}
	}
	return nil
}

// rotateLocked seals the active memtable and starts a fresh one. Caller holds e.mu.
func (e *Engine) rotateLocked() error {
	if e.mem.Len() == 0 {
		return nil
	}
	newNum := e.nextFileNum
	newWAL, _, err := OpenWAL(filepath.Join(e.dir, walName(newNum)), e.sync)
	if err != nil {
		return err
	}
	e.nextFileNum++
	e.imms = append(e.imms, &flushJob{mem: e.mem, walNum: e.walNum})
	e.mem = NewMemtable()
	e.wal = newWAL
	e.walNum = newNum
	e.signalFlush()
	return nil
}

func (e *Engine) signalFlush() {
	select {
	case e.flushCh <- struct{}{}:
	default:
	}
}

// Put stores value under key.
func (e *Engine) Put(key, value []byte) error { return e.write(KindSet, key, value) }

// Delete records a tombstone for key.
func (e *Engine) Delete(key []byte) error { return e.write(KindDelete, key, nil) }

// Get returns the value for key, or ErrNotFound if it is absent or deleted. It merges
// the active memtable, the immutable memtables, and the SSTables. The memtable and
// immutable memtables are strictly newer than any SSTable, so a hit there is
// authoritative; among SSTables the record with the highest sequence number wins.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, ErrEmptyKey
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return nil, ErrClosed
	}

	if rec, ok := e.mem.Get(key); ok {
		return interpretRecord(rec)
	}
	for i := len(e.imms) - 1; i >= 0; i-- {
		if rec, ok := e.imms[i].mem.Get(key); ok {
			return interpretRecord(rec)
		}
	}
	var best Record
	found := false
	for i := range e.tables {
		rec, ok, err := e.tables[i].st.Get(key)
		if err != nil {
			return nil, err
		}
		if ok && (!found || rec.Seq > best.Seq) {
			best = rec
			found = true
		}
	}
	if found {
		return interpretRecord(best)
	}
	return nil, ErrNotFound
}

func interpretRecord(rec Record) ([]byte, error) {
	if rec.Kind == KindDelete {
		return nil, ErrNotFound
	}
	return cloneBytes(rec.Value), nil
}

// Flush seals the active memtable and blocks until every pending memtable has been
// written to an SSTable.
func (e *Engine) Flush() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	if e.flushErr != nil {
		return e.flushErr
	}
	if err := e.rotateLocked(); err != nil {
		return err
	}
	for len(e.imms) > 0 && !e.closed && e.flushErr == nil {
		e.flushCond.Wait()
	}
	return e.flushErr
}

func (e *Engine) flushLoop() {
	defer e.flusherWG.Done()
	for {
		select {
		case <-e.stopCh:
			e.drainImmutables()
			return
		case <-e.flushCh:
			e.drainImmutables()
			e.maybeCompact()
		}
	}
}

// drainImmutables flushes every queued memtable to an SSTable, oldest first.
func (e *Engine) drainImmutables() {
	for {
		e.mu.Lock()
		if e.flushErr != nil || len(e.imms) == 0 {
			e.mu.Unlock()
			return
		}
		job := e.imms[0]
		tableNum := e.nextFileNum
		e.nextFileNum++
		dir := e.dir
		cache := e.cache
		e.mu.Unlock()

		path := filepath.Join(dir, sstName(tableNum))
		if err := writeSSTableFromMemtable(path, job.mem); err != nil {
			_ = os.Remove(path)
			e.failFlush(err)
			return
		}
		st, err := OpenSSTableWithCache(path, tableNum, cache)
		if err != nil {
			e.failFlush(err)
			return
		}
		var sz int64
		if fi, serr := os.Stat(path); serr == nil {
			sz = fi.Size()
		}

		e.mu.Lock()
		e.tables = append(e.tables, tableRef{num: tableNum, st: st, size: sz})
		e.lastFlushedWAL = max(e.lastFlushedWAL, job.walNum)
		if err := e.persistManifestLocked(); err != nil {
			e.tables = e.tables[:len(e.tables)-1]
			e.mu.Unlock()
			_ = st.Close()
			e.failFlush(err)
			return
		}
		e.imms = e.imms[1:]
		e.flushCond.Broadcast()
		e.mu.Unlock()

		_ = os.Remove(filepath.Join(dir, walName(job.walNum)))
		e.log.Info("flushed memtable to sstable", "sstable", sstName(tableNum), "wal", walName(job.walNum))
	}
}

// maybeCompact merges similarly sized SSTables while any group qualifies.
func (e *Engine) maybeCompact() {
	for {
		e.mu.Lock()
		if e.closed || e.flushErr != nil {
			e.mu.Unlock()
			return
		}
		bucket := getSizeTieredBucket(e.tables, e.compactionMinThreshold)
		if bucket == nil {
			e.mu.Unlock()
			return
		}
		full := len(bucket) == len(e.tables)
		outNum := e.nextFileNum
		e.nextFileNum++
		dir := e.dir
		cache := e.cache
		e.mu.Unlock()

		outPath := filepath.Join(dir, sstName(outNum))
		sts := make([]*SSTable, len(bucket))
		var estBytes int64
		for i := range bucket {
			sts[i] = bucket[i].st
			estBytes += bucket[i].size
		}
		expected := int(estBytes / 32)
		if expected < 1 {
			expected = 1
		}

		w, err := NewSSTableWriter(outPath, expected)
		if err != nil {
			e.failFlush(err)
			return
		}
		written, err := mergeTables(sts, full, w)
		if err != nil {
			w.Abort()
			e.failFlush(err)
			return
		}

		// A full compaction can delete every key, leaving nothing to write. Drop the
		// bucket without creating an empty table rather than churning a useless file.
		if written == 0 {
			w.Abort()
			e.mu.Lock()
			orig := e.tables
			e.tables = removeTables(e.tables, bucket)
			if err := e.persistManifestLocked(); err != nil {
				e.tables = orig
				e.mu.Unlock()
				e.failFlush(err)
				return
			}
			e.mu.Unlock()
			for _, t := range bucket {
				_ = t.st.Close()
				_ = os.Remove(filepath.Join(dir, sstName(t.num)))
			}
			e.log.Info("compacted sstables to empty", "inputs", len(bucket))
			continue
		}

		if err := w.Finish(); err != nil {
			_ = os.Remove(outPath)
			e.failFlush(err)
			return
		}
		newSt, err := OpenSSTableWithCache(outPath, outNum, cache)
		if err != nil {
			e.failFlush(err)
			return
		}
		var newSize int64
		if fi, serr := os.Stat(outPath); serr == nil {
			newSize = fi.Size()
		}

		e.mu.Lock()
		orig := e.tables
		kept := removeTables(e.tables, bucket)
		kept = append(kept, tableRef{num: outNum, st: newSt, size: newSize})
		sort.Slice(kept, func(i, j int) bool { return kept[i].num < kept[j].num })
		e.tables = kept
		if err := e.persistManifestLocked(); err != nil {
			e.tables = orig
			e.mu.Unlock()
			_ = newSt.Close()
			_ = os.Remove(outPath)
			e.failFlush(err)
			return
		}
		e.mu.Unlock()

		// The swap held the write lock, so no reader is mid-read on the inputs, and
		// new readers see the updated set. Releasing the input files is now safe.
		for _, t := range bucket {
			_ = t.st.Close()
			_ = os.Remove(filepath.Join(dir, sstName(t.num)))
		}
		e.log.Info("compacted sstables", "inputs", len(bucket), "output", sstName(outNum), "dropped_tombstones", full)
	}
}

// removeTables returns src with every table in drop removed, preserving order.
func removeTables(src, drop []tableRef) []tableRef {
	dropNums := make(map[uint64]bool, len(drop))
	for _, t := range drop {
		dropNums[t.num] = true
	}
	kept := make([]tableRef, 0, len(src))
	for _, t := range src {
		if !dropNums[t.num] {
			kept = append(kept, t)
		}
	}
	return kept
}

func (e *Engine) failFlush(err error) {
	e.mu.Lock()
	if e.flushErr == nil {
		e.flushErr = err
	}
	e.flushCond.Broadcast()
	e.mu.Unlock()
	e.log.Error("background flush or compaction failed", "error", err)
}

func (e *Engine) persistManifestLocked() error {
	nums := make([]uint64, len(e.tables))
	for i := range e.tables {
		nums[i] = e.tables[i].num
	}
	return writeManifest(e.dir, manifestState{
		Tables:         nums,
		NextFileNum:    e.nextFileNum,
		LastFlushedWAL: e.lastFlushedWAL,
		LastSeq:        e.seq,
	})
}

func writeSSTableFromMemtable(path string, mem *Memtable) error {
	w, err := NewSSTableWriter(path, mem.Len())
	if err != nil {
		return err
	}
	var addErr error
	mem.Scan(func(r Record) bool {
		if err := w.Add(r); err != nil {
			addErr = err
			return false
		}
		return true
	})
	if addErr != nil {
		w.Abort()
		return addErr
	}
	return w.Finish()
}

// Sync flushes the active WAL segment to stable storage.
func (e *Engine) Sync() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return ErrClosed
	}
	return e.wal.Sync()
}

// Stats returns a snapshot of engine counters.
func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Stats{
		Keys:        e.mem.Len(),
		ApproxBytes: e.mem.ApproxSize(),
		NextSeq:     e.seq + 1,
		SSTables:    len(e.tables),
		Immutable:   len(e.imms),
	}
}

// Close flushes all pending memtables, stops the background worker, and releases file
// handles. The active memtable is not flushed; its WAL segment is replayed on the next
// Open. Close is idempotent.
func (e *Engine) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	e.flushCond.Broadcast()
	e.mu.Unlock()

	close(e.stopCh)
	e.flusherWG.Wait()

	e.mu.Lock()
	defer e.mu.Unlock()
	var err error
	if e.wal != nil {
		err = e.wal.Close()
	}
	for _, t := range e.tables {
		if cerr := t.st.Close(); err == nil {
			err = cerr
		}
	}
	return err
}
