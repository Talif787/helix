// Package simulation drives a Helix cluster in a single process through a controllable
// network, so tests can inject partitions, message loss, and latency and check how the
// cluster behaves. The network's behavior is driven by a seed: the sequence of drop and
// latency decisions is reproducible for a given seed (when drawn sequentially), and partitions
// are set explicitly by the test, so a failing scenario can be replayed.
//
// Scope and honesty: this is a seeded, fault-injecting simulation, not a bit-for-bit
// deterministic scheduler. The coordinator runs concurrent requests on real goroutines, so the
// interleaving of those goroutines (and therefore the exact order of random draws across
// concurrent callers) is not controlled. What is reproducible is the partition schedule (set by
// the test) and, in sequential use, the fault draws. That is enough to reproduce the
// high-value failures (quorum loss under partition, availability of each side of a split).
package simulation

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/talifpathan/helix/internal/cluster"
)

// Faults is the per-message fault model a caller sets: an independent chance each replica call
// is dropped, and a latency drawn uniformly from [MinLatency, MaxLatency]. It is a plain value
// with no mutable state, so it is safe to copy and pass around.
type Faults struct {
	DropProbability float64
	MinLatency      time.Duration
	MaxLatency      time.Duration
}

// faultState is the runtime side of the fault model: the seeded RNG and its guard. It is never
// copied (always used by pointer), so the mutex it holds is meaningful.
type faultState struct {
	cfg Faults

	mu  sync.Mutex
	rng *rand.Rand
}

func newFaults(f Faults, seed int64) *faultState {
	return &faultState{cfg: f, rng: rand.New(rand.NewSource(seed))}
}

func (f *faultState) shouldDrop() bool {
	if f.cfg.DropProbability <= 0 {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rng.Float64() < f.cfg.DropProbability
}

func (f *faultState) latency() time.Duration {
	if f.cfg.MaxLatency <= 0 {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	span := f.cfg.MaxLatency - f.cfg.MinLatency
	if span <= 0 {
		return f.cfg.MinLatency
	}
	return f.cfg.MinLatency + time.Duration(f.rng.Int63n(int64(span)))
}

// Network models message reachability and faults between nodes. A partition splits the nodes
// into an isolated set and the rest: nodes on the same side reach each other, nodes on opposite
// sides do not. It is safe for concurrent use.
type Network struct {
	faults *faultState

	mu       sync.Mutex
	isolated map[string]bool
	hung     map[string]bool
}

func newNetwork(faults *faultState) *Network {
	return &Network{faults: faults, isolated: map[string]bool{}, hung: map[string]bool{}}
}

// Partition isolates the given nodes into their own group: they can talk to each other but not
// to any node outside the set, and vice versa. It replaces any previous partition.
func (n *Network) Partition(nodes ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.isolated = make(map[string]bool, len(nodes))
	for _, id := range nodes {
		n.isolated[id] = true
	}
}

// Heal removes all partitions.
func (n *Network) Heal() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.isolated = map[string]bool{}
}

// Hang marks the given nodes as hung: a call to one of them is accepted (it is reachable) but
// never answers, so the caller blocks until its context deadline. This models a node that is
// present in the ring and resolvable but not actually serving, for example a pod whose DNS
// resolves before the process is listening. It replaces any previous hung set.
func (n *Network) Hang(nodes ...string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hung = make(map[string]bool, len(nodes))
	for _, id := range nodes {
		n.hung[id] = true
	}
}

// Unhang clears all hung nodes.
func (n *Network) Unhang() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hung = map[string]bool{}
}

func (n *Network) isHung(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.hung[id]
}

func (n *Network) reachable(from, to string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.isolated[from] == n.isolated[to]
}

// gate applies the network model to one replica call: a self-call is always local and
// instant; a call across a partition or a dropped call fails; otherwise the configured latency
// is applied before the call proceeds.
func (n *Network) gate(ctx context.Context, from, to string) error {
	if from == to {
		return nil // local: a node reaching its own engine never crosses the network
	}
	if !n.reachable(from, to) {
		return cluster.ErrNodeUnavailable
	}
	if n.isHung(to) {
		<-ctx.Done() // reachable but never answers: block until the caller's deadline
		return ctx.Err()
	}
	if n.faults.shouldDrop() {
		return cluster.ErrNodeUnavailable
	}
	if d := n.faults.latency(); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// SimTransport implements cluster.Transport for one node ("self"), returning replica handles
// whose every call is gated through the shared Network.
type SimTransport struct {
	self  string
	net   *Network
	nodes map[string]cluster.Replica
}

var _ cluster.Transport = (*SimTransport)(nil)

// Replica returns a gated handle to the named node, or (nil, false) if the node is unknown.
func (t *SimTransport) Replica(id string) (cluster.Replica, bool) {
	r, ok := t.nodes[id]
	if !ok {
		return nil, false
	}
	return &simReplica{from: t.self, to: id, net: t.net, inner: r}, true
}

// simReplica wraps a real replica and gates each method through the network.
type simReplica struct {
	from, to string
	net      *Network
	inner    cluster.Replica
}

var _ cluster.Replica = (*simReplica)(nil)

func (s *simReplica) GetVersioned(ctx context.Context, key []byte) (cluster.VersionedValue, bool, error) {
	if err := s.net.gate(ctx, s.from, s.to); err != nil {
		return cluster.VersionedValue{}, false, err
	}
	return s.inner.GetVersioned(ctx, key)
}

func (s *simReplica) PutVersioned(ctx context.Context, key []byte, vv cluster.VersionedValue) error {
	if err := s.net.gate(ctx, s.from, s.to); err != nil {
		return err
	}
	return s.inner.PutVersioned(ctx, key, vv)
}

func (s *simReplica) PutHint(ctx context.Context, intended string, key []byte, vv cluster.VersionedValue) error {
	if err := s.net.gate(ctx, s.from, s.to); err != nil {
		return err
	}
	return s.inner.PutHint(ctx, intended, key, vv)
}

func (s *simReplica) MerkleTree(ctx context.Context, scope cluster.RepairScope) (*cluster.MerkleTree, error) {
	if err := s.net.gate(ctx, s.from, s.to); err != nil {
		return nil, err
	}
	return s.inner.MerkleTree(ctx, scope)
}

func (s *simReplica) BucketEntries(ctx context.Context, buckets []int, scope cluster.RepairScope) ([]cluster.KeyVersion, error) {
	if err := s.net.gate(ctx, s.from, s.to); err != nil {
		return nil, err
	}
	return s.inner.BucketEntries(ctx, buckets, scope)
}
