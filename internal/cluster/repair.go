package cluster

import "context"

// RepairScope describes which keys an anti-entropy round covers, in a form that crosses the
// network: the keys co-replicated by NodeA and NodeB at replication factor N. A node turns a
// scope into a concrete key predicate using its own view of the ring, so the same scope sent
// to both nodes selects the same key set on each. Scoping by co-replication is what keeps
// repair from copying a key onto a node outside its preference list.
type RepairScope struct {
	NodeA string
	NodeB string
	N     int
}

// Repair runs one round of anti-entropy between two replicas a and b within scope. It builds
// each side's Merkle tree, finds the leaf buckets that differ, exchanges just those buckets'
// entries, and applies each side's entries to the other. Because PutVersioned reconciles,
// both replicas converge to the same versions for the differing keys. It returns the number
// of keys reconciled.
func Repair(ctx context.Context, a, b Replica, scope RepairScope) (int, error) {
	ta, err := a.MerkleTree(ctx, scope)
	if err != nil {
		return 0, err
	}
	tb, err := b.MerkleTree(ctx, scope)
	if err != nil {
		return 0, err
	}

	diff := ta.Diff(tb)
	if len(diff) == 0 {
		return 0, nil // already in sync, no data exchanged
	}

	ea, err := a.BucketEntries(ctx, diff, scope)
	if err != nil {
		return 0, err
	}
	eb, err := b.BucketEntries(ctx, diff, scope)
	if err != nil {
		return 0, err
	}

	reconciled := 0
	for _, kv := range eb {
		if err := a.PutVersioned(ctx, kv.Key, kv.Value); err != nil {
			return reconciled, err
		}
		reconciled++
	}
	for _, kv := range ea {
		if err := b.PutVersioned(ctx, kv.Key, kv.Value); err != nil {
			return reconciled, err
		}
		reconciled++
	}
	return reconciled, nil
}

// RepairPair runs anti-entropy between the two named nodes, scoped to the keys they both
// replicate. It returns the number of keys reconciled.
func (c *Cluster) RepairPair(ctx context.Context, nodeA, nodeB string) (int, error) {
	ra, ok := c.tr.Replica(nodeA)
	if !ok {
		return 0, ErrNodeUnavailable
	}
	rb, ok := c.tr.Replica(nodeB)
	if !ok {
		return 0, ErrNodeUnavailable
	}
	return Repair(ctx, ra, rb, RepairScope{NodeA: nodeA, NodeB: nodeB, N: c.n})
}

// AntiEntropy runs one repair round across every pair of nodes, each pair scoped to the keys
// both replicate. It returns the total number of keys reconciled. A real deployment would
// repair replica-overlapping pairs on a schedule rather than all pairs at once; this is the
// simple, exhaustive form.
func (c *Cluster) AntiEntropy(ctx context.Context) (int, error) {
	ids := c.Nodes()
	total := 0
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			reconciled, err := c.RepairPair(ctx, ids[i], ids[j])
			if err != nil {
				return total, err
			}
			total += reconciled
		}
	}
	return total, nil
}
