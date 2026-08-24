package cluster

import (
	"context"
	"sync"
)

// Store is the operation surface a single node exposes to the rest of the cluster. It
// mirrors the storage engine's core operations but takes a context, because a request
// routed to another node is cancelable and, in a later phase, crosses the network.
type Store interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Put(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
}

// Transport resolves a node ID to a Store. The in-process implementation looks the node
// up in a map. A network implementation would return a client stub that dials the node,
// so the coordinator above it is unchanged when nodes move onto separate processes.
type Transport interface {
	Store(nodeID string) (Store, bool)
}

// InProcessTransport dispatches operations to nodes running in the same process. It is
// safe for concurrent use.
type InProcessTransport struct {
	mu    sync.RWMutex
	nodes map[string]Store
}

// NewInProcessTransport returns an empty transport.
func NewInProcessTransport() *InProcessTransport {
	return &InProcessTransport{nodes: make(map[string]Store)}
}

// Register makes nodeID resolvable to s. Registering an existing id replaces it.
func (t *InProcessTransport) Register(nodeID string, s Store) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nodes[nodeID] = s
}

// Deregister removes nodeID from the transport.
func (t *InProcessTransport) Deregister(nodeID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.nodes, nodeID)
}

// Store resolves nodeID, reporting ok=false if it is not registered.
func (t *InProcessTransport) Store(nodeID string) (Store, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s, ok := t.nodes[nodeID]
	return s, ok
}
