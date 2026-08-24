package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const manifestFileName = "MANIFEST"

// manifestState is the durable record of engine metadata. It is small and rewritten
// atomically (write a temp file, fsync, rename) so a crash never leaves it partial.
type manifestState struct {
	// Tables lists the file numbers of live SSTables, in ascending (oldest first) order.
	Tables []uint64 `json:"tables"`
	// NextFileNum is the next number to allocate for a WAL segment or SSTable.
	NextFileNum uint64 `json:"next_file_num"`
	// LastFlushedWAL is the highest WAL segment number whose data is fully persisted
	// in an SSTable. On recovery, WAL segments at or below this are stale and dropped.
	LastFlushedWAL uint64 `json:"last_flushed_wal"`
	// LastSeq is the highest record sequence number assigned as of the last manifest
	// write. Recovery restores the counter to at least this, so sequence numbers stay
	// monotonic even when all data has been flushed and the active WAL is empty.
	LastSeq uint64 `json:"last_seq"`
}

// loadManifest reads the manifest. The bool reports whether a manifest existed; a
// fresh data directory returns a zero state and false with no error.
func loadManifest(dir string) (manifestState, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, manifestFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return manifestState{}, false, nil
		}
		return manifestState{}, false, err
	}
	var m manifestState
	if err := json.Unmarshal(data, &m); err != nil {
		return manifestState{}, false, err
	}
	return m, true, nil
}

// writeManifest atomically persists the manifest.
func writeManifest(dir string, m manifestState) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifestFileName+".tmp")
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, manifestFileName))
}
