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
	id    string
	eng   *storage.Engine
	mu    sync.Mutex
	hints *hintStore
}

// NewLocalNode wraps eng as the node identified by id.
func NewLocalNode(id string, eng *storage.Engine) *LocalNode {
	return &LocalNode{id: id, eng: eng, hints: newHintStore()}
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

// PutHint buffers a write destined for intended that could not be delivered to it. The
// hint is held locally, separate from this node's own data, until DeliverHints replays it.
func (n *LocalNode) PutHint(ctx context.Context, intended string, key []byte, vv VersionedValue) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.hints.add(intended, key, vv)
	return nil
}

// DeliverHints attempts to replay buffered hints to their intended nodes through tr,
// removing each hint that is delivered. Unreachable intended nodes are skipped and retried
// on the next call. It returns the number of hints delivered.
func (n *LocalNode) DeliverHints(ctx context.Context, tr Transport) int {
	delivered := 0
	for _, intended := range n.hints.intendedNodes() {
		rep, ok := tr.Replica(intended)
		if !ok {
			continue
		}
		var done [][]byte
		for _, kv := range n.hints.pendingFor(intended) {
			if err := rep.PutVersioned(ctx, kv.Key, kv.Value); err == nil {
				done = append(done, kv.Key)
				delivered++
			}
		}
		n.hints.remove(intended, done)
	}
	return delivered
}

// PendingHints returns how many hints this node currently holds, for tests and metrics.
func (n *LocalNode) PendingHints() int { return n.hints.count() }

// scanVersioned enumerates the node's live entries, decoding each stored value into a
// VersionedValue and applying filter (nil accepts all). It is the basis for both the Merkle
// tree and bucket extraction.
func (n *LocalNode) scanVersioned(ctx context.Context, filter KeyFilter) ([]KeyVersion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var out []KeyVersion
	var scanErr error
	err := n.eng.Scan(func(key, value []byte) bool {
		if filter != nil && !filter(key) {
			return true
		}
		vv, err := decodeVersioned(value)
		if err != nil {
			scanErr = err
			return false
		}
		out = append(out, KeyVersion{Key: append([]byte(nil), key...), Value: vv})
		return true
	})
	if err != nil {
		return nil, err
	}
	if scanErr != nil {
		return nil, scanErr
	}
	return out, nil
}

// MerkleTree builds a Merkle tree over the node's filtered keys.
func (n *LocalNode) MerkleTree(ctx context.Context, filter KeyFilter) (*MerkleTree, error) {
	entries, err := n.scanVersioned(ctx, filter)
	if err != nil {
		return nil, err
	}
	return BuildMerkleTree(entries)
}

// BucketEntries returns the node's filtered entries whose keys fall in any of buckets.
func (n *LocalNode) BucketEntries(ctx context.Context, buckets []int, filter KeyFilter) ([]KeyVersion, error) {
	entries, err := n.scanVersioned(ctx, filter)
	if err != nil {
		return nil, err
	}
	want := make(map[int]bool, len(buckets))
	for _, b := range buckets {
		want[b] = true
	}
	out := entries[:0]
	for _, kv := range entries {
		if want[bucketOf(kv.Key)] {
			out = append(out, kv)
		}
	}
	return out, nil
}
