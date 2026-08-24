package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func newTestCluster(t *testing.T, n, r, w int, ids ...string) *Cluster {
	t.Helper()
	c, err := NewCluster(ids, Options{
		BaseDir: t.TempDir(),
		VNodes:  64,
		N:       n,
		R:       r,
		W:       w,
		Storage: storage.Options{SyncWrites: false},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClusterPutGetDelete(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()

	const n = 400
	for i := 0; i < n; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("k%04d", i)), []byte(fmt.Sprintf("v%04d", i))); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		got, err := c.Get(ctx, []byte(fmt.Sprintf("k%04d", i)))
		if err != nil || string(got) != fmt.Sprintf("v%04d", i) {
			t.Fatalf("get k%04d: got %q err %v", i, got, err)
		}
	}
	for i := 0; i < n; i += 2 {
		if err := c.Delete(ctx, []byte(fmt.Sprintf("k%04d", i))); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		_, err := c.Get(ctx, []byte(fmt.Sprintf("k%04d", i)))
		if i%2 == 0 {
			if !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("k%04d should be deleted, err=%v", i, err)
			}
		} else if err != nil {
			t.Fatalf("k%04d should be present, err=%v", i, err)
		}
	}
}

// With N=2 on a 3-node cluster, each key should be replicated onto exactly its two
// preference-list nodes and be absent on the third.
func TestClusterReplicationPlacement(t *testing.T) {
	c := newTestCluster(t, 2, 1, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()

	for i := 0; i < 200; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("k%04d", i)), []byte("x")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	for i := 0; i < 200; i++ {
		key := []byte(fmt.Sprintf("k%04d", i))
		pref := map[string]bool{}
		for _, id := range c.PreferenceList(key, 2) {
			pref[id] = true
		}
		if len(pref) != 2 {
			t.Fatalf("expected 2 preference nodes, got %d", len(pref))
		}
		for _, id := range c.Nodes() {
			node, _ := c.Node(id)
			_, found, err := node.GetVersioned(ctx, key)
			if err != nil {
				t.Fatalf("getversioned on %s: %v", id, err)
			}
			if pref[id] && !found {
				t.Fatalf("replica %s should hold %s", id, key)
			}
			if !pref[id] && found {
				t.Fatalf("non-replica %s should not hold %s", id, key)
			}
		}
	}
}

// A write meets W with one node offline, and after the node rejoins a read repairs it.
func TestClusterToleratesNodeDownThenRepairs(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	ctx := context.Background()
	key := []byte("resilient")

	if err := c.Put(ctx, key, []byte("v1")); err != nil {
		t.Fatalf("initial put: %v", err)
	}

	// Take one preference node offline, then write again; W=2 is still reachable.
	down := c.PreferenceList(key, 3)[2]
	c.Transport().Deregister(down)
	if err := c.Put(ctx, key, []byte("v2")); err != nil {
		t.Fatalf("put with one node down should meet W=2: %v", err)
	}
	if got, err := c.Get(ctx, key); err != nil || string(got) != "v2" {
		t.Fatalf("get with one node down: got %q err %v", got, err)
	}

	// Bring it back; the down node is stale. A read reconciles to v2 and repairs it.
	node, _ := c.Node(down)
	c.Transport().Register(down, node)
	if got, err := c.Get(ctx, key); err != nil || string(got) != "v2" {
		t.Fatalf("get after rejoin: got %q err %v", got, err)
	}
	if vv, found, _ := node.GetVersioned(ctx, key); !found || string(vv.Value) != "v2" {
		t.Fatalf("rejoined node should be repaired to v2, found=%v value=%q", found, vv.Value)
	}
}

func TestClusterMissingKey(t *testing.T) {
	c := newTestCluster(t, 3, 2, 2, "node-a", "node-b", "node-c")
	if _, err := c.Get(context.Background(), []byte("absent")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClusterRejectsBadQuorum(t *testing.T) {
	_, err := NewCluster([]string{"a", "b"}, Options{
		BaseDir: t.TempDir(),
		N:       2,
		R:       3, // R > N
		W:       1,
		Storage: storage.Options{SyncWrites: false},
	})
	if err == nil {
		t.Fatal("expected an error when R exceeds N")
	}
}

func TestClusterRejectsDuplicateNodeID(t *testing.T) {
	_, err := NewCluster([]string{"a", "a"}, Options{
		BaseDir: t.TempDir(),
		Storage: storage.Options{SyncWrites: false},
	})
	if err == nil {
		t.Fatal("expected an error for a duplicate node id")
	}
}

func TestClusterRejectsEmptyMembership(t *testing.T) {
	if _, err := NewCluster(nil, Options{BaseDir: t.TempDir()}); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("want ErrNoNodes, got %v", err)
	}
}
