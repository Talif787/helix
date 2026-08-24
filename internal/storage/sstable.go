package storage

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
)

// SSTable on-disk layout, written in this order:
//
//	[data block 0][data block 1]...[data block N-1][filter block][index block][footer]
//
// Data blocks hold entries sorted by key. Each entry is:
//
//	uvarint(keyLen) | key | uvarint(seq) | kind(1) | uvarint(valLen) | value
//
// The filter block is a serialized bloom filter over every key. The index block
// has one entry per data block giving that block's last key, byte offset, and byte
// length. The fixed-size footer points at the filter and index blocks and carries
// a magic number for validation.
//
// A written SSTable is immutable, so reads use ReadAt and are safe for concurrent
// use without locking.
const (
	sstBlockTargetSize = 4096
	sstFooterSize      = 40
	sstMagic           = uint64(0x48454c4958535354) // "HELIXSST"
)

type indexEntry struct {
	lastKey []byte
	offset  uint64
	length  uint64
}

// SSTableWriter builds an SSTable. Keys must be added in strictly ascending order,
// which is what the memtable's ordered scan and the compaction merge both produce.
type SSTableWriter struct {
	f        *os.File
	bw       *bufio.Writer
	offset   uint64
	bloom    *Bloom
	index    []indexEntry
	curBlock []byte
	curLast  []byte
	prevKey  []byte
	finished bool
}

// NewSSTableWriter creates path and prepares a writer sized for expectedKeys.
func NewSSTableWriter(path string, expectedKeys int) (*SSTableWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &SSTableWriter{
		f:     f,
		bw:    bufio.NewWriter(f),
		bloom: NewBloom(expectedKeys, 0.01),
	}, nil
}

func (w *SSTableWriter) writeRaw(p []byte) error {
	n, err := w.bw.Write(p)
	w.offset += uint64(n)
	return err
}

// Add appends one record. It returns an error if keys are not strictly ascending.
func (w *SSTableWriter) Add(r Record) error {
	if w.finished {
		return errors.New("sstable: writer already finished")
	}
	if w.prevKey != nil && bytes.Compare(r.Key, w.prevKey) <= 0 {
		return fmt.Errorf("sstable: keys must be strictly ascending, got %q after %q", r.Key, w.prevKey)
	}
	w.prevKey = cloneBytes(r.Key)

	w.bloom.Add(r.Key)
	w.curBlock = appendEntry(w.curBlock, &r)
	w.curLast = cloneBytes(r.Key)

	if len(w.curBlock) >= sstBlockTargetSize {
		return w.flushBlock()
	}
	return nil
}

func (w *SSTableWriter) flushBlock() error {
	if len(w.curBlock) == 0 {
		return nil
	}
	off := w.offset
	if err := w.writeRaw(w.curBlock); err != nil {
		return err
	}
	w.index = append(w.index, indexEntry{
		lastKey: w.curLast,
		offset:  off,
		length:  uint64(len(w.curBlock)),
	})
	w.curBlock = w.curBlock[:0]
	w.curLast = nil
	return nil
}

// Finish flushes the final block, writes the filter, index, and footer, then syncs
// and closes the file. The writer must not be used afterwards.
func (w *SSTableWriter) Finish() error {
	if w.finished {
		return nil
	}
	if err := w.flushBlock(); err != nil {
		return err
	}

	filterOff := w.offset
	filterBytes := w.bloom.Bytes()
	if err := w.writeRaw(filterBytes); err != nil {
		return err
	}

	indexOff := w.offset
	var idx []byte
	for i := range w.index {
		idx = binary.AppendUvarint(idx, uint64(len(w.index[i].lastKey)))
		idx = append(idx, w.index[i].lastKey...)
		idx = binary.AppendUvarint(idx, w.index[i].offset)
		idx = binary.AppendUvarint(idx, w.index[i].length)
	}
	if err := w.writeRaw(idx); err != nil {
		return err
	}

	var footer [sstFooterSize]byte
	binary.LittleEndian.PutUint64(footer[0:8], filterOff)
	binary.LittleEndian.PutUint64(footer[8:16], uint64(len(filterBytes)))
	binary.LittleEndian.PutUint64(footer[16:24], indexOff)
	binary.LittleEndian.PutUint64(footer[24:32], uint64(len(idx)))
	binary.LittleEndian.PutUint64(footer[32:40], sstMagic)
	if err := w.writeRaw(footer[:]); err != nil {
		return err
	}

	if err := w.bw.Flush(); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	w.finished = true
	return w.f.Close()
}

