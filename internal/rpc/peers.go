// Package rpc carries the network layer that puts Helix nodes on real sockets. Until now a
// cluster ran in one process behind the in-process transport; this package will implement
// the same Replica, Transport, and Messenger seams over gRPC so a coordinator can reach
// replicas on other machines. This first part establishes the peer address registry that
// the networked transport uses to resolve a node id to a dial target; the gRPC server,
// client, and transport are added next.
package rpc

import (
	"sort"
	"sync"
)

// PeerRegistry maps node ids to network addresses ("host:port"). The networked transport
// consults it to dial the replica that owns a given node id. It is safe for concurrent use,
// since membership changes and request routing happen on different goroutines.
type PeerRegistry struct {
	mu    sync.RWMutex
	addrs map[string]string
}

// NewPeerRegistry returns an empty registry. Pass seed addresses to Set afterward, or build
// one from a config map with NewPeerRegistryFromMap.
func NewPeerRegistry() *PeerRegistry {
	return &PeerRegistry{addrs: make(map[string]string)}
}

// NewPeerRegistryFromMap returns a registry seeded from a copy of nodeID->address.
func NewPeerRegistryFromMap(seed map[string]string) *PeerRegistry {
	r := NewPeerRegistry()
	for id, addr := range seed {
		r.addrs[id] = addr
	}
	return r
}

// Set records or updates the address for nodeID.
func (r *PeerRegistry) Set(nodeID, addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addrs[nodeID] = addr
}

// Remove drops nodeID from the registry, if present.
func (r *PeerRegistry) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.addrs, nodeID)
}

// Address returns the address registered for nodeID.
func (r *PeerRegistry) Address(nodeID string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	addr, ok := r.addrs[nodeID]
	return addr, ok
}

// Nodes returns the registered node ids, sorted for deterministic iteration.
func (r *PeerRegistry) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.addrs))
	for id := range r.addrs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Snapshot returns a copy of the current nodeID->address mapping, safe for the caller to
// keep and mutate.
func (r *PeerRegistry) Snapshot() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]string, len(r.addrs))
	for id, addr := range r.addrs {
		out[id] = addr
	}
	return out
}

// Len returns how many peers are registered.
func (r *PeerRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.addrs)
}
