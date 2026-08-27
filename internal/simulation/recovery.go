package simulation

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
)

// This file adds the recovery half of the simulation phase: taking a node down and bringing it
// back, driving Merkle anti-entropy across the cluster, and checking the eventual-consistency
// invariant that once the cluster is healed and quiesced every replica of a key agrees on its
// value. Where Part 2 proves safety under fault (no fabrication, read-after-write freshness),
// this proves liveness after fault (the repair path actually closes the gaps a partition or a
// crash leaves behind).
//
// Quiescence: Crash, Restart, AntiEntropy, and CheckConvergence must be called while no workload
// is in flight. The node registry is read locklessly by every SimTransport on the coordinators'
// request goroutines, so mutating it or reasoning about a "settled" state concurrently with live
// traffic would race and would not be meaningful. The recovery scenarios crash, restart, repair,
// and check between workload bursts, when the cluster is idle. RunWorkload is synchronous, so a
// call after it returns is safely quiescent.

// Crash takes a node offline: it removes the node from the shared registry, so every coordinator
// that tries to reach it now sees ErrNodeUnavailable exactly as if the process had died, then
// closes the node's engine. The node's data directory is left intact so Restart can recover it.
// Call it only while the cluster is quiesced.
func (c *Cluster) Crash(id string) error {
	eng, ok := c.engines[id]
	if !ok {
		return fmt.Errorf("simulation: unknown node %q", id)
	}
	if _, live := c.nodes[id]; !live {
		return fmt.Errorf("simulation: node %q is already down", id)
	}
	delete(c.nodes, id)
	if err := eng.Close(); err != nil {
		return fmt.Errorf("simulation: close engine for %s: %w", id, err)
	}
	return nil
}

// Restart brings a crashed node back: it reopens the node's engine from the same data directory,
// so the write-ahead log replays and the durable state the node held before the crash returns,
// rebuilds the node's Replica with the ring-backed preference function that repair scoping needs,
// and puts it back in the shared registry. The node's existing coordinator resolves through that
// registry, so it serves again immediately. Writes the node missed while it was down are closed
// by a later AntiEntropy round. Call it only while the cluster is quiesced.
func (c *Cluster) Restart(id string) error {
	if _, known := c.coords[id]; !known {
		return fmt.Errorf("simulation: unknown node %q", id)
	}
	if _, live := c.nodes[id]; live {
		return fmt.Errorf("simulation: node %q is not down", id)
	}
	eng, err := storage.Open(storage.Options{DataDir: filepath.Join(c.baseDir, id)})
	if err != nil {
		return fmt.Errorf("simulation: reopen engine for %s: %w", id, err)
	}
	ln := cluster.NewLocalNode(id, eng)
	ln.SetPreferenceFunc(c.ring.LookupN)
	c.engines[id] = eng
	c.nodes[id] = ln
	return nil
}

// AntiEntropy runs one round of Merkle anti-entropy across every pair of live nodes, each pair
// scoped to the keys both replicate, and returns the total number of keys reconciled. Each pair
// is repaired from one side's point of view: that side reads its own tree locally and reaches the
// other side across the simulated network, so a pair split by an active partition (or with a side
// still down) simply reconciles nothing and is skipped. Run it after a heal or a restart to close
// the gaps those events leave. Call it only while the cluster is quiesced.
func (c *Cluster) AntiEntropy(ctx context.Context) (int, error) {
	ids := c.liveIDs()
	total := 0
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			reconciled, err := c.repairPair(ctx, ids[i], ids[j])
			if err != nil {
				if errors.Is(err, cluster.ErrNodeUnavailable) {
					continue // the pair is split or a side is down: nothing to repair this round
				}
				return total, err
			}
			total += reconciled
		}
	}
	return total, nil
}

// repairPair runs anti-entropy between nodeA and nodeB, scoped to the keys they both replicate.
// nodeA repairs from its own local view and reaches nodeB through a SimTransport, so the call
// crosses the simulated network and an active partition between them surfaces as
// ErrNodeUnavailable.
func (c *Cluster) repairPair(ctx context.Context, nodeA, nodeB string) (int, error) {
	local, ok := c.nodes[nodeA]
	if !ok {
		return 0, cluster.ErrNodeUnavailable
	}
	tr := &SimTransport{self: nodeA, net: c.Net, nodes: c.nodes}
	remote, ok := tr.Replica(nodeB)
	if !ok {
		return 0, cluster.ErrNodeUnavailable
	}
	return cluster.Repair(ctx, local, remote, cluster.RepairScope{NodeA: nodeA, NodeB: nodeB, N: c.n})
}

// liveIDs returns the ids of nodes currently up (registered), in the cluster's stable id order.
func (c *Cluster) liveIDs() []string {
	out := make([]string, 0, len(c.nodes))
	for _, id := range c.ids {
		if _, ok := c.nodes[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// CheckConvergence verifies the eventual-consistency invariant: for each of the given keys, every
// live replica that should hold the key (it is on the key's preference list) holds the same
// version. It reads each node's local state directly, not through a quorum, so it inspects what
// each replica actually stores rather than what a coordinated read would reconcile on the fly. A
// node that is currently down is not expected to agree and is skipped. Call it only while the
// cluster is quiesced, and only after anti-entropy has had a chance to run: a divergence it
// reports before repair is expected, not a bug, which is exactly what the anti-vacuous test
// relies on.
func (c *Cluster) CheckConvergence(ctx context.Context, keys []string) ([]Violation, error) {
	var vs []Violation
	for _, key := range keys {
		preferred := c.ring.LookupN([]byte(key), c.n)
		states := make([]replicaState, 0, len(preferred))
		for _, id := range preferred {
			rep, ok := c.nodes[id]
			if !ok {
				continue // node is down: it cannot be expected to agree until it restarts
			}
			vv, found, err := rep.GetVersioned(ctx, []byte(key))
			if err != nil {
				return vs, fmt.Errorf("simulation: read %s from %s: %w", key, id, err)
			}
			states = append(states, replicaState{id: id, found: found, deleted: vv.Deleted, value: string(vv.Value)})
		}
		if len(states) < 2 {
			continue // fewer than two live replicas: nothing to compare
		}
		ref := states[0]
		agree := true
		for _, s := range states[1:] {
			if s.found != ref.found || s.deleted != ref.deleted || s.value != ref.value {
				agree = false
				break
			}
		}
		if !agree {
			vs = append(vs, Violation{Kind: "convergence", Detail: describeDisagreement(key, states)})
		}
	}
	return vs, nil
}

// replicaState is one live replica's stored view of a key, used by the convergence check.
type replicaState struct {
	id      string
	found   bool
	deleted bool
	value   string
}

// describeDisagreement renders the per-replica state for a key, sorted by node id, for a readable
// violation message.
func describeDisagreement(key string, states []replicaState) string {
	sort.Slice(states, func(i, j int) bool { return states[i].id < states[j].id })
	detail := fmt.Sprintf("replicas of %s disagree:", key)
	for _, s := range states {
		switch {
		case !s.found:
			detail += fmt.Sprintf(" %s=absent", s.id)
		case s.deleted:
			detail += fmt.Sprintf(" %s=tombstone", s.id)
		default:
			detail += fmt.Sprintf(" %s=%q", s.id, s.value)
		}
	}
	return detail
}
