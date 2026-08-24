package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/talifpathan/helix/internal/storage"
)

// Options configures a cluster of in-process nodes.
type Options struct {
	// BaseDir is the parent directory for per-node data; node id "n" lives in BaseDir/n.
	BaseDir string
	// VNodes is the number of virtual nodes per physical node. Below 1 uses DefaultVNodes.
	VNodes int
	// Storage is applied to every node as a template; DataDir and Logger are overridden
	// per node so each engine gets its own directory and an annotated logger.
	Storage storage.Options
	// Logger receives cluster and per-node logs. Nil uses slog.Default().
	Logger *slog.Logger
}

// Cluster owns a set of in-process nodes, the ring that partitions the keyspace across
// them, and the coordinator that routes operations. It is the Phase 3 stand-in for a set
// of networked processes: the Store and Transport seams it is built on are the ones the
// network layer will implement, so callers of Get, Put, and Delete do not change.
type Cluster struct {
	ring  *Ring
	tr    *InProcessTransport
	coord *Coordinator
	nodes map[string]*LocalNode
	log   *slog.Logger
}

// NewCluster opens one storage engine per node id and wires them onto a shared ring. On
// any failure it closes the engines it already opened and returns the error.
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

	c.coord = NewCoordinator(c.ring, c.tr)
	c.log.Info("cluster started", "nodes", len(nodeIDs), "vnodes", vnodes)
	return c, nil
}

func (c *Cluster) closeOpened() {
	for _, n := range c.nodes {
		_ = n.Close()
	}
}

// Get routes a read through the coordinator to the key's owner.
func (c *Cluster) Get(ctx context.Context, key []byte) ([]byte, error) {
	return c.coord.Get(ctx, key)
}

// Put routes a write through the coordinator to the key's owner.
func (c *Cluster) Put(ctx context.Context, key, value []byte) error {
	return c.coord.Put(ctx, key, value)
}

// Delete routes a delete through the coordinator to the key's owner.
func (c *Cluster) Delete(ctx context.Context, key []byte) error {
	return c.coord.Delete(ctx, key)
}

// OwnerOf returns the node that currently owns key.
func (c *Cluster) OwnerOf(key []byte) (string, bool) {
	return c.ring.Lookup(key)
}

// PreferenceList returns up to n distinct nodes for key, primary first. Replication in a
// later phase writes to this list; it is exposed now so routing and placement share one
// source of truth.
func (c *Cluster) PreferenceList(key []byte, n int) []string {
	return c.ring.LookupN(key, n)
}

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
