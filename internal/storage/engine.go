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
	DefaultMaxKeyBytes       = 64 << 10 // 64 KiB
	DefaultMaxValueBytes     = 1 << 20  // 1 MiB
	DefaultMemtableMaxBytes  = 4 << 20  // 4 MiB
	maxImmutablesBeforeStall = 4        // writers block when this many memtables await flush
)

// Options configures an Engine. It is a plain value object so the storage package
// stays independent of the application configuration package.
type Options struct {
	DataDir          string
	SyncWrites       bool
	MaxKeyBytes      int
	MaxValueBytes    int
	MemtableMaxBytes int
	Logger           *slog.Logger
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
	num uint64
	st  *SSTable
}

// Engine is a durable, single-node, ordered key-value store built as a log-structured
// merge tree. Writes append to a per-memtable WAL segment and then apply to the active
// memtable. When the active memtable fills it is sealed into an immutable queue and a
// background goroutine flushes it to an immutable SSTable; a fresh memtable and WAL
// segment take over immediately, so writes do not stall for the flush. Reads merge the
// active memtable, the immutable memtables (newest first), and the SSTables (newest
// first), returning the first match, where a tombstone means the key is deleted.
//
// A single mutex serializes writes and structural changes. Reads take the mutex only
// to snapshot the current sources, then query those snapshots without holding it, so
// reads never block behind a flush.
type Engine struct {
	dir       string
	sync      bool
	threshold int64
	maxKey    int
	maxVal    int
	log       *slog.Logger

	mu        sync.Mutex
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
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return nil, err
	}

	man, _, err := loadManifest(opts.DataDir)
	if err != nil {
		return nil, err
	}

	e := &Engine{
		dir:            opts.DataDir,
		sync:           opts.SyncWrites,
		threshold:      threshold,
		maxKey:         opts.MaxKeyBytes,
		maxVal:         opts.MaxValueBytes,
		log:            opts.Logger,
		lastFlushedWAL: man.LastFlushedWAL,
		flushCh:        make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
	}
	e.flushCond = sync.NewCond(&e.mu)

	var maxNum uint64

	// Open the SSTables recorded in the manifest, oldest first.
	for _, num := range man.Tables {
		st, err := OpenSSTable(filepath.Join(opts.DataDir, sstName(num)))
		if err != nil {
			e.closeTables()
			return nil, fmt.Errorf("open sstable %d: %w", num, err)
		}
		e.tables = append(e.tables, tableRef{num: num, st: st})
		if num > maxNum {
			maxNum = num
		}
	}

	// Find WAL segments and drop any already fully flushed.
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

	// Replay unflushed WAL segments. The highest becomes the active memtable; any
	// earlier ones are immutable memtables to be re-flushed (a crash left them behind).
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

	// With no recovered active memtable, start a fresh one and WAL segment.
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
	if len(e.imms) > 0 {
		e.signalFlush()
	}

	e.log.Info("storage engine opened",
		"data_dir", opts.DataDir,
		"sstables", len(e.tables),
		"recovered_immutables", len(e.imms),
		"active_wal", walName(e.walNum),
		"next_seq", maxSeq+1,
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

	// Backpressure: block while too many memtables await flush.
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

// rotateLocked seals the active memtable into the immutable queue and starts a fresh
// active memtable and WAL segment. The caller must hold e.mu.
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
// the active memtable, the immutable memtables, and the SSTables in recency order.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, ErrEmptyKey
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, ErrClosed
	}
	mem := e.mem
	imms := e.imms
	tables := e.tables
	e.mu.Unlock()

	if rec, ok := mem.Get(key); ok {
		return interpretRecord(rec)
	}
	for i := len(imms) - 1; i >= 0; i-- {
		if rec, ok := imms[i].mem.Get(key); ok {
			return interpretRecord(rec)
		}
	}
	for i := len(tables) - 1; i >= 0; i-- {
		rec, ok, err := tables[i].st.Get(key)
		if err != nil {
			return nil, err
		}
		if ok {
			return interpretRecord(rec)
		}
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
// written to an SSTable. It is primarily useful in tests and for clean checkpoints.
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
		e.mu.Unlock()

		path := filepath.Join(dir, sstName(tableNum))
		if err := writeSSTableFromMemtable(path, job.mem); err != nil {
			_ = os.Remove(path)
			e.failFlush(err)
			return
		}
		st, err := OpenSSTable(path)
		if err != nil {
			e.failFlush(err)
			return
		}

		e.mu.Lock()
		e.tables = append(e.tables, tableRef{num: tableNum, st: st})
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

func (e *Engine) failFlush(err error) {
	e.mu.Lock()
	if e.flushErr == nil {
		e.flushErr = err
	}
	e.flushCond.Broadcast()
	e.mu.Unlock()
	e.log.Error("flush failed", "error", err)
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
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrClosed
	}
	return e.wal.Sync()
}

// Stats returns a snapshot of engine counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Stats{
		Keys:        e.mem.Len(),
		ApproxBytes: e.mem.ApproxSize(),
		NextSeq:     e.seq + 1,
		SSTables:    len(e.tables),
		Immutable:   len(e.imms),
	}
}

// Close flushes all pending memtables, stops the background flusher, and releases
// file handles. The active memtable is not flushed; its WAL segment is replayed on
// the next Open. Close is idempotent.
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
