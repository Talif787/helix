package rpc

import (
	"context"
	"net"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
)

// startNode opens a storage engine, wraps it as a node, and serves it over gRPC on a random
// localhost port. It returns the node id, its address, and a cleanup func.
func startNode(t *testing.T, id string) (string, string, func()) {
	t.Helper()
	eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open engine for %s: %v", id, err)
	}
	node := cluster.NewLocalNode(id, eng)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for %s: %v", id, err)
	}
	srv := NewGRPCServer(node)
	go func() { _ = srv.Serve(lis) }()

	cleanup := func() {
		srv.GracefulStop()
		_ = node.Close()
	}
	return id, lis.Addr().String(), cleanup
}

// TestGRPCCoordinatorRoundTrip runs a real three-node coordinator entirely over gRPC on
// localhost: every replica put and get crosses a socket. It proves the server, client, and
// networked transport implement the same seams the in-process transport did.
func TestGRPCCoordinatorRoundTrip(t *testing.T) {
	ids := []string{"node-a", "node-b", "node-c"}
	peers := NewPeerRegistry()
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	ring := cluster.NewRing(128)
	for _, id := range ids {
		nid, addr, cleanup := startNode(t, id)
		cleanups = append(cleanups, cleanup)
		peers.Set(nid, addr)
		ring.Add(nid)
	}

	tr := NewGRPCTransport(peers)
	defer tr.Close()
	coord := cluster.NewCoordinator(ring, tr, 3, 2, 2, 0, nil, nil)
	ctx := context.Background()

	key := []byte("account:42")

	// Write and read back across the network.
	if err := coord.Put(ctx, key, []byte("balance-100")); err != nil {
		t.Fatalf("put over gRPC: %v", err)
	}
	got, err := coord.Get(ctx, key)
	if err != nil || string(got) != "balance-100" {
		t.Fatalf("get over gRPC: got %q err %v", got, err)
	}

	// Overwrite and confirm reconciliation carried the newer value across the wire.
	if err := coord.Put(ctx, key, []byte("balance-250")); err != nil {
		t.Fatalf("overwrite over gRPC: %v", err)
	}
	got, err = coord.Get(ctx, key)
	if err != nil || string(got) != "balance-250" {
		t.Fatalf("get after overwrite: got %q err %v", got, err)
	}

	// A missing key reports not found across the wire.
	if _, err := coord.Get(ctx, []byte("nope")); err != storage.ErrNotFound {
		t.Fatalf("missing key should be ErrNotFound, got %v", err)
	}

	// Delete replicates as a tombstone and a subsequent read is not found.
	if err := coord.Delete(ctx, key); err != nil {
		t.Fatalf("delete over gRPC: %v", err)
	}
	if _, err := coord.Get(ctx, key); err != storage.ErrNotFound {
		t.Fatalf("deleted key should be ErrNotFound, got %v", err)
	}
}

// TestGRPCTransportUnknownNode confirms the transport reports a miss for an unregistered id
// rather than dialing a bogus address.
func TestGRPCTransportUnknownNode(t *testing.T) {
	tr := NewGRPCTransport(NewPeerRegistry())
	defer tr.Close()
	if _, ok := tr.Replica("ghost"); ok {
		t.Fatal("unregistered node id should not resolve to a replica")
	}
}
