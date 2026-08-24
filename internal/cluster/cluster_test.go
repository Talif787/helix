package cluster

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

func newTestCluster(t *testing.T, ids ...string) *Cluster {
	t.Helper()
	c, err := NewCluster(ids, Options{
		BaseDir: t.TempDir(),
		VNodes:  64,
		Storage: storage.Options{SyncWrites: false},
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestClusterPutGetDelete(t *testing.T) {
	c := newTestCluster(t, "node-a", "node-b", "node-c")
	ctx := context.Background()

	const n = 500
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%04d", i))
		v := []byte(fmt.Sprintf("v%04d", i))
		if err := c.Put(ctx, k, v); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%04d", i))
		want := fmt.Sprintf("v%04d", i)
		got, err := c.Get(ctx, k)
		if err != nil {
			t.Fatalf("get %s: %v", k, err)
		}
		if string(got) != want {
			t.Fatalf("get %s: want %s got %s", k, want, got)
		}
	}

	for i := 0; i < n; i += 2 {
		if err := c.Delete(ctx, []byte(fmt.Sprintf("k%04d", i))); err != nil {
			t.Fatalf("delete: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%04d", i))
		_, err := c.Get(ctx, k)
		if i%2 == 0 {
			if !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("expected %s deleted, err=%v", k, err)
			}
		} else if err != nil {
			t.Fatalf("expected %s present, err=%v", k, err)
		}
	}
}

func TestClusterDataLocality(t *testing.T) {
	c := newTestCluster(t, "node-a", "node-b", "node-c")
	ctx := context.Background()

	const n = 300
	for i := 0; i < n; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("k%04d", i)), []byte("x")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	for i := 0; i < n; i++ {
		k := []byte(fmt.Sprintf("k%04d", i))
		owner, ok := c.OwnerOf(k)
		if !ok {
			t.Fatalf("no owner for %s", k)
		}
		for _, id := range c.Nodes() {
			node, _ := c.Node(id)
			_, err := node.Get(ctx, k)
			if id == owner {
				if err != nil {
					t.Fatalf("owner %s should hold %s: %v", id, k, err)
				}
			} else if !errors.Is(err, storage.ErrNotFound) {
				t.Fatalf("non-owner %s should not hold %s: err=%v", id, k, err)
			}
		}
	}
}

func TestClusterRoutingIsStable(t *testing.T) {
	c := newTestCluster(t, "node-a", "node-b", "node-c")
	for i := 0; i < 1000; i++ {
		k := []byte(fmt.Sprintf("stable-%d", i))
		o1, ok1 := c.OwnerOf(k)
		o2, ok2 := c.OwnerOf(k)
		if !ok1 || !ok2 || o1 != o2 {
			t.Fatalf("unstable owner for %s: %s/%v vs %s/%v", k, o1, ok1, o2, ok2)
		}
	}
}

func TestClusterMissingKey(t *testing.T) {
	c := newTestCluster(t, "node-a", "node-b")
	if _, err := c.Get(context.Background(), []byte("absent")); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestClusterRejectsDuplicateNodeID(t *testing.T) {
	_, err := NewCluster([]string{"a", "a"}, Options{
		BaseDir: t.TempDir(),
		VNodes:  8,
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
