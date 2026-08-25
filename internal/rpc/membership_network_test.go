package rpc

import (
	"context"
	"math/rand"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/membership"
)

// swimNode bundles a SWIM engine with the gRPC server that receives probes for it.
type swimNode struct {
	id     string
	engine *membership.Swim
	srv    *grpc.Server
}

// startSwimCluster builds len(ids) SWIM engines, each served over gRPC on localhost and
// sharing one gRPC messenger, and returns them plus a cleanup func.
func startSwimCluster(t *testing.T, ids []string) (map[string]*swimNode, *GRPCMessenger, func()) {
	t.Helper()
	peers := NewPeerRegistry()
	msngr := NewGRPCMessenger(peers)
	nodes := make(map[string]*swimNode, len(ids))

	for i, id := range ids {
		cfg := membership.SwimConfig{
			SuspicionTicks: 3,
			IndirectProbes: 2,
			PingTimeout:    500 * time.Millisecond,
			Rand:           rand.New(rand.NewSource(int64(i) + 1)),
		}
		engine := membership.NewSwim(id, msngr, ids, cfg)

		lis, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen %s: %v", id, err)
		}
		srv := NewServer(nil, engine) // membership plane only
		go func() { _ = srv.Serve(lis) }()

		peers.Set(id, lis.Addr().String())
		nodes[id] = &swimNode{id: id, engine: engine, srv: srv}
	}

	cleanup := func() {
		for _, n := range nodes {
			n.srv.GracefulStop()
		}
		_ = msngr.Close()
	}
	return nodes, msngr, cleanup
}

func runSwimRounds(nodes map[string]*swimNode, rounds int) {
	ctx := context.Background()
	order := make([]string, 0, len(nodes))
	for id := range nodes {
		order = append(order, id)
	}
	for i := 0; i < rounds; i++ {
		for _, id := range order {
			nodes[id].engine.RunOnce(ctx)
		}
	}
}

func allSeeState(nodes map[string]*swimNode, target string, want membership.State) bool {
	for _, n := range nodes {
		if st, ok := n.engine.List().StateOf(target); !ok || st != want {
			return false
		}
	}
	return true
}

func TestGRPCSwimConverges(t *testing.T) {
	ids := []string{"node-a", "node-b", "node-c", "node-d"}
	nodes, _, cleanup := startSwimCluster(t, ids)
	defer cleanup()

	runSwimRounds(nodes, 12)
	for _, target := range ids {
		if !allSeeState(nodes, target, membership.Alive) {
			t.Fatalf("healthy cluster over gRPC should see %s alive everywhere", target)
		}
	}
}

func TestGRPCSwimDetectsFailure(t *testing.T) {
	ids := []string{"node-a", "node-b", "node-c", "node-d"}
	nodes, _, cleanup := startSwimCluster(t, ids)
	defer cleanup()

	runSwimRounds(nodes, 8) // converge first

	// Kill node-d: stop its server so probes to it fail, and drop it from the round set.
	dead := "node-d"
	nodes[dead].srv.GracefulStop()
	delete(nodes, dead)

	// Survivors probe node-d, fail directly and indirectly, suspect it, and after the
	// suspicion timeout gossip it dead. Extra rounds cover the network round-trips.
	runSwimRounds(nodes, 30)

	if !allSeeState(nodes, dead, membership.Dead) {
		states := map[string]string{}
		for id, n := range nodes {
			st, _ := n.engine.List().StateOf(dead)
			states[id] = st.String()
		}
		t.Fatalf("survivors should converge on %s dead over gRPC, got %v", dead, states)
	}
}
