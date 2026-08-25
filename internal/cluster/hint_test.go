package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func TestHintStoreReconcilesAndDrains(t *testing.T) {
	h := newHintStore()
	h.add("nodeX", []byte("k"), VersionedValue{Value: []byte("v1"), Clock: VectorClock{"p": 1}})
	// A causally later hint for the same key replaces the earlier one.
	h.add("nodeX", []byte("k"), VersionedValue{Value: []byte("v2"), Clock: VectorClock{"p": 2}})
	h.add("nodeX", []byte("k2"), VersionedValue{Value: []byte("x"), Clock: VectorClock{"p": 1}})
	h.add("nodeY", []byte("k3"), VersionedValue{Value: []byte("y"), Clock: VectorClock{"p": 1}})

	if got := h.count(); got != 3 {
		t.Fatalf("expected 3 hints, got %d", got)
	}
	if ids := h.intendedNodes(); len(ids) != 2 || ids[0] != "nodeX" || ids[1] != "nodeY" {
		t.Fatalf("intended nodes wrong: %v", ids)
	}
	pending := h.pendingFor("nodeX")
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending for nodeX, got %d", len(pending))
	}
	for _, kv := range pending {
		if string(kv.Key) == "k" && string(kv.Value.Value) != "v2" {
			t.Fatalf("hint for k should have reconciled to v2, got %q", kv.Value.Value)
		}
	}
	h.remove("nodeX", [][]byte{[]byte("k"), []byte("k2")})
	if got := h.count(); got != 1 {
		t.Fatalf("expected 1 hint after removing nodeX's, got %d", got)
	}
	if ids := h.intendedNodes(); len(ids) != 1 || ids[0] != "nodeY" {
		t.Fatalf("nodeX should be gone, ids=%v", ids)
	}
}

// With a preferred replica down, the coordinator stores a hint on a fallback and the write
// still meets the quorum; when the replica returns, delivering hints heals it.
func TestClusterHintedHandoffHealsRecoveredNode(t *testing.T) {
	// Five nodes so there is a fallback beyond the 3-node preference list.
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c", "node-d", "node-e")
	ctx := context.Background()
	key := []byte("account:42")

	pref := c.PreferenceList(key, 3)
	down := pref[2]
	c.Transport().Deregister(down) // model the intended replica being offline

	if err := c.Put(ctx, key, []byte("balance-100")); err != nil {
		t.Fatalf("write with one preferred replica down should still meet quorum: %v", err)
	}
	// A hint should now be buffered somewhere for the down node.
	if c.PendingHints() == 0 {
		t.Fatal("expected a hint to be buffered for the down replica")
	}
	// The down node itself holds nothing yet.
	downNode, _ := c.Node(down)
	if _, found, _ := downNode.GetVersioned(ctx, key); found {
		t.Fatal("down node should not have the value while it was offline")
	}

	// It recovers; delivering hints replays the buffered write to it.
	c.Transport().Register(down, downNode)
	if delivered := c.DeliverHints(ctx); delivered == 0 {
		t.Fatal("expected at least one hint to be delivered")
	}
	if vv, found, _ := downNode.GetVersioned(ctx, key); !found || string(vv.Value) != "balance-100" {
		t.Fatalf("recovered node should have received the hinted write, found=%v value=%q", found, vv.Value)
	}
	if c.PendingHints() != 0 {
		t.Fatalf("delivered hints should be cleared, %d remain", c.PendingHints())
	}
}

// A hinted write counts toward the write quorum, so a write succeeds even when two of three
// preferred replicas are down, as long as fallbacks can hold the hints.
func TestClusterSloppyQuorumWithHints(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c", "node-d", "node-e")
	ctx := context.Background()
	key := []byte("k")

	pref := c.PreferenceList(key, 3)
	c.Transport().Deregister(pref[1])
	c.Transport().Deregister(pref[2])

	// Only one preferred replica is up, but two fallbacks can take hints, so W=2 is met.
	if err := c.Put(ctx, key, []byte("v")); err != nil {
		t.Fatalf("sloppy quorum with hints should satisfy W=2: %v", err)
	}
	if c.PendingHints() == 0 {
		t.Fatal("expected hints buffered for the two down replicas")
	}
}

// With no fallbacks available, a write that cannot meet the quorum fails cleanly.
func TestClusterWriteFailsWhenNoFallbacks(t *testing.T) {
	// Exactly three nodes: preference list is all of them, no fallback exists.
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()
	key := []byte("k")
	pref := c.PreferenceList(key, 3)
	c.Transport().Deregister(pref[1])
	c.Transport().Deregister(pref[2])

	if err := c.Put(ctx, key, []byte("v")); !errors.Is(err, ErrWriteQuorum) {
		t.Fatalf("want ErrWriteQuorum with only one reachable node and no fallbacks, got %v", err)
	}
}

func TestCoordinatorHintsFailedReplicaToFallback(t *testing.T) {
	// Build a 4-node ring so there is one fallback past a 3-node preference list.
	ids := []string{"n1", "n2", "n3", "n4"}
	ring := NewRing(32)
	tr := NewInProcessTransport()
	reps := make(map[string]*memReplica, len(ids))
	for _, id := range ids {
		ring.Add(id)
		mr := newMemReplica()
		reps[id] = mr
		tr.Register(id, mr)
	}
	var tick int64
	now := func() int64 { tick++; return tick }
	co := NewCoordinator(ring, tr, 3, 2, 2, 1, now, nil) // N=3, W=2, one fallback

	key := []byte("k")
	pref := ring.LookupN(key, 3)
	fallback := ring.LookupN(key, 4)[3]
	reps[pref[2]].failPut.Store(true) // one preferred replica rejects writes

	if err := co.Put(context.Background(), key, []byte("v")); err != nil {
		t.Fatalf("put should succeed via hint: %v", err)
	}
	if reps[fallback].hintCount() == 0 {
		t.Fatalf("fallback %s should hold a hint for %s", fallback, pref[2])
	}
}

func TestClusterHintlessConfigStillWorks(t *testing.T) {
	// MaxHints negative disables hinting; a normal write to a healthy cluster still works.
	c, err := NewCluster([]string{"node-a", "node-b", "node-c"}, Options{
		BaseDir:  t.TempDir(),
		N:        3,
		R:        2,
		W:        2,
		MaxHints: -1,
		Storage:  storage.Options{SyncWrites: false},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()
	if err := c.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("get: got %q err %v", got, err)
	}
	_ = fmt.Sprint(c.PendingHints())
}
