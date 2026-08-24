// Command clusterdemo brings up a three-node in-process Helix cluster, seeds it, and
// verifies partitioning end to end: keys spread across nodes, reads route back to the
// right owner, data lives only on its owner, routing is deterministic, and deletes take
// effect. It prints a short report and exits non-zero on any failed check, so it doubles
// as a smoke test.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/observability"
	"github.com/talifpathan/helix/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "clusterdemo: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("clusterdemo: PASS")
}

func run() error {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "helix-clusterdemo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	logger := observability.NewLogger("warn", "text") // quiet; the demo prints its own report
	nodeIDs := []string{"node-a", "node-b", "node-c"}

	c, err := cluster.NewCluster(nodeIDs, cluster.Options{
		BaseDir: dir,
		VNodes:  cluster.DefaultVNodes,
		Storage: storage.Options{SyncWrites: false},
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	const n = 3000
	key := func(i int) []byte { return []byte(fmt.Sprintf("key:%05d", i)) }
	val := func(i int) string { return fmt.Sprintf("val:%05d", i) }

	for i := 0; i < n; i++ {
		if err := c.Put(ctx, key(i), []byte(val(i))); err != nil {
			return fmt.Errorf("put %s: %w", key(i), err)
		}
	}

	// Distribution across nodes.
	dist := map[string]int{}
	for i := 0; i < n; i++ {
		owner, ok := c.OwnerOf(key(i))
		if !ok {
			return fmt.Errorf("no owner for %s", key(i))
		}
		dist[owner]++
	}
	fmt.Printf("seeded %d keys across %d nodes\n", n, len(nodeIDs))
	for _, id := range sortedKeys(dist) {
		fmt.Printf("  %-8s %5d keys (%.1f%%)\n", id, dist[id], float64(dist[id])*100/float64(n))
	}
	if len(dist) != len(nodeIDs) {
		return fmt.Errorf("expected keys on all %d nodes, saw %d", len(nodeIDs), len(dist))
	}
	for _, id := range nodeIDs {
		if dist[id] == 0 {
			return fmt.Errorf("node %s received no keys", id)
		}
	}

	// Read-back through the coordinator.
	for i := 0; i < n; i++ {
		got, err := c.Get(ctx, key(i))
		if err != nil {
			return fmt.Errorf("get %s: %w", key(i), err)
		}
		if string(got) != val(i) {
			return fmt.Errorf("get %s: want %s got %s", key(i), val(i), got)
		}
	}
	fmt.Printf("verified read-back of all %d keys through the coordinator\n", n)

	// Data locality and deterministic routing on a few samples.
	samples := []int{0, 1500, 2999}
	for _, i := range samples {
		k := key(i)
		owner, _ := c.OwnerOf(k)
		if o2, _ := c.OwnerOf(k); o2 != owner {
			return fmt.Errorf("nondeterministic owner for %s: %s vs %s", k, owner, o2)
		}
		for _, id := range nodeIDs {
			node, _ := c.Node(id)
			_, gerr := node.Get(ctx, k)
			if id == owner {
				if gerr != nil {
					return fmt.Errorf("owner %s missing %s: %w", id, k, gerr)
				}
			} else if !errors.Is(gerr, storage.ErrNotFound) {
				return fmt.Errorf("non-owner %s should not hold %s (err=%v)", id, k, gerr)
			}
		}
		fmt.Printf("  %s owned by %s and absent on the other nodes\n", k, owner)
	}

	// Deletes take effect; neighbors remain.
	del := []int{0, 1, 2}
	for _, i := range del {
		if err := c.Delete(ctx, key(i)); err != nil {
			return fmt.Errorf("delete %s: %w", key(i), err)
		}
	}
	for _, i := range del {
		if _, err := c.Get(ctx, key(i)); !errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("expected %s deleted, err=%v", key(i), err)
		}
	}
	if _, err := c.Get(ctx, key(3)); err != nil {
		return fmt.Errorf("neighbor %s should remain: %w", key(3), err)
	}
	fmt.Printf("deleted %d keys and confirmed a neighbor survived\n", len(del))

	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
