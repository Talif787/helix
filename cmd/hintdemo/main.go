// Command hintdemo shows hinted handoff on a five-node in-process cluster: with a preferred
// replica offline, a write still succeeds by parking a hint on a fallback node, and once
// the replica recovers the hint is replayed so it holds the data. It prints a short report
// and exits non-zero on any failed check, so it doubles as a smoke test.
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
		fmt.Fprintln(os.Stderr, "hintdemo: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("hintdemo: PASS")
}

func run() error {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "helix-hintdemo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	logger := observability.NewLogger("warn", "text")
	nodeIDs := []string{"node-a", "node-b", "node-c", "node-d", "node-e"}

	c, err := cluster.NewCluster(nodeIDs, cluster.Options{
		BaseDir: dir,
		VNodes:  cluster.DefaultVNodes,
		N:       3, R: 2, W: 2, MaxHints: 2,
		Storage: storage.Options{SyncWrites: false},
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	key := []byte("account:42")
	pref := c.PreferenceList(key, 3)
	fmt.Printf("cluster of %d nodes, N=3 R=2 W=2; %q is replicated to %v\n", len(nodeIDs), key, pref)

	down := pref[2]
	c.Transport().Deregister(down)
	fmt.Printf("took preferred replica %s offline\n", down)

	if err := c.Put(ctx, key, []byte("balance-100")); err != nil {
		return fmt.Errorf("write with %s down should still meet W=2: %w", down, err)
	}
	fmt.Printf("write succeeded via sloppy quorum; %d hint(s) buffered on fallback nodes\n", c.PendingHints())
	if c.PendingHints() == 0 {
		return fmt.Errorf("expected a hint to be buffered for %s", down)
	}

	downNode, _ := c.Node(down)
	if _, found, _ := downNode.GetVersioned(ctx, key); found {
		return fmt.Errorf("%s should not hold the value while offline", down)
	}
	fmt.Printf("%s holds nothing yet, as expected while offline\n", down)

	c.Transport().Register(down, downNode)
	fmt.Printf("%s recovered; delivering hints\n", down)
	delivered := c.DeliverHints(ctx)
	fmt.Printf("delivered %d hint(s)\n", delivered)

	vv, found, err := downNode.GetVersioned(ctx, key)
	if err != nil {
		return fmt.Errorf("read recovered node: %w", err)
	}
	if !found || string(vv.Value) != "balance-100" {
		return fmt.Errorf("%s should have received the hinted write, found=%v value=%q", down, found, vv.Value)
	}
	fmt.Printf("hinted handoff complete: %s now holds the write it missed\n", down)

	if c.PendingHints() != 0 {
		return fmt.Errorf("expected all hints cleared after delivery, %d remain", c.PendingHints())
	}
	return nil
}
