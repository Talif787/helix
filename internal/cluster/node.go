package cluster

import (
	"context"
	"errors"
	"sync"

	"github.com/talifpathan/helix/internal/storage"
)

// LocalNode adapts a storage.Engine to the Replica interface. It stores each key as a
// JSON-encoded VersionedValue, and every write is a read-modify-write that reconciles the
// incoming version against what the node already holds, so a replica never loses causal
// history or regresses to an older value. A per-node mutex serializes those
// read-modify-write cycles; finer-grained per-key locking is a later optimization.
type LocalNode struct {
	id  string
	eng *storage.Engine
	mu  sync.Mutex
}

// NewLocalNode wraps eng as the node identified by id.
func NewLocalNode(id string, eng *storage.Engine) *LocalNode {
	return &LocalNode{id: id, eng: eng}
}

// ID returns the node's identifier.
func (n *LocalNode) ID() string { return n.id }

func (n *LocalNode) readDecode(key []byte) (VersionedValue, bool, error) {
	raw, err := n.eng.Get(key)
	if errors.Is(err, storage.ErrNotFound) {
		return VersionedValue{}, false, nil
	}
	if err != nil {
		return VersionedValue{}, false, err
	}
	vv, err := decodeVersioned(raw)
	if err != nil {
		return VersionedValue{}, false, err
	}
	return vv, true, nil
}

// GetVersioned returns the node's current version of key.
func (n *LocalNode) GetVersioned(ctx context.Context, key []byte) (VersionedValue, bool, error) {
	if err := ctx.Err(); err != nil {
		return VersionedValue{}, false, err
	}
	return n.readDecode(key)
}

// PutVersioned reconciles vv against the node's current version and stores the result.
func (n *LocalNode) PutVersioned(ctx context.Context, key []byte, vv VersionedValue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	final := vv
	if cur, ok, err := n.readDecode(key); err != nil {
		return err
	} else if ok {
		final = Reconcile(vv, cur)
	}
	raw, err := encodeVersioned(final)
	if err != nil {
		return err
	}
	return n.eng.Put(key, raw)
}

// Close closes the underlying engine.
func (n *LocalNode) Close() error { return n.eng.Close() }
