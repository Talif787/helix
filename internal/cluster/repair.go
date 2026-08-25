package cluster

import "context"

// Repair runs one round of anti-entropy between two replicas a and b, reconciling only the
// keys accepted by filter (so a repair scoped to co-replicated keys never copies a key onto
// a node that should not hold it). It builds each side's Merkle tree, finds the leaf buckets
// that differ, exchanges just those buckets' entries, and applies each side's entries to the
// other. Because PutVersioned reconciles, both replicas converge to the same versions for
// the differing keys. It returns the number of keys reconciled.
func Repair(ctx context.Context, a, b Replica, filter KeyFilter) (int, error) {
	ta, err := a.MerkleTree(ctx, filter)
	if err != nil {
		return 0, err
	}
	tb, err := b.MerkleTree(ctx, filter)
	if err != nil {
		return 0, err
	}

	diff := ta.Diff(tb)
	if len(diff) == 0 {
		return 0, nil // already in sync, no data exchanged
	}

	ea, err := a.BucketEntries(ctx, diff, filter)
	if err != nil {
		return 0, err
	}
	eb, err := b.BucketEntries(ctx, diff, filter)
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

// coReplicationFilter returns a filter accepting keys that both nodeA and nodeB replicate,
// according to the ring. Scoping a repair with it keeps anti-entropy from spreading a key to
// a node outside its preference list.
func (c *Cluster) coReplicationFilter(nodeA, nodeB string) KeyFilter {
	n := c.n
	return func(key []byte) bool {
		inA, inB := false, false
		for _, id := range c.ring.LookupN(key, n) {
			if id == nodeA {
				inA = true
			}
			if id == nodeB {
				inB = true
			}
		}
		return inA && inB
	}
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
	return Repair(ctx, ra, rb, c.coReplicationFilter(nodeA, nodeB))
}

// AntiEntropy runs one repair round across every pair of nodes, each pair scoped to the keys
// both replicate. It returns the total number of keys reconciled. A real deployment would
// repair replica-overlapping pairs on a schedule rather than all pairs at once; this is the
// simple, exhaustive form for in-process use.
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
