package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/talifpathan/helix/internal/storage"
)

// Default replication settings. N is the replication factor; R and W are the read and
// write quorums. R + W > N gives read-your-writes overlap.
const (
	DefaultN = 3
	DefaultR = 2
	DefaultW = 2
)

// Options configures a cluster of in-process nodes.
type Options struct {
	// BaseDir is the parent directory for per-node data; node id "n" lives in BaseDir/n.
	BaseDir string
	// VNodes is the number of virtual nodes per physical node. Below 1 uses DefaultVNodes.
	VNodes int
	// N, R, W are the replication factor and read and write quorums. Zero uses the
	// defaults. R and W must not exceed N.
	N int
	R int
	W int
	// Storage is applied to every node as a template; DataDir and Logger are overridden
	// per node.
	Storage storage.Options
	// Logger receives cluster and per-node logs. Nil uses slog.Default().
	Logger *slog.Logger
	// Now overrides the wall clock used for last-write-wins timestamps, for tests.
	Now func() int64
}

// Cluster owns a set of in-process nodes, the ring that partitions the keyspace, and the
// coordinator that replicates and reconciles operations across them. It is the stand-in
// for a set of networked processes: the Replica and Transport seams it is built on are the
// ones the network layer will implement, so callers of Get, Put, and Delete do not change.
type Cluster struct {
	ring  *Ring
	tr    *InProcessTransport
	coord *Coordinator
	nodes map[string]*LocalNode
	log   *slog.Logger
}

// NewCluster opens one storage engine per node id, wires them onto a shared ring with the
// given replication settings, and returns the assembled cluster. On any failure it closes
// the engines it already opened.
func NewCluster(nodeIDs []string, opts Options) (*Cluster, error) {
	if len(nodeIDs) == 0 {
		return nil, ErrNoNodes
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	vnodes := opts.VNodes
	if vnodes < 1 {
		vnodes = DefaultVNodes
	}
	n, r, w := opts.N, opts.R, opts.W
	if n < 1 {
		n = DefaultN
	}
	if r < 1 {
		r = DefaultR
	}
	if w < 1 {
		w = DefaultW
	}
	if r > n || w > n {
		return nil, fmt.Errorf("cluster: R (%d) and W (%d) must not exceed N (%d)", r, w, n)
	}

	c := &Cluster{
		ring:  NewRing(vnodes),
		tr:    NewInProcessTransport(),
		nodes: make(map[string]*LocalNode, len(nodeIDs)),
		log:   opts.Logger,
	}

	for _, id := range nodeIDs {
		if _, exists := c.nodes[id]; exists {
			c.closeOpened()
			return nil, fmt.Errorf("cluster: duplicate node id %q", id)
		}
		o := opts.Storage
		o.DataDir = filepath.Join(opts.BaseDir, id)
		o.Logger = opts.Logger.With("node", id)
		eng, err := storage.Open(o)
		if err != nil {
			c.closeOpened()
			return nil, fmt.Errorf("cluster: open node %q: %w", id, err)
		}
		node := NewLocalNode(id, eng)
		c.nodes[id] = node
		c.tr.Register(id, node)
		c.ring.Add(id)
	}

	c.coord = NewCoordinator(c.ring, c.tr, n, r, w, opts.Now, opts.Logger)
	c.log.Info("cluster started", "nodes", len(nodeIDs), "vnodes", vnodes, "n", n, "r", r, "w", w)
	return c, nil
}

func (c *Cluster) closeOpened() {
	for _, n := range c.nodes {
		_ = n.Close()
	}
}

// Get reads key with a read quorum, reconciling and repairing replicas as needed.
func (c *Cluster) Get(ctx context.Context, key []byte) ([]byte, error) {
	return c.coord.Get(ctx, key)
}

// Put replicates a write with a write quorum.
func (c *Cluster) Put(ctx context.Context, key, value []byte) error {
	return c.coord.Put(ctx, key, value)
}

// Delete replicates a tombstone with a write quorum.
func (c *Cluster) Delete(ctx context.Context, key []byte) error {
	return c.coord.Delete(ctx, key)
}

// OwnerOf returns the primary node for key (the first entry of its preference list).
func (c *Cluster) OwnerOf(key []byte) (string, bool) {
	return c.ring.Lookup(key)
}

// PreferenceList returns up to n distinct nodes for key, primary first: the replicas that
// hold it.
func (c *Cluster) PreferenceList(key []byte, n int) []string {
	return c.ring.LookupN(key, n)
}

// Transport exposes the in-process transport so a test or the demo can simulate a node
// going offline and rejoining.
func (c *Cluster) Transport() *InProcessTransport { return c.tr }

// Node returns the in-process node with the given id, for tests and introspection.
func (c *Cluster) Node(id string) (*LocalNode, bool) {
	n, ok := c.nodes[id]
	return n, ok
}

// Nodes returns the node ids in sorted order.
func (c *Cluster) Nodes() []string { return c.ring.Nodes() }

// Close closes every node's storage engine, returning the first error encountered.
func (c *Cluster) Close() error {
	var firstErr error
	for id, n := range c.nodes {
		if err := n.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close node %q: %w", id, err)
		}
	}
	return firstErr
}
