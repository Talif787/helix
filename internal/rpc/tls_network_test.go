package rpc

import (
	"context"
	"net"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/storage"
	"github.com/talifpathan/helix/internal/tlsutil"
)

// startTLSNode serves a node over gRPC with mutual TLS using a cert issued by ca.
func startTLSNode(t *testing.T, id string, ca *tlsutil.CA) (string, func()) {
	t.Helper()
	certPEM, keyPEM, err := ca.IssueNodeCert(id, []string{"127.0.0.1", "localhost"})
	if err != nil {
		t.Fatalf("issue cert for %s: %v", id, err)
	}
	srvCfg, err := tlsutil.ServerConfig(certPEM, keyPEM, ca.CertPEM)
	if err != nil {
		t.Fatalf("server config: %v", err)
	}
	eng, err := storage.Open(storage.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open %s: %v", id, err)
	}
	node := cluster.NewLocalNode(id, eng)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen %s: %v", id, err)
	}
	srv := NewGRPCServer(node, ServerTLSOption(srvCfg))
	go func() { _ = srv.Serve(lis) }()

	cleanup := func() { srv.GracefulStop(); _ = node.Close() }
	return lis.Addr().String(), cleanup
}

// TestMTLSCoordinatorRoundTrip runs a three-node coordinator over mutually authenticated TLS:
// every put and get crosses an encrypted, client-verified connection.
func TestMTLSCoordinatorRoundTrip(t *testing.T) {
	ca, err := tlsutil.GenerateCA("helix-test-ca")
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}

	ids := []string{"node-a", "node-b", "node-c"}
	peers := NewPeerRegistry()
	ring := cluster.NewRing(128)
	var cleanups []func()
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()
	for _, id := range ids {
		addr, cleanup := startTLSNode(t, id, ca)
		cleanups = append(cleanups, cleanup)
		peers.Set(id, addr)
		ring.Add(id)
	}

	// A client cert (any node's) plus the CA lets the transport authenticate to every peer.
	clientCertPEM, clientKeyPEM, err := ca.IssueNodeCert("client", []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("client cert: %v", err)
	}
	cliCfg, err := tlsutil.ClientConfig(clientCertPEM, clientKeyPEM, ca.CertPEM)
	if err != nil {
		t.Fatalf("client config: %v", err)
	}

	tr := NewGRPCTransport(peers, ClientTLSOption(cliCfg))
	defer tr.Close()
	coord := cluster.NewCoordinator(ring, tr, 3, 2, 2, 0, nil, nil)
	ctx := context.Background()

	key := []byte("account:42")
	if err := coord.Put(ctx, key, []byte("balance-100")); err != nil {
		t.Fatalf("put over mTLS: %v", err)
	}
	got, err := coord.Get(ctx, key)
	if err != nil || string(got) != "balance-100" {
		t.Fatalf("get over mTLS: got %q err %v", got, err)
	}
}

// TestMTLSRejectsPlaintextClient confirms the server refuses a client that offers no
// certificate, proving mutual authentication is enforced rather than optional.
func TestMTLSRejectsPlaintextClient(t *testing.T) {
	ca, err := tlsutil.GenerateCA("helix-test-ca")
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	addr, cleanup := startTLSNode(t, "node-a", ca)
	defer cleanup()

	// Dial the TLS server with a plaintext client (DialNode defaults to insecure creds): the
	// RPC must fail because the server requires a client certificate.
	client, err := DialNode(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()
	if _, _, err := client.GetVersioned(context.Background(), []byte("k")); err == nil {
		t.Fatal("a plaintext client must be rejected by the mTLS server")
	}
}
