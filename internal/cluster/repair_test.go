package cluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func kv(key, val string, clock VectorClock, ts int64) KeyVersion {
	return KeyVersion{Key: []byte(key), Value: VersionedValue{Value: []byte(val), Clock: clock, Timestamp: ts}}
}

func TestMerkleIdenticalDataMatches(t *testing.T) {
	entries := []KeyVersion{
		kv("a", "1", VectorClock{"p": 1}, 10),
		kv("b", "2", VectorClock{"p": 1}, 11),
		kv("c", "3", VectorClock{"p": 2}, 12),
	}
	// Same entries in a different order must produce the same root.
	shuffled := []KeyVersion{entries[2], entries[0], entries[1]}
	ta, err := BuildMerkleTree(entries)
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	tb, err := BuildMerkleTree(shuffled)
	if err != nil {
		t.Fatalf("build b: %v", err)
	}
	if ta.Root() != tb.Root() {
		t.Fatal("identical data should produce equal roots regardless of order")
	}
	if d := ta.Diff(tb); len(d) != 0 {
		t.Fatalf("identical trees should not differ, got buckets %v", d)
	}
}

func TestMerkleDiffFindsChangedBuckets(t *testing.T) {
	base := []KeyVersion{
		kv("a", "1", VectorClock{"p": 1}, 10),
		kv("b", "2", VectorClock{"p": 1}, 11),
		kv("c", "3", VectorClock{"p": 1}, 12),
	}
	// b2 is missing "a" and has a newer "c".
	b2 := []KeyVersion{
		kv("b", "2", VectorClock{"p": 1}, 11),
		kv("c", "3b", VectorClock{"p": 2}, 20),
	}
	ta, _ := BuildMerkleTree(base)
	tb, _ := BuildMerkleTree(b2)

	diff := ta.Diff(tb)
	if len(diff) == 0 {
		t.Fatal("expected differing buckets")
	}
	want := map[int]bool{bucketOf([]byte("a")): true, bucketOf([]byte("c")): true}
	got := map[int]bool{}
	for _, d := range diff {
		got[d] = true
	}
	for b := range want {
		if !got[b] {
			t.Fatalf("expected bucket %d in diff %v", b, diff)
		}
	}
	// The unchanged key "b" must not force its bucket into the diff (unless it collides).
	if got[bucketOf([]byte("b"))] && bucketOf([]byte("b")) != bucketOf([]byte("a")) && bucketOf([]byte("b")) != bucketOf([]byte("c")) {
		t.Fatalf("bucket for unchanged key b should not differ: %v", diff)
	}
}

// A key that only one replica holds, but both are supposed to replicate, is copied to the
// other by anti-entropy. Uses N equal to the node count so every node co-replicates every
// key and the co-replication filter accepts all keys.
func TestClusterAntiEntropyHealsMissingKey(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()
	key := []byte("orphan")

	// Simulate a write that reached only one replica: write directly to a single node,
	// bypassing the coordinator, so the other two never learned it and no hint exists.
	target := c.PreferenceList(key, 3)[0]
	tn, _ := c.Node(target)
	if err := tn.PutVersioned(ctx, key, VersionedValue{Value: []byte("v"), Clock: VectorClock{"p": 1}, Timestamp: 1}); err != nil {
		t.Fatalf("seed one replica: %v", err)
	}
	// Precondition: exactly one node has it.
	holders := 0
	for _, id := range c.Nodes() {
		n, _ := c.Node(id)
		if _, found, _ := n.GetVersioned(ctx, key); found {
			holders++
		}
	}
	if holders != 1 {
		t.Fatalf("precondition: expected 1 holder, got %d", holders)
	}

	reconciled, err := c.AntiEntropy(ctx)
	if err != nil {
		t.Fatalf("anti-entropy: %v", err)
	}
	if reconciled == 0 {
		t.Fatal("expected anti-entropy to reconcile at least one key")
	}

	// Now all three replicas hold it.
	for _, id := range c.Nodes() {
		n, _ := c.Node(id)
		vv, found, _ := n.GetVersioned(ctx, key)
		if !found || string(vv.Value) != "v" {
			t.Fatalf("node %s should hold the healed key, found=%v value=%q", id, found, vv.Value)
		}
	}
}

// After anti-entropy converges a cluster, a second round finds nothing to do (empty diff),
// confirming the trees match and no needless data is exchanged.
func TestClusterAntiEntropyIsIdempotent(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("k%03d", i)), []byte("v")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	if _, err := c.AntiEntropy(ctx); err != nil {
		t.Fatalf("first round: %v", err)
	}
	reconciled, err := c.AntiEntropy(ctx)
	if err != nil {
		t.Fatalf("second round: %v", err)
	}
	if reconciled != 0 {
		t.Fatalf("a converged cluster should reconcile nothing on a second round, got %d", reconciled)
	}
}

// Anti-entropy heals a replica that missed writes while it was offline and was never read
// or hinted, which is the gap hinted handoff does not cover.
func TestClusterAntiEntropyHealsOfflineReplicaWithoutHints(t *testing.T) {
	// MaxHints -1 disables hinting, so a write during the outage is simply missed by the
	// down replica with no hint to heal it later.
	c, err := NewCluster([]string{"node-a", "node-b", "node-c"}, Options{
		BaseDir:  t.TempDir(),
		N:        3,
		R:        1,
		W:        2,
		MaxHints: -1,
		Storage:  storage.Options{SyncWrites: false},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	key := []byte("k")

	down := c.PreferenceList(key, 3)[2]
	c.Transport().Deregister(down)
	if err := c.Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("write with one replica down (W=2): %v", err)
	}
	c.Transport().Register(down, mustNode(t, c, down))

	// No hint exists to heal it (hinting disabled), and the key is never read.
	if _, found, _ := mustNode(t, c, down).GetVersioned(ctx, key); found {
		t.Fatal("down replica should not have the value yet")
	}

	if _, err := c.AntiEntropy(ctx); err != nil {
		t.Fatalf("anti-entropy: %v", err)
	}
	if vv, found, _ := mustNode(t, c, down).GetVersioned(ctx, key); !found || string(vv.Value) != "v" {
		t.Fatalf("anti-entropy should have healed the offline replica, found=%v value=%q", found, vv.Value)
	}
}

func mustNode(t *testing.T, c *Cluster, id string) *LocalNode {
	t.Helper()
	n, ok := c.Node(id)
	if !ok {
		t.Fatalf("node %s not found", id)
	}
	return n
}
