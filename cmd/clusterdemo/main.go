// Command clusterdemo brings up a three-node in-process Helix cluster with replication and
// tunable quorums, then verifies the Phase 4 behavior end to end: each key is replicated
// to its preference list, writes and reads succeed with a node offline (quorum), and a
// read repairs a stale replica after it rejoins. It prints a short report and exits
// non-zero on any failed check, so it doubles as a smoke test.
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
	const N, R, W = 3, 2, 2

	c, err := cluster.NewCluster(nodeIDs, cluster.Options{
		BaseDir: dir,
		VNodes:  cluster.DefaultVNodes,
		N:       N, R: R, W: W,
		Storage: storage.Options{SyncWrites: false},
		Logger:  logger,
	})
	if err != nil {
		return err
	}
	defer c.Close()

	fmt.Printf("cluster of %d nodes, replication N=%d R=%d W=%d\n", len(nodeIDs), N, R, W)

	const n = 2000
	key := func(i int) []byte { return []byte(fmt.Sprintf("key:%05d", i)) }
	val := func(i int) string { return fmt.Sprintf("val:%05d", i) }

	for i := 0; i < n; i++ {
		if err := c.Put(ctx, key(i), []byte(val(i))); err != nil {
			return fmt.Errorf("put %s: %w", key(i), err)
		}
	}
	for i := 0; i < n; i++ {
		got, err := c.Get(ctx, key(i))
		if err != nil {
			return fmt.Errorf("get %s: %w", key(i), err)
		}
		if string(got) != val(i) {
			return fmt.Errorf("get %s: want %s got %s", key(i), val(i), got)
		}
	}
	fmt.Printf("wrote and read back %d keys with quorum\n", n)

	// Every key should physically live on each replica in its preference list.
	replicaLoad := map[string]int{}
	for i := 0; i < n; i++ {
		k := key(i)
		for _, id := range c.PreferenceList(k, N) {
			node, _ := c.Node(id)
			_, found, err := node.GetVersioned(ctx, k)
			if err != nil {
				return fmt.Errorf("getversioned %s on %s: %w", k, id, err)
			}
			if !found {
				return fmt.Errorf("replica %s missing %s", id, k)
			}
			replicaLoad[id]++
		}
	}
	fmt.Printf("replica placement verified (%d copies total for %d keys)\n", n*N, n)
	for _, id := range sortedKeys(replicaLoad) {
		fmt.Printf("  %-8s holds %5d keys\n", id, replicaLoad[id])
	}

	// Fault tolerance: take one preference node offline, keep serving with quorum.
	fkey := []byte("failover-key")
	if err := c.Put(ctx, fkey, []byte("before")); err != nil {
		return fmt.Errorf("seed failover key: %w", err)
	}
	down := c.PreferenceList(fkey, N)[N-1]
	c.Transport().Deregister(down)
	fmt.Printf("took %s offline\n", down)

	if err := c.Put(ctx, fkey, []byte("after")); err != nil {
		return fmt.Errorf("write with %s down should meet W=%d: %w", down, W, err)
	}
	if got, err := c.Get(ctx, fkey); err != nil || string(got) != "after" {
		return fmt.Errorf("read with %s down: got %q err %v", down, got, err)
	}
	fmt.Printf("served read and write with %s offline (W=%d, R=%d)\n", down, W, R)

	// Rejoin and let a read repair the stale replica.
	node, _ := c.Node(down)
	c.Transport().Register(down, node)
	if _, err := c.Get(ctx, fkey); err != nil {
		return fmt.Errorf("read after rejoin: %w", err)
	}
	if vv, found, _ := node.GetVersioned(ctx, fkey); !found || string(vv.Value) != "after" {
		return fmt.Errorf("read repair should have updated %s to \"after\", found=%v value=%q", down, found, vv.Value)
	}
	fmt.Printf("%s rejoined and was read-repaired to the latest value\n", down)

	// Delete replicates too.
	if err := c.Delete(ctx, fkey); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	if _, err := c.Get(ctx, fkey); !errors.Is(err, storage.ErrNotFound) {
		return fmt.Errorf("expected deleted key to be absent, err=%v", err)
	}
	fmt.Println("delete replicated and observed on read")

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
