package simulation

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
)

// Config describes a simulated cluster. IDs are the node names; N/R/W are the replication
// factor and read/write quorums; BaseDir is where each node's engine is stored (a subdirectory
// per node); Seed drives the fault RNG; Faults sets the drop and latency model.
type Config struct {
	IDs      []string
	N, R, W  int
	VNodes   int
	MaxHints int
	BaseDir  string
	Seed     int64
	Faults   Faults
	// RequestTimeout, if set, bounds each replica RPC in the coordinator so one hung replica
	// cannot consume the caller's whole deadline. Zero leaves the coordinator's default (off).
	RequestTimeout time.Duration
}

func (c *Config) withDefaults() {
	if c.VNodes == 0 {
		c.VNodes = 64
	}
	if c.N == 0 {
		c.N = 3
	}
	if c.R == 0 {
		c.R = 2
	}
	if c.W == 0 {
		c.W = 2
	}
	if c.MaxHints == 0 {
		c.MaxHints = c.N
	}
}

// Cluster is a running simulated cluster: one storage engine and one coordinator per node, all
// sharing a controllable Network. Drive it with Put/Get/Delete, inject faults through Net, and
// close it with Close.
type Cluster struct {
	Net *Network

	ids     []string
	engines map[string]*storage.Engine
	coords  map[string]*cluster.Coordinator
	clock   int64

	// The fields below support crash/restart and anti-entropy (the recovery phase). nodes is the
	// same registry every SimTransport resolves through, so updating it here changes what every
	// coordinator can reach; ring, baseDir, and n are what Restart needs to reopen a node's engine
	// and rebuild its Replica, and what AntiEntropy and CheckConvergence need to scope a repair.
	nodes   map[string]cluster.Replica
	ring    *cluster.Ring
	baseDir string
	n       int
}

// NewCluster builds and returns a simulated cluster. Each node gets an engine under
// BaseDir/<id>; all nodes are wired to each other through a shared fault-injecting network.
func NewCluster(cfg Config) (*Cluster, error) {
	cfg.withDefaults()
	if len(cfg.IDs) == 0 {
		return nil, fmt.Errorf("simulation: at least one node id is required")
	}

	net := newNetwork(newFaults(cfg.Faults, cfg.Seed))

	ring := cluster.NewRing(cfg.VNodes)
	for _, id := range cfg.IDs {
		ring.Add(id)
	}

	engines := make(map[string]*storage.Engine, len(cfg.IDs))
	nodes := make(map[string]cluster.Replica, len(cfg.IDs))
	for _, id := range cfg.IDs {
		eng, err := storage.Open(storage.Options{DataDir: filepath.Join(cfg.BaseDir, id)})
		if err != nil {
			for _, e := range engines {
				_ = e.Close()
			}
			return nil, fmt.Errorf("simulation: open engine for %s: %w", id, err)
		}
		engines[id] = eng
		ln := cluster.NewLocalNode(id, eng)
		ln.SetPreferenceFunc(ring.LookupN)
		nodes[id] = ln
	}

	c := &Cluster{
		Net:     net,
		ids:     append([]string(nil), cfg.IDs...),
		engines: engines,
		coords:  make(map[string]*cluster.Coordinator, len(cfg.IDs)),
		nodes:   nodes,
		ring:    ring,
		baseDir: cfg.BaseDir,
		n:       cfg.N,
	}
	for _, id := range cfg.IDs {
		tr := &SimTransport{self: id, net: net, nodes: nodes}
		co := cluster.NewCoordinator(ring, tr, cfg.N, cfg.R, cfg.W, cfg.MaxHints, c.now, nil)
		co.SetRequestTimeout(cfg.RequestTimeout)
		c.coords[id] = co
	}
	return c, nil
}

// now is the cluster's deterministic clock: a monotonic counter, so version timestamps do not
// depend on wall-clock time.
func (c *Cluster) now() int64 { return atomic.AddInt64(&c.clock, 1) }

// IDs returns the node ids in the cluster.
func (c *Cluster) IDs() []string { return append([]string(nil), c.ids...) }

// Put issues a coordinated write through the named node.
func (c *Cluster) Put(ctx context.Context, via string, key, value []byte) error {
	co, ok := c.coords[via]
	if !ok {
		return fmt.Errorf("simulation: unknown node %q", via)
	}
	return co.Put(ctx, key, value)
}

// Get issues a coordinated read through the named node.
func (c *Cluster) Get(ctx context.Context, via string, key []byte) ([]byte, error) {
	co, ok := c.coords[via]
	if !ok {
		return nil, fmt.Errorf("simulation: unknown node %q", via)
	}
	return co.Get(ctx, key)
}

// Delete issues a coordinated delete through the named node.
func (c *Cluster) Delete(ctx context.Context, via string, key []byte) error {
	co, ok := c.coords[via]
	if !ok {
		return fmt.Errorf("simulation: unknown node %q", via)
	}
	return co.Delete(ctx, key)
}

// Close closes every node's engine.
func (c *Cluster) Close() error {
	var firstErr error
	for _, e := range c.engines {
		if err := e.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
