package rpc

import (
	"context"
	"net"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
)

// startRepairNode opens an engine, wraps it as a node with its preference function wired to
// the shared ring, and serves the data plane (including the Merkle RPCs) over gRPC.
func startRepairNode(t *testing.T, id string, ring *cluster.Ring, n int) (*cluster.LocalNode, string, func()) {
	t.Helper()
	eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	node := cluster.NewLocalNode(id, eng)
	node.SetPreferenceFunc(ring.LookupN)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen %s: %v", id, err)
	}
	srv := NewGRPCServer(node)
	go func() { _ = srv.Serve(lis) }()

	cleanup := func() { srv.GracefulStop(); _ = node.Close() }
	return node, lis.Addr().String(), cleanup
}

// TestGRPCAntiEntropyRepairsOverNetwork drives a Merkle-tree repair between two nodes purely
// over gRPC: one holds a key the other is missing, and after Repair both hold it. This
// exercises the Merkle and BucketEntries RPCs and the RepairScope crossing the wire.
func TestGRPCAntiEntropyRepairsOverNetwork(t *testing.T) {
	ids := []string{"node-a", "node-b", "node-c"}
	n := 3 // every node co-replicates every key, so the scope covers all keys
	ring := cluster.NewRing(128)
	for _, id := range ids {
		ring.Add(id)
	}

	peers := NewPeerRegistry()
	locals := map[string]*cluster.LocalNode{}
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
	for _, id := range ids {
		node, addr, cleanup := startRepairNode(t, id, ring, n)
		cleanups = append(cleanups, cleanup)
		locals[id] = node
		peers.Set(id, addr)
	}

	tr := NewGRPCTransport(peers)
	defer tr.Close()
	ctx := context.Background()

	// Write a key to node-a directly (locally), so node-b and node-c are missing it and no
	// hint exists. This is the gap only anti-entropy closes.
	key := []byte("orphan")
	if err := locals["node-a"].PutVersioned(ctx, key, cluster.VersionedValue{
		Value: []byte("v"), Clock: cluster.VectorClock{"p": 1}, Timestamp: 1,
	}); err != nil {
		t.Fatalf("seed node-a: %v", err)
	}

	// Repair node-a against node-b entirely over gRPC (both are remote via the transport).
	ra, _ := tr.Replica("node-a")
	rb, _ := tr.Replica("node-b")
	reconciled, err := cluster.Repair(ctx, ra, rb, cluster.RepairScope{NodeA: "node-a", NodeB: "node-b", N: n})
	if err != nil {
		t.Fatalf("repair over gRPC: %v", err)
	}
	if reconciled == 0 {
		t.Fatal("expected the repair to reconcile the missing key")
	}

	// node-b should now hold the key, healed over the network.
	if vv, found, _ := locals["node-b"].GetVersioned(ctx, key); !found || string(vv.Value) != "v" {
		t.Fatalf("node-b should have been healed over gRPC, found=%v value=%q", found, vv.Value)
	}

	// A second repair of the same pair should find nothing to do.
	again, err := cluster.Repair(ctx, ra, rb, cluster.RepairScope{NodeA: "node-a", NodeB: "node-b", N: n})
	if err != nil {
		t.Fatalf("second repair: %v", err)
	}
	if again != 0 {
		t.Fatalf("a converged pair should reconcile nothing, got %d", again)
	}
}
