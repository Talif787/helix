package storage

import (
	"bufio"
	"encoding/binary"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

// maxFrameSize bounds a single WAL frame so that a corrupt length prefix cannot
// trigger an unbounded allocation during replay.
const maxFrameSize = 64 << 20 // 64 MiB

// WAL is an append-only, crash-safe write-ahead log. Each frame is encoded as:
//
//	len(uint32 LE) | crc32(uint32 LE, IEEE, over payload) | payload
//
// On open the log is replayed from the start; the first torn or corrupt frame is
// treated as the end of the log (the expected result of a crash mid-append) and
// the file is truncated to the last fully-valid offset so future appends are clean.
type WAL struct {
	mu   sync.Mutex
	f    *os.File
	sync bool
	buf  []byte // reused payload encode buffer, guarded by mu
}

// OpenWAL opens (creating if necessary) the log at path, replays and returns its
// valid records in write order, truncates any torn tail, and positions the file
// for appending. When sync is true each Append is durably flushed before returning.
func OpenWAL(path string, sync bool) (*WAL, []Record, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, err
	}

	records, offset, err := replayWAL(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if err := f.Truncate(offset); err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, nil, err
	}

	return &WAL{f: f, sync: sync}, records, nil
}

// replayWAL reads every intact frame from the start of f and returns the decoded
// records plus the byte offset just past the last valid frame. Any short read or
// checksum mismatch is interpreted as a torn tail and stops replay cleanly.
func replayWAL(f *os.File) ([]Record, int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, 0, err
	}
	r := bufio.NewReader(f)

	var (
		records []Record
		offset  int64
		header  [8]byte
	)
	for {
		if _, err := io.ReadFull(r, header[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break // clean end of log, or a header torn by a crash
			}
			return nil, 0, err
		}
		length := binary.LittleEndian.Uint32(header[0:4])
		crc := binary.LittleEndian.Uint32(header[4:8])
		if length == 0 || length > maxFrameSize {
			break // implausible length: treat as garbage tail
		}

		payload := make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break // payload torn by a crash
			}
			return nil, 0, err
		}
		if crc32.ChecksumIEEE(payload) != crc {
			break // torn or corrupt frame at the tail
		}
		rec, err := decodeRecordPayload(payload)
		if err != nil {
			break
		}

		records = append(records, rec)
		offset += int64(8) + int64(length)
	}
	return records, offset, nil
}

// Append durably writes a single record. Writing the header and payload in two
// calls is safe: a crash between them leaves a torn tail that replayWAL discards.
func (w *WAL) Append(r *Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return ErrClosed
	}

	w.buf = encodeRecordPayload(w.buf[:0], r)
	payload := w.buf

	var header [8]byte
	binary.LittleEndian.PutUint32(header[0:4], uint32(len(payload)))
	binary.LittleEndian.PutUint32(header[4:8], crc32.ChecksumIEEE(payload))

	if _, err := w.f.Write(header[:]); err != nil {
		return err
	}
	if _, err := w.f.Write(payload); err != nil {
		return err
	}
	if w.sync {
		return w.f.Sync()
	}
	return nil
}

// Sync flushes buffered writes to stable storage. Useful when sync-on-write is off.
func (w *WAL) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return ErrClosed
	}
	return w.f.Sync()
}

// Close flushes and closes the underlying file. It is idempotent.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Sync()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f = nil
	return err
}
