package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/storage"
	"github.com/talifpathan/helix/internal/tlsutil"
)

// newTestCluster starts a daemon per id on loopback, sharing one peer set. Pre-created
// listeners avoid a bind race: the addresses are known before any daemon starts. If certDir
// is non-empty, each daemon loads <id>.crt/<id>.key and ca.crt from it for mutual TLS.
func newTestCluster(t *testing.T, ids []string, certDir string) (map[string]*Daemon, func()) {
	t.Helper()
	listeners := map[string]net.Listener{}
	peers := map[string]string{}
	for _, id := range ids {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		listeners[id] = l
		peers[id] = l.Addr().String()
	}

	daemons := map[string]*Daemon{}
	cleanup := func() {
		for _, d := range daemons {
			_ = d.Stop()
		}
	}
	for _, id := range ids {
		cfg := Config{
			NodeID:              id,
			BindAddr:            peers[id],
			Listener:            listeners[id],
			Peers:               peers,
			N:                   3,
			R:                   2,
			W:                   2,
			Storage:             storage.Options{DataDir: t.TempDir()},
			SwimInterval:        50 * time.Millisecond,
			AntiEntropyInterval: time.Hour, // do not auto-repair mid-test
			HintInterval:        time.Hour,
		}
		if certDir != "" {
			cfg.TLSCert = filepath.Join(certDir, id+".crt")
			cfg.TLSKey = filepath.Join(certDir, id+".key")
			cfg.TLSCA = filepath.Join(certDir, "ca.crt")
		}
		d, err := New(cfg)
		if err != nil {
			cleanup()
			t.Fatalf("new %s: %v", id, err)
		}
		if err := d.Start(); err != nil {
			cleanup()
			t.Fatalf("start %s: %v", id, err)
		}
		daemons[id] = d
	}
	return daemons, cleanup
}

func writeTestCerts(t *testing.T, ids []string) string {
	t.Helper()
	dir := t.TempDir()
	ca, err := tlsutil.GenerateCA("helix-test-ca")
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), ca.CertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		certPEM, keyPEM, err := ca.IssueNodeCert(id, []string{"127.0.0.1", "localhost", id})
		if err != nil {
			t.Fatalf("issue %s: %v", id, err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".crt"), certPEM, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".key"), keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func allAlive(ds map[string]*Daemon, ids []string) bool {
	for holder, d := range ds {
		for _, target := range ids {
			if target == holder {
				continue // a node does not track itself in its member list
			}
			if st, ok := d.Members().StateOf(target); !ok || st != membership.Alive {
				return false
			}
		}
	}
	return true
}

// TestDaemonClusterReplicates writes to one node and reads it back from another, proving the
// coordinator, transport, and gRPC serving are wired so a write replicates across real
// daemons over the network.
func TestDaemonClusterReplicates(t *testing.T) {
	ids := []string{"node-0", "node-1", "node-2"}
	ds, cleanup := newTestCluster(t, ids, "")
	defer cleanup()
	ctx := context.Background()

	if err := ds["node-0"].Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put on node-0: %v", err)
	}
	got, err := ds["node-2"].Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("read from node-2 should see the write: got %q err %v", got, err)
	}
}

// TestDaemonClusterConverges lets the running SWIM loops discover each other over gRPC and
// waits for every node to see every other node alive.
func TestDaemonClusterConverges(t *testing.T) {
	ids := []string{"node-0", "node-1", "node-2"}
	ds, cleanup := newTestCluster(t, ids, "")
	defer cleanup()

	deadline := time.Now().Add(5 * time.Second)
	for !allAlive(ds, ids) {
		if time.Now().After(deadline) {
			t.Fatal("cluster did not converge to all-alive over gRPC in time")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDaemonClusterWithTLS runs the same replication check with mutual TLS loaded from cert
// files, proving the daemon's TLS file-loading path wires through to the server and transport.
func TestDaemonClusterWithTLS(t *testing.T) {
	ids := []string{"node-0", "node-1", "node-2"}
	dir := writeTestCerts(t, ids)
	ds, cleanup := newTestCluster(t, ids, dir)
	defer cleanup()
	ctx := context.Background()

	if err := ds["node-0"].Put(ctx, []byte("secure"), []byte("v")); err != nil {
		t.Fatalf("put over TLS: %v", err)
	}
	got, err := ds["node-1"].Get(ctx, []byte("secure"))
	if err != nil || string(got) != "v" {
		t.Fatalf("read over TLS should see the write: got %q err %v", got, err)
	}
}
