package storage

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Default size limits used when Options leaves them unset.
const (
	DefaultMaxKeyBytes   = 64 << 10  // 64 KiB
	DefaultMaxValueBytes = 1 << 20   // 1 MiB
	walFileName          = "wal.log"
)

// Options configures an Engine. It is a plain value object so the storage package
// stays independent of the application configuration package (dependency points
// inward, not outward).
type Options struct {
	// DataDir is the directory that holds the write-ahead log and, later, SSTables.
	DataDir string
	// SyncWrites forces an fsync on every write when true, trading throughput for
	// the strongest durability. When false, durability is guaranteed at Close and Sync.
	SyncWrites bool
	// MaxKeyBytes and MaxValueBytes bound accepted inputs. Zero selects the defaults.
	MaxKeyBytes   int
	MaxValueBytes int
	// Logger receives structured lifecycle logs. Nil selects slog.Default().
	Logger *slog.Logger
}

// Stats is a point-in-time snapshot of engine state, useful for observability.
type Stats struct {
	Keys        int
	ApproxBytes int64
	NextSeq     uint64
}

// Engine is a durable, single-node, ordered key-value store. Writes are appended
// to the write-ahead log and then applied to the in-memory memtable; reads are
// served from the memtable. On Open the log is replayed so no acknowledged write
// is lost across a restart.
//
// Write path serialization: writes hold e.mu so that sequence-number assignment
// and log append order agree. Reads do not take e.mu; they rely on the memtable's
// own lock and an atomic closed flag, so reads never block behind writes.
type Engine struct {
	log    *slog.Logger
	mu     sync.Mutex
	mem    *Memtable
	wal    *WAL
	seq    uint64
	closed atomic.Bool
	maxKey int
	maxVal int
}

// Open initializes an engine rooted at opts.DataDir, recovering any prior state
// from the write-ahead log.
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
	if err := os.MkdirAll(opts.DataDir, 0o755); err != nil {
		return nil, err
	}

	wal, records, err := OpenWAL(filepath.Join(opts.DataDir, walFileName), opts.SyncWrites)
	if err != nil {
		return nil, err
	}

	mem := NewMemtable()
	var maxSeq uint64
	for i := range records {
		mem.Put(records[i])
		if records[i].Seq > maxSeq {
			maxSeq = records[i].Seq
		}
	}

	e := &Engine{
		log:    opts.Logger,
		mem:    mem,
		wal:    wal,
		seq:    maxSeq,
		maxKey: opts.MaxKeyBytes,
		maxVal: opts.MaxValueBytes,
	}
	e.log.Info("storage engine opened",
		"data_dir", opts.DataDir,
		"recovered_records", len(records),
		"next_seq", maxSeq+1,
		"sync_writes", opts.SyncWrites,
	)
	return e, nil
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
	if e.closed.Load() {
		return ErrClosed
	}

	e.seq++
	rec := Record{Seq: e.seq, Kind: kind, Key: cloneBytes(key), Value: cloneBytes(value)}

	if err := e.wal.Append(&rec); err != nil {
		// The sequence number is intentionally not rolled back. If the append
		// partially succeeded, the record may survive to the next replay under
		// this seq; reusing it would risk a duplicate. A gap is harmless. A write
		// that returns an error must be treated by the caller as an unknown outcome
		// and retried idempotently.
		return err
	}

	e.mem.Put(rec)
	return nil
}

// Put stores value under key.
func (e *Engine) Put(key, value []byte) error { return e.write(KindSet, key, value) }

// Delete records a tombstone for key. A subsequent Get returns ErrNotFound.
func (e *Engine) Delete(key []byte) error { return e.write(KindDelete, key, nil) }

// Get returns the value stored under key, or ErrNotFound if the key is absent or
// deleted. The returned slice is a copy the caller may retain and mutate freely.
func (e *Engine) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, ErrEmptyKey
	}
	if e.closed.Load() {
		return nil, ErrClosed
	}
	rec, ok := e.mem.Get(key)
	if !ok || rec.Kind == KindDelete {
		return nil, ErrNotFound
	}
	return cloneBytes(rec.Value), nil
}

// Sync flushes the write-ahead log to stable storage. It is a no-op distinct from
// SyncWrites: callers running with SyncWrites off can force durability on demand.
func (e *Engine) Sync() error {
	if e.closed.Load() {
		return ErrClosed
	}
	return e.wal.Sync()
}

// Stats returns a snapshot of engine counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	seq := e.seq
	e.mu.Unlock()
	return Stats{Keys: e.mem.Len(), ApproxBytes: e.mem.ApproxSize(), NextSeq: seq + 1}
}

// Close flushes and closes the engine. It is idempotent and safe to call once
// from a deferred shutdown path.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Swap(true) {
		return nil
	}
	return e.wal.Close()
}
