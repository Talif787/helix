package simulation

import (
	"context"
	"errors"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
)

// TestReproducibleFaults shows that a given seed produces the same sequence of drop decisions
// when drawn sequentially, so a fault schedule can be replayed.
func TestReproducibleFaults(t *testing.T) {
	draw := func(seed int64) []bool {
		f := newFaults(Faults{DropProbability: 0.5}, seed)
		out := make([]bool, 50)
		for i := range out {
			out[i] = f.shouldDrop()
		}
		return out
	}
	a := draw(42)
	b := draw(42)
	c := draw(43)

	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("same seed produced different draws at %d", i)
		}
	}
	same := true
	for i := range a {
		if a[i] != c[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different seeds produced identical draws (RNG not seeded per run)")
	}
}

// TestClusterReplicates is a sanity check: with no faults, a write through one node is readable
// through another.
func TestClusterReplicates(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"},
		N:   3, R: 2, W: 2,
		BaseDir: t.TempDir(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	if err := c.Put(ctx, "n0", []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := c.Get(ctx, "n2", []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("read from another node: got %q err %v", got, err)
	}
}

// TestPartitionQuorum is the headline: with a 3-node cluster and R=W=2, isolating one node
// leaves the majority side (two nodes) fully available and the minority side (one node) unable
// to reach quorum, so it can neither write nor read.
func TestPartitionQuorum(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"},
		N:   3, R: 2, W: 2,
		BaseDir: t.TempDir(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	// Isolate n2 from n0 and n1.
	c.Net.Partition("n2")

	// Majority side can still commit a write (n0 and n1 ack, meeting W=2).
	if err := c.Put(ctx, "n0", []byte("key"), []byte("majority")); err != nil {
		t.Fatalf("write on majority side should succeed: %v", err)
	}
	// Majority side can read it back (R=2 from the two reachable nodes).
	if got, err := c.Get(ctx, "n1", []byte("key")); err != nil || string(got) != "majority" {
		t.Fatalf("read on majority side: got %q err %v", got, err)
	}

	// Minority side (n2 alone) cannot reach a write quorum of 2.
	if err := c.Put(ctx, "n2", []byte("key2"), []byte("minority")); err == nil {
		t.Fatal("write on the isolated minority node should fail (cannot reach W=2)")
	}
	// Minority side cannot reach a read quorum of 2 either.
	if _, err := c.Get(ctx, "n2", []byte("key")); err == nil {
		t.Fatal("read on the isolated minority node should fail (cannot reach R=2)")
	}
}

// TestHealRestoresAvailability shows that after the partition heals, the formerly isolated node
// can serve again and, reading at quorum, sees the value written during the split.
func TestHealRestoresAvailability(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"},
		N:   3, R: 2, W: 2,
		BaseDir: t.TempDir(),
		Seed:    1,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	c.Net.Partition("n2")
	if err := c.Put(ctx, "n0", []byte("k"), []byte("during-split")); err != nil {
		t.Fatalf("majority write: %v", err)
	}
	c.Net.Heal()

	// n2 can now reach the others, so a quorum read returns the value it missed during the split.
	got, err := c.Get(ctx, "n2", []byte("k"))
	if err != nil || string(got) != "during-split" {
		t.Fatalf("after heal, read via n2: got %q err %v", got, err)
	}
}

// TestUnknownNodeAndErrType guards the small contract details the coordinator relies on.
func TestUnknownNodeAndErrType(t *testing.T) {
	c, err := NewCluster(Config{IDs: []string{"n0"}, N: 1, R: 1, W: 1, BaseDir: t.TempDir(), Seed: 1})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	if err := c.Put(context.Background(), "ghost", []byte("k"), []byte("v")); err == nil {
		t.Fatal("expected an error writing through an unknown node")
	}

	// A gated cross-partition call surfaces ErrNodeUnavailable.
	net := newNetwork(newFaults(Faults{}, 1))
	net.Partition("a")
	if err := net.gate(context.Background(), "a", "b"); !errors.Is(err, cluster.ErrNodeUnavailable) {
		t.Fatalf("cross-partition gate: want ErrNodeUnavailable, got %v", err)
	}
	if err := net.gate(context.Background(), "a", "a"); err != nil {
		t.Fatalf("self gate should always pass: %v", err)
	}
}
