// Command repairdemo shows Merkle-tree anti-entropy on a three-node in-process cluster. It
// creates divergence that hinted handoff cannot fix (a write missed by a replica that was
// offline, never read, and not hinted), then runs an anti-entropy round and confirms the
// replica is healed and the trees converge. It prints a short report and exits non-zero on
// any failed check, so it doubles as a smoke test.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/observability"
	"github.com/talifpathan/helix/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "repairdemo: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("repairdemo: PASS")
}

func run() error {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "helix-repairdemo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	logger := observability.NewLogger("warn", "text")
	nodeIDs := []string{"node-a", "node-b", "node-c"}

	// Hinting disabled so the outage leaves a genuine gap only anti-entropy can close.
	c, err := cluster.NewCluster(nodeIDs, cluster.Options{
		BaseDir: dir,
		VNodes:  cluster.DefaultVNodes,
		N:       3, R: 1, W: 2, MaxHints: -1,
		Storage: storage.Options{SyncWrites: false},
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	// Seed some baseline data that all replicas share.
	for i := 0; i < 20; i++ {
		if err := c.Put(ctx, []byte(fmt.Sprintf("seed:%02d", i)), []byte("base")); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
	}
	fmt.Printf("cluster of %d nodes, N=3 W=2, hinting disabled; seeded 20 shared keys\n", len(nodeIDs))

	key := []byte("account:42")
	down := c.PreferenceList(key, 3)[2]
	c.Transport().Deregister(down)
	fmt.Printf("took replica %s offline (no hints will be stored)\n", down)

	if err := c.Put(ctx, key, []byte("balance-100")); err != nil {
		return fmt.Errorf("write during outage should meet W=2: %w", err)
	}
	fmt.Printf("wrote %q while %s was offline; buffered hints: %d\n", key, down, c.PendingHints())

	downNode, _ := c.Node(down)
	c.Transport().Register(down, downNode)
	if _, found, _ := downNode.GetVersioned(ctx, key); found {
		return fmt.Errorf("%s should still be missing the write after rejoining", down)
	}
	fmt.Printf("%s rejoined but is missing the write, and no hint exists to heal it\n", down)

	reconciled, err := c.AntiEntropy(ctx)
	if err != nil {
		return fmt.Errorf("anti-entropy: %w", err)
	}
	fmt.Printf("ran anti-entropy: reconciled %d key(s)\n", reconciled)

	vv, found, err := downNode.GetVersioned(ctx, key)
	if err != nil {
		return fmt.Errorf("read healed node: %w", err)
	}
	if !found || string(vv.Value) != "balance-100" {
		return fmt.Errorf("%s should have been healed by anti-entropy, found=%v value=%q", down, found, vv.Value)
	}
	fmt.Printf("anti-entropy healed %s: it now holds the write it missed\n", down)

	// A second round should find nothing to do.
	again, err := c.AntiEntropy(ctx)
	if err != nil {
		return fmt.Errorf("second anti-entropy: %w", err)
	}
	if again != 0 {
		return fmt.Errorf("converged cluster should reconcile nothing on a second round, got %d", again)
	}
	fmt.Println("second round reconciled 0 keys: replicas are in sync")
	return nil
}
