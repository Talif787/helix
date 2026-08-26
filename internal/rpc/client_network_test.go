package rpc

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
)

// startClientPlane builds a coordinator over three in-process replicas and serves its client
// plane over gRPC on localhost. It returns the serving address and a cleanup func.
func startClientPlane(t *testing.T) (string, func()) {
	t.Helper()
	tr := cluster.NewInProcessTransport()
	ring := cluster.NewRing(128)
	var engines []*storage.Engine
	for _, id := range []string{"node-a", "node-b", "node-c"} {
		eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
		if err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
		engines = append(engines, eng)
		tr.Register(id, cluster.NewLocalNode(id, eng))
		ring.Add(id)
	}
	coord := cluster.NewCoordinator(ring, tr, 3, 2, 2, 0, nil, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	RegisterClientService(srv, coord)
	go func() { _ = srv.Serve(lis) }()

	cleanup := func() {
		srv.GracefulStop()
		for _, e := range engines {
			_ = e.Close()
		}
	}
	return lis.Addr().String(), cleanup
}

// TestClientServiceRoundTrip drives the full client path: a program uses the client library to
// Put, Get, overwrite, Delete, and read a missing key, all through the coordinated ClientService
// over gRPC.
func TestClientServiceRoundTrip(t *testing.T) {
	addr, cleanup := startClientPlane(t)
	defer cleanup()

	client, err := DialClient(addr)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer client.Close()
	ctx := context.Background()
	key := []byte("user:1001")

	if err := client.Put(ctx, key, []byte("alice")); err != nil {
		t.Fatalf("put: %v", err)
	}
	v, found, err := client.Get(ctx, key)
	if err != nil || !found || string(v) != "alice" {
		t.Fatalf("get: value=%q found=%v err=%v", v, found, err)
	}

	if err := client.Put(ctx, key, []byte("alice-v2")); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	v, found, err = client.Get(ctx, key)
	if err != nil || !found || string(v) != "alice-v2" {
		t.Fatalf("get after overwrite: value=%q found=%v err=%v", v, found, err)
	}

	if err := client.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, err := client.Get(ctx, key); err != nil || found {
		t.Fatalf("get after delete: found should be false, got found=%v err=%v", found, err)
	}

	if _, found, err := client.Get(ctx, []byte("absent")); err != nil || found {
		t.Fatalf("missing key: found should be false with no error, got found=%v err=%v", found, err)
	}
}