// Abort closes and removes a partially written file. It is safe to defer.
func (w *SSTableWriter) Abort() {
	if w.finished {
		return
	}
	name := w.f.Name()
	_ = w.f.Close()
	_ = os.Remove(name)
}

func appendEntry(dst []byte, r *Record) []byte {
	dst = binary.AppendUvarint(dst, uint64(len(r.Key)))
	dst = append(dst, r.Key...)
	dst = binary.AppendUvarint(dst, r.Seq)
	dst = append(dst, byte(r.Kind))
	dst = binary.AppendUvarint(dst, uint64(len(r.Value)))
	dst = append(dst, r.Value...)
	return dst
}

// decodeEntry parses one entry and returns it with the number of bytes consumed.
func decodeEntry(data []byte) (Record, int, error) {
	orig := len(data)
	var r Record

	klen, n := binary.Uvarint(data)
	if n <= 0 {
		return r, 0, ErrCorruptRecord
	}
	data = data[n:]
	if uint64(len(data)) < klen {
		return r, 0, ErrCorruptRecord
	}
	r.Key = cloneBytes(data[:klen])
	data = data[klen:]

	seq, n := binary.Uvarint(data)
	if n <= 0 {
		return r, 0, ErrCorruptRecord
	}
	data = data[n:]
	r.Seq = seq

	if len(data) < 1 {
		return r, 0, ErrCorruptRecord
	}
	r.Kind = Kind(data[0])
	data = data[1:]
	if !r.Kind.valid() {
		return r, 0, ErrCorruptRecord
	}

	vlen, n := binary.Uvarint(data)
	if n <= 0 {
		return r, 0, ErrCorruptRecord
	}
	data = data[n:]
	if uint64(len(data)) < vlen {
		return r, 0, ErrCorruptRecord
	}
	r.Value = cloneBytes(data[:vlen])
	data = data[vlen:]

	return r, orig - len(data), nil
}

// SSTable is a read-only handle to an on-disk table. Get is safe for concurrent use.
// When cache is non-nil, data blocks are served from and populated into it, keyed by
// (num, offset); num identifies this table within the shared cache.
type SSTable struct {
	f     *os.File
	path  string
	num   uint64
	cache *BlockCache
	bloom *Bloom
	index []indexEntry
}

// OpenSSTable opens an SSTable for reading with no block cache.
func OpenSSTable(path string) (*SSTable, error) {
	return openSSTable(path, 0, nil)
}

// OpenSSTableWithCache opens an SSTable that serves data blocks through cache,
// identifying its blocks by num.
func OpenSSTableWithCache(path string, num uint64, cache *BlockCache) (*SSTable, error) {
	return openSSTable(path, num, cache)
}

func openSSTable(path string, num uint64, cache *BlockCache) (*SSTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	size := fi.Size()
	if size < sstFooterSize {
		_ = f.Close()
		return nil, fmt.Errorf("sstable: file too small to be valid: %s", path)
	}

	var footer [sstFooterSize]byte
	if _, err := f.ReadAt(footer[:], size-sstFooterSize); err != nil {
		_ = f.Close()
		return nil, err
	}
	if binary.LittleEndian.Uint64(footer[32:40]) != sstMagic {
		_ = f.Close()
		return nil, fmt.Errorf("sstable: bad magic in %s", path)
	}
	filterOff := binary.LittleEndian.Uint64(footer[0:8])
	filterLen := binary.LittleEndian.Uint64(footer[8:16])
	indexOff := binary.LittleEndian.Uint64(footer[16:24])
	indexLen := binary.LittleEndian.Uint64(footer[24:32])

	filterBuf := make([]byte, filterLen)
	if _, err := f.ReadAt(filterBuf, int64(filterOff)); err != nil {
		_ = f.Close()
		return nil, err
	}
	bloom, err := LoadBloom(filterBuf)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	indexBuf := make([]byte, indexLen)
	if _, err := f.ReadAt(indexBuf, int64(indexOff)); err != nil {
		_ = f.Close()
		return nil, err
	}
	index, err := parseIndex(indexBuf)
	if err != nil {
		_ = f.Close()
		return nil, err
	}

	return &SSTable{f: f, path: path, num: num, cache: cache, bloom: bloom, index: index}, nil
}

