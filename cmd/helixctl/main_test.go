package main

import (
	"bytes"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/rpc"
	"github.com/talifpathan/helix/internal/storage"
)

// startServer stands up a single-node coordinator (N=R=W=1 so writes commit) fronted by
// ClientService over gRPC on localhost.
func startServer(t *testing.T) (string, func()) {
	t.Helper()
	eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	tr := cluster.NewInProcessTransport()
	tr.Register("n1", cluster.NewLocalNode("n1", eng))
	ring := cluster.NewRing(64)
	ring.Add("n1")
	coord := cluster.NewCoordinator(ring, tr, 1, 1, 1, 0, nil, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	rpc.RegisterClientService(srv, coord)
	go func() { _ = srv.Serve(lis) }()

	return lis.Addr().String(), func() { srv.GracefulStop(); _ = eng.Close() }
}

func TestHelixctlPutGetDelete(t *testing.T) {
	addr, cleanup := startServer(t)
	defer cleanup()

	run3 := func(args ...string) string {
		var out bytes.Buffer
		full := append([]string{"-addr", addr}, args...)
		if err := run(full, &out); err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		return strings.TrimSpace(out.String())
	}

	if got := run3("put", "k", "v"); got != "OK" {
		t.Fatalf("put output = %q, want OK", got)
	}
	if got := run3("get", "k"); got != "v" {
		t.Fatalf("get output = %q, want v", got)
	}
	if got := run3("get", "missing"); got != "(not found)" {
		t.Fatalf("get missing output = %q, want (not found)", got)
	}
	if got := run3("delete", "k"); got != "OK" {
		t.Fatalf("delete output = %q, want OK", got)
	}
	if got := run3("get", "k"); got != "(not found)" {
		t.Fatalf("get after delete output = %q, want (not found)", got)
	}
}

func TestHelixctlUsageErrors(t *testing.T) {
	var out bytes.Buffer
	cases := [][]string{
		{},                             // no command
		{"put", "k"},                   // put needs key and value
		{"get"},                        // get needs a key
		{"bogus"},                      // unknown command
		{"-tls-cert", "x", "get", "k"}, // partial TLS flags
	}
	for _, args := range cases {
		out.Reset()
		if err := run(args, &out); err == nil {
			t.Fatalf("run %v should have returned an error", args)
		}
	}
}
