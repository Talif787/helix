package storage

import "errors"

// Sentinel errors are wrapped-friendly (compare with errors.Is) so callers can
// branch on failure classes without string matching.
var (
	// ErrNotFound is returned when a key is absent or shadowed by a tombstone.
	ErrNotFound = errors.New("storage: key not found")
	// ErrEmptyKey is returned when a zero-length key is supplied.
	ErrEmptyKey = errors.New("storage: key must not be empty")
	// ErrKeyTooLarge is returned when a key exceeds the configured maximum.
	ErrKeyTooLarge = errors.New("storage: key exceeds maximum size")
	// ErrValueTooLarge is returned when a value exceeds the configured maximum.
	ErrValueTooLarge = errors.New("storage: value exceeds maximum size")
	// ErrClosed is returned when an operation is attempted on a closed engine.
	ErrClosed = errors.New("storage: engine is closed")
	// ErrCorruptRecord indicates a write-ahead log frame failed decoding.
	ErrCorruptRecord = errors.New("storage: corrupt write-ahead log record")
	// ErrNoDataDir is returned when Options.DataDir is not set.
	ErrNoDataDir = errors.New("storage: DataDir is required")
)