func parseIndex(buf []byte) ([]indexEntry, error) {
	var entries []indexEntry
	for len(buf) > 0 {
		klen, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, ErrCorruptRecord
		}
		buf = buf[n:]
		if uint64(len(buf)) < klen {
			return nil, ErrCorruptRecord
		}
		lastKey := cloneBytes(buf[:klen])
		buf = buf[klen:]

		off, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, ErrCorruptRecord
		}
		buf = buf[n:]

		length, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, ErrCorruptRecord
		}
		buf = buf[n:]

		entries = append(entries, indexEntry{lastKey: lastKey, offset: off, length: length})
	}
	return entries, nil
}

// Get returns the record stored for key. The bool reports whether the key was
// found in this table; the returned record may be a tombstone, which the caller
// interprets. A miss reported by the bloom filter avoids any disk read.
func (st *SSTable) Get(key []byte) (Record, bool, error) {
	if len(st.index) == 0 || !st.bloom.MayContain(key) {
		return Record{}, false, nil
	}

	i := sort.Search(len(st.index), func(i int) bool {
		return bytes.Compare(st.index[i].lastKey, key) >= 0
	})
	if i == len(st.index) {
		return Record{}, false, nil
	}

	block, err := st.readBlock(st.index[i].offset, st.index[i].length)
	if err != nil {
		return Record{}, false, err
	}
	for len(block) > 0 {
		r, n, err := decodeEntry(block)
		if err != nil {
			return Record{}, false, err
		}
		block = block[n:]
		switch bytes.Compare(r.Key, key) {
		case 0:
			return r, true, nil
		case 1:
			return Record{}, false, nil // passed where the key would be
		}
	}
	return Record{}, false, nil
}

// readBlock returns the data block at offset, serving it from the cache when present
// and populating the cache on a miss. Cached blocks are immutable; decodeEntry copies
// keys and values out, so sharing a cached block across readers is safe.
func (st *SSTable) readBlock(offset, length uint64) ([]byte, error) {
	if b, ok := st.cache.Get(st.num, offset); ok {
		return b, nil
	}
	block := make([]byte, length)
	if _, err := st.f.ReadAt(block, int64(offset)); err != nil {
		return nil, err
	}
	st.cache.Put(st.num, offset, block)
	return block, nil
}

// Path returns the file path backing this table.
func (st *SSTable) Path() string { return st.path }

// Close releases the underlying file handle.
func (st *SSTable) Close() error { return st.f.Close() }

// blockCount reports the number of data blocks; used in tests.
func (st *SSTable) blockCount() int { return len(st.index) }

// SSTableIterator yields every record in ascending key order. It is used by
// compaction in a later phase and by tests.
type SSTableIterator struct {
	st       *SSTable
	blockIdx int
	block    []byte
	err      error
}

// Iterator returns a fresh iterator positioned before the first record.
func (st *SSTable) Iterator() *SSTableIterator {
	return &SSTableIterator{st: st}
}

// Next returns the next record and true, or a zero record and false at the end or
// on error. Check Err after a false result to distinguish the two.
func (it *SSTableIterator) Next() (Record, bool) {
	for len(it.block) == 0 {
		if it.blockIdx >= len(it.st.index) {
			return Record{}, false
		}
		e := it.st.index[it.blockIdx]
		it.blockIdx++
		buf := make([]byte, e.length)
		if _, err := it.st.f.ReadAt(buf, int64(e.offset)); err != nil {
			it.err = err
			return Record{}, false
		}
		it.block = buf
	}

	r, n, err := decodeEntry(it.block)
	if err != nil {
		it.err = err
		return Record{}, false
	}
	it.block = it.block[n:]
	return r, true
}

// Err reports the first error encountered during iteration, if any.
func (it *SSTableIterator) Err() error { return it.err }
