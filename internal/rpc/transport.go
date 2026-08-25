package rpc

import (
	"sync"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/cluster"
)

// GRPCTransport resolves a node id to a gRPC client, satisfying cluster.Transport so a
// Coordinator can route to remote replicas. It looks up the node's address in a
// PeerRegistry and dials lazily, caching one client per node id. It is safe for concurrent
// use.
type GRPCTransport struct {
	peers    *PeerRegistry
	dialOpts []grpc.DialOption

	mu      sync.Mutex
	clients map[string]*NodeClient
}

// compile-time check that GRPCTransport satisfies the cluster transport contract.
var _ cluster.Transport = (*GRPCTransport)(nil)

// NewGRPCTransport builds a transport backed by peers. Dial options (for example TLS
// credentials in a later part) apply to every client; with none, clients dial plaintext.
func NewGRPCTransport(peers *PeerRegistry, dialOpts ...grpc.DialOption) *GRPCTransport {
	return &GRPCTransport{
		peers:    peers,
		dialOpts: dialOpts,
		clients:  make(map[string]*NodeClient),
	}
}

// Replica returns a client to nodeID, dialing and caching it on first use. It reports
// false only when the node id has no registered address; because grpc.NewClient is lazy,
// a dial here does not fail just because the peer is momentarily down (that surfaces on the
// RPC instead, which the coordinator already treats as an unavailable replica).
func (t *GRPCTransport) Replica(nodeID string) (cluster.Replica, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if c, ok := t.clients[nodeID]; ok {
		return c, true
	}
	addr, ok := t.peers.Address(nodeID)
	if !ok {
		return nil, false
	}
	c, err := DialNode(addr, t.dialOpts...)
	if err != nil {
		return nil, false
	}
	t.clients[nodeID] = c
	return c, true
}

// Close shuts every cached client connection, returning the first error.
func (t *GRPCTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	var firstErr error
	for id, c := range t.clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(t.clients, id)
	}
	return firstErr
}
