package cluster

import "context"

// Coordinator routes each operation to the node that owns the key on the ring and invokes
// it through the transport. In this phase there is a single owner per key and no
// replication; the preference list that replication needs is already available from
// Ring.LookupN, so adding replication later does not change routing.
type Coordinator struct {
	ring *Ring
	tr   Transport
}

// NewCoordinator binds a ring to a transport.
func NewCoordinator(ring *Ring, tr Transport) *Coordinator {
	return &Coordinator{ring: ring, tr: tr}
}

// storeFor resolves the owning node's Store for key, returning a typed error when the
// ring is empty or the owner is not reachable through the transport.
func (c *Coordinator) storeFor(key []byte) (Store, string, error) {
	nodeID, ok := c.ring.Lookup(key)
	if !ok {
		return nil, "", ErrNoNodes
	}
	s, ok := c.tr.Store(nodeID)
	if !ok {
		return nil, nodeID, ErrNodeUnavailable
	}
	return s, nodeID, nil
}

// Get routes a read to the owning node.
func (c *Coordinator) Get(ctx context.Context, key []byte) ([]byte, error) {
	s, _, err := c.storeFor(key)
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, key)
}

// Put routes a write to the owning node.
func (c *Coordinator) Put(ctx context.Context, key, value []byte) error {
	s, _, err := c.storeFor(key)
	if err != nil {
		return err
	}
	return s.Put(ctx, key, value)
}

// Delete routes a delete to the owning node.
func (c *Coordinator) Delete(ctx context.Context, key []byte) error {
	s, _, err := c.storeFor(key)
	if err != nil {
		return err
	}
	return s.Delete(ctx, key)
}
