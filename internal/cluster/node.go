package cluster

import (
	"context"

	"github.com/talifpathan/helix/internal/storage"
)

// LocalNode adapts a storage.Engine to the Store interface so the coordinator can route
// requests to it exactly as it will later route them across the network. Each method
// honors context cancellation, then delegates to the engine.
type LocalNode struct {
	id  string
	eng *storage.Engine
}

// NewLocalNode wraps eng as the node identified by id.
func NewLocalNode(id string, eng *storage.Engine) *LocalNode {
	return &LocalNode{id: id, eng: eng}
}

// ID returns the node's identifier.
func (n *LocalNode) ID() string { return n.id }

// Get returns the value for key from this node's engine.
func (n *LocalNode) Get(ctx context.Context, key []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return n.eng.Get(key)
}

// Put stores value under key on this node's engine.
func (n *LocalNode) Put(ctx context.Context, key, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return n.eng.Put(key, value)
}

// Delete records a tombstone for key on this node's engine.
func (n *LocalNode) Delete(ctx context.Context, key []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return n.eng.Delete(key)
}

// Close closes the underlying engine.
func (n *LocalNode) Close() error { return n.eng.Close() }
