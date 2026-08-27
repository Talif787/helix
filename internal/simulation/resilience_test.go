package simulation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/cluster"
)

// TestWriteAvailableWithOneHungReplica reproduces the Kubernetes finding: one preferred replica
// is present in the ring and reachable but never answers (a pod whose DNS resolves before it is
// serving). With a per-attempt timeout, the hung replica fails fast, the two healthy nodes meet
// the write quorum, and the write succeeds and is readable through another node. This is the
// core availability fix.
func TestWriteAvailableWithOneHungReplica(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 1,
		RequestTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	c.Net.Hang("n2") // reachable but never answers

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.Put(ctx, "n0", []byte("k"), []byte("v")); err != nil {
		t.Fatalf("write with a healthy quorum should succeed despite one hung replica: %v", err)
	}
	got, err := c.Get(ctx, "n1", []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("read back through a healthy node: got %q err %v", got, err)
	}
}

// TestReadAvailableWithOneHungReplica shows the read path returns as soon as a read quorum of
// healthy nodes answers, without blocking on the hung replica.
func TestReadAvailableWithOneHungReplica(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 1,
		RequestTimeout: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	ctx := context.Background()
	if err := c.Put(ctx, "n0", []byte("k"), []byte("v")); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	c.Net.Hang("n2")

	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := c.Get(rctx, "n0", []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("read should return on the quorum of healthy nodes: got %q err %v", got, err)
	}
}

// TestWriteFailsFastWhenQuorumHung is the non-vacuous proof that the per-attempt timeout does its
// job: with two of three replicas hung, no write quorum is reachable, so the write must fail. The
// point is that it fails FAST (bounded by the per-attempt timeout) rather than hanging until the
// caller's deadline. Without the timeout the hung replicas would block until the 5s deadline; the
// generous 2s bound below would be violated. The error must be a write-quorum error.
func TestWriteFailsFastWhenQuorumHung(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2, MaxHints: -1,
		BaseDir: t.TempDir(), Seed: 1,
		RequestTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	c.Net.Hang("n1", "n2") // only n0 (the coordinator's self) can ack: below W=2

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	err = c.Put(ctx, "n0", []byte("k"), []byte("v"))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a write-quorum failure with two of three replicas hung")
	}
	if !errors.Is(err, cluster.ErrWriteQuorum) {
		t.Fatalf("expected ErrWriteQuorum, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("write should fail fast via the per-attempt timeout, took %v (unbounded wait)", elapsed)
	}
}
