package cluster

import (
	"context"
	"sync"
)

// KeyFilter selects which keys participate in an operation. A nil filter means all keys.
// Anti-entropy uses it to scope a repair to the keys two nodes both replicate, so repair
// never copies a key onto a node that should not hold it.
type KeyFilter func(key []byte) bool

// Replica is the versioned operation surface a single node exposes to the coordinator.
// It replaces the raw get/put of the previous phase: a node now stores and returns
// VersionedValue so the coordinator can reconcile divergent copies. In this phase it is
// served in-process; a later phase serves it over the network with the same signatures.
type Replica interface {
	// GetVersioned returns the node's current version of key, or found=false if absent.
	GetVersioned(ctx context.Context, key []byte) (VersionedValue, bool, error)
	// PutVersioned stores vv, reconciling it against any version the node already holds so
	// a replica never regresses to an older or divergent value.
	PutVersioned(ctx context.Context, key []byte, vv VersionedValue) error
	// PutHint buffers vv for key on this node on behalf of intended, a replica that could
	// not be reached. The hint is replayed to intended once it is reachable again.
	PutHint(ctx context.Context, intended string, key []byte, vv VersionedValue) error
	// MerkleTree builds a Merkle tree over the node's keys accepted by filter, for
	// anti-entropy comparison.
	MerkleTree(ctx context.Context, filter KeyFilter) (*MerkleTree, error)
	// BucketEntries returns the node's entries, accepted by filter, whose keys fall in any
	// of the given leaf buckets.
	BucketEntries(ctx context.Context, buckets []int, filter KeyFilter) ([]KeyVersion, error)
}

// Transport resolves a node id to a Replica. The in-process implementation looks the node
// up in a map. A network implementation would return a client stub that dials the node,
// so the coordinator above it is unchanged when nodes move onto separate processes.
type Transport interface {
	Replica(nodeID string) (Replica, bool)
}

// InProcessTransport dispatches operations to replicas running in the same process. It is
// safe for concurrent use, and Deregister lets a test or the demo simulate a node that has
// become unreachable.
type InProcessTransport struct {
	mu    sync.RWMutex
	nodes map[string]Replica
}

// NewInProcessTransport returns an empty transport.
func NewInProcessTransport() *InProcessTransport {
	return &InProcessTransport{nodes: make(map[string]Replica)}
}

// Register makes nodeID resolvable to r. Registering an existing id replaces it.
func (t *InProcessTransport) Register(nodeID string, r Replica) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes[nodeID] = r
}

// Deregister removes nodeID, modeling a node that has gone offline.
func (t *InProcessTransport) Deregister(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.nodes, nodeID)
}

// Replica resolves nodeID, reporting ok=false if it is not currently registered.
func (t *InProcessTransport) Replica(nodeID string) (Replica, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	r, ok := t.nodes[nodeID]
	return r, ok
}
