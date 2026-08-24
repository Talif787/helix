package cluster

import (
	"fmt"
	"testing"
)

func buildRing(t *testing.T, vnodes int, ids ...string) *Ring {
	t.Helper()
	r := NewRing(vnodes)
	for _, id := range ids {
		r.Add(id)
	}
	return r
}

func TestRingLookupEmpty(t *testing.T) {
	r := NewRing(16)
	if _, ok := r.Lookup([]byte("k")); ok {
		t.Fatal("empty ring should not resolve a key")
	}
	if got := r.LookupN([]byte("k"), 3); got != nil {
		t.Fatalf("empty ring LookupN should be nil, got %v", got)
	}
}

func TestRingDeterministicRegardlessOfAddOrder(t *testing.T) {
	r1 := buildRing(t, 64, "a", "b", "c")
	r2 := buildRing(t, 64, "c", "b", "a")
	for i := 0; i < 1000; i++ {
		k := []byte(fmt.Sprintf("key-%d", i))
		o1, _ := r1.Lookup(k)
		o2, _ := r2.Lookup(k)
		if o1 != o2 {
			t.Fatalf("ring not deterministic for %s: %s vs %s", k, o1, o2)
		}
	}
}

func TestRingDistributionSpread(t *testing.T) {
	r := buildRing(t, 256, "a", "b", "c")
	const n = 30000
	dist := map[string]int{}
	for i := 0; i < n; i++ {
		o, _ := r.Lookup([]byte(fmt.Sprintf("key-%d", i)))
		dist[o]++
	}
	if len(dist) != 3 {
		t.Fatalf("expected keys on all 3 nodes, got %d", len(dist))
	}
	// With 256 virtual nodes and a well-mixed hash, each of the three nodes should hold
	// close to a third of the keys. Allow a generous 25 percent band around the ideal
	// share: wide enough to stay robust, tight enough to catch a genuinely skewed ring
	// (an unmixed hash lands well outside this band).
	expected := n / len(dist)
	lo, hi := expected*3/4, expected*5/4
	for id, c := range dist {
		if c < lo || c > hi {
			t.Fatalf("node %s share %d outside balance band [%d, %d]", id, c, lo, hi)
		}
	}
}

func TestRingRemoveOnlyMovesRemovedNodesKeys(t *testing.T) {
	r := buildRing(t, 128, "a", "b", "c")
	const n = 5000
	before := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("key-%d", i)
		o, _ := r.Lookup([]byte(k))
		before[k] = o
	}
	r.Remove("c")
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("key-%d", i)
		after, _ := r.Lookup([]byte(k))
		if before[k] != "c" {
			if after != before[k] {
				t.Fatalf("key %s moved from %s to %s though its owner was not removed", k, before[k], after)
			}
		} else if after == "c" {
			t.Fatalf("key %s still owned by removed node c", k)
		}
	}
}

func TestRingAddOnlyStealsToNewNode(t *testing.T) {
	r := buildRing(t, 128, "a", "b", "c")
	const n = 5000
	before := make(map[string]string, n)
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("key-%d", i)
		o, _ := r.Lookup([]byte(k))
		before[k] = o
	}
	r.Add("d")
	moved := 0
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("key-%d", i)
		after, _ := r.Lookup([]byte(k))
		if after != before[k] {
			moved++
			if after != "d" {
				t.Fatalf("key %s moved to %s, but adding d should only move keys onto d", k, after)
			}
		}
	}
	if moved == 0 {
		t.Fatal("adding a node should move at least some keys")
	}
}

func TestRingLookupNDistinctAndOrdered(t *testing.T) {
	r := buildRing(t, 64, "a", "b", "c")
	for i := 0; i < 500; i++ {
		k := []byte(fmt.Sprintf("key-%d", i))
		got := r.LookupN(k, 3)
		if len(got) != 3 {
			t.Fatalf("LookupN(3) returned %d nodes for %s: %v", len(got), k, got)
		}
		seen := map[string]bool{}
		for _, id := range got {
			if seen[id] {
				t.Fatalf("LookupN returned duplicate node %s: %v", id, got)
			}
			seen[id] = true
		}
		if p, _ := r.Lookup(k); got[0] != p {
			t.Fatalf("LookupN primary %s != Lookup %s", got[0], p)
		}
	}
	if got := r.LookupN([]byte("x"), 10); len(got) != 3 {
		t.Fatalf("LookupN beyond node count should return all 3, got %d", len(got))
	}
}
