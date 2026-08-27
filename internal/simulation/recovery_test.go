package simulation

import (
	"context"
	"testing"
)

// TestConvergenceAfterPartitionHeal drives writes only through the majority while one node is
// isolated (so that node misses them), heals the partition, and shows that the divergence is real
// before repair and that a single anti-entropy round closes every gap.
func TestConvergenceAfterPartitionHeal(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 1,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	keys := []string{"a", "b", "c", "d", "e"}

	// Isolate n2, then drive the workload only through the majority side so n2 misses the writes.
	c.Net.Partition("n2")
	h := RunWorkload(ctx, c, WorkloadConfig{
		Keys: keys, WritesPerKey: 10, Readers: 3, Seed: 1,
		WriterNodes: []string{"n0", "n1"}, ReaderNodes: []string{"n0", "n1"},
	})
	for _, e := range h.Events() {
		if !e.OK {
			t.Fatalf("majority-side op failed under a minority partition: %+v", e)
		}
	}

	// Heal, then confirm the divergence is real before repair: n2 has not seen the writes.
	c.Net.Heal()
	if vs, err := c.CheckConvergence(ctx, keys); err != nil {
		t.Fatalf("convergence check: %v", err)
	} else if len(vs) == 0 {
		t.Fatal("expected divergence after heal but before anti-entropy: n2 should still be behind")
	}

	// One anti-entropy round should reconcile the isolated node with the majority.
	if _, err := c.AntiEntropy(ctx); err != nil {
		t.Fatalf("anti-entropy: %v", err)
	}
	if vs, err := c.CheckConvergence(ctx, keys); err != nil {
		t.Fatalf("convergence check: %v", err)
	} else if len(vs) > 0 {
		t.Fatalf("replicas did not converge after anti-entropy: %v", vs)
	}
}

// TestCrashRestartRecoversAndConverges takes a node fully down (its engine closed), keeps writing
// through the majority so it misses updates, restarts it (its durable state replays from the
// write-ahead log), and shows anti-entropy then brings it fully current, with a coordinated read
// through the restarted node returning the latest value.
func TestCrashRestartRecoversAndConverges(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 7,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	keys := []string{"k0", "k1", "k2", "k3"}

	// Baseline: every node, including n2, holds an initial value.
	for _, k := range keys {
		if err := c.Put(ctx, "n0", []byte(k), []byte(k+"-base")); err != nil {
			t.Fatalf("baseline put %s: %v", k, err)
		}
	}

	// Crash n2, then keep writing through the majority so n2 misses the updates.
	if err := c.Crash("n2"); err != nil {
		t.Fatalf("crash n2: %v", err)
	}
	h := RunWorkload(ctx, c, WorkloadConfig{
		Keys: keys, WritesPerKey: 8, Readers: 2, Seed: 7,
		WriterNodes: []string{"n0", "n1"}, ReaderNodes: []string{"n0", "n1"},
	})
	for _, e := range h.Events() {
		if !e.OK {
			t.Fatalf("majority op failed while n2 was down: %+v", e)
		}
	}

	// Restart n2: its engine reopens and the baseline it held before the crash replays from the
	// write-ahead log. It has not yet seen the writes made while it was down.
	if err := c.Restart("n2"); err != nil {
		t.Fatalf("restart n2: %v", err)
	}

	// Anti-entropy closes the gap, and every replica agrees.
	if _, err := c.AntiEntropy(ctx); err != nil {
		t.Fatalf("anti-entropy: %v", err)
	}
	if vs, err := c.CheckConvergence(ctx, keys); err != nil {
		t.Fatalf("convergence check: %v", err)
	} else if len(vs) > 0 {
		t.Fatalf("replicas did not converge after crash, restart, and anti-entropy: %v", vs)
	}

	// A coordinated read through the restarted node returns the latest write.
	got, err := c.Get(ctx, "n2", []byte("k0"))
	if err != nil {
		t.Fatalf("read via restarted n2: %v", err)
	}
	if string(got) != "k0#8" {
		t.Fatalf("restarted node read: got %q, want the last write k0#8", got)
	}
}

// TestCrashRestartPreservesDurableData is a durability sanity check: a value written before a
// crash survives the close and reopen of the node's engine, with no repair involved.
func TestCrashRestartPreservesDurableData(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 5,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	if err := c.Put(ctx, "n0", []byte("durable"), []byte("v1")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := c.Crash("n2"); err != nil {
		t.Fatalf("crash n2: %v", err)
	}
	if err := c.Restart("n2"); err != nil {
		t.Fatalf("restart n2: %v", err)
	}

	// n2's own copy survived the close and reopen via the write-ahead log, with no repair needed.
	vv, found, err := c.nodes["n2"].GetVersioned(ctx, []byte("durable"))
	if err != nil {
		t.Fatalf("local read on restarted n2: %v", err)
	}
	if !found || string(vv.Value) != "v1" {
		t.Fatalf("restarted n2 lost durable data: found=%v value=%q", found, vv.Value)
	}
}

// TestConvergenceCatchesDivergentReplicas is the anti-vacuous guard: with a genuine divergence in
// place and no repair run, the convergence check must report a violation. If it passed here it
// would be meaningless in the tests above.
func TestConvergenceCatchesDivergentReplicas(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 3,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	// Write through the majority while n2 is isolated, then heal but do not run anti-entropy.
	c.Net.Partition("n2")
	if err := c.Put(ctx, "n0", []byte("x"), []byte("only-majority")); err != nil {
		t.Fatalf("majority put: %v", err)
	}
	c.Net.Heal()

	vs, err := c.CheckConvergence(ctx, []string{"x"})
	if err != nil {
		t.Fatalf("convergence check: %v", err)
	}
	if len(vs) == 0 {
		t.Fatal("expected a convergence violation: n2 never saw the write and no repair ran")
	}
}

// TestAntiEntropyIdempotent confirms a second anti-entropy round over an already-converged
// cluster reconciles nothing, so repair converges and then stays quiet.
func TestAntiEntropyIdempotent(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 9,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()
	ctx := context.Background()

	keys := []string{"p", "q", "r"}
	c.Net.Partition("n2")
	RunWorkload(ctx, c, WorkloadConfig{
		Keys: keys, WritesPerKey: 6, Readers: 2, Seed: 9,
		WriterNodes: []string{"n0", "n1"}, ReaderNodes: []string{"n0", "n1"},
	})
	c.Net.Heal()

	if _, err := c.AntiEntropy(ctx); err != nil {
		t.Fatalf("first anti-entropy: %v", err)
	}
	second, err := c.AntiEntropy(ctx)
	if err != nil {
		t.Fatalf("second anti-entropy: %v", err)
	}
	if second != 0 {
		t.Fatalf("second anti-entropy reconciled %d keys; a converged cluster should reconcile 0", second)
	}
	if vs, err := c.CheckConvergence(ctx, keys); err != nil {
		t.Fatalf("convergence check: %v", err)
	} else if len(vs) > 0 {
		t.Fatalf("cluster diverged after a supposedly idempotent repair: %v", vs)
	}
}
