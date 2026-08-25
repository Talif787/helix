package rpc

import (
	"context"
	"sync"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/talifpathan/helix/internal/membership"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// GRPCMessenger implements membership.Messenger over gRPC: a SWIM engine uses it to probe
// peers on other machines. It resolves node ids to addresses through a PeerRegistry, dials
// lazily, and caches one MembershipService client per node. An unresolved id or a failed
// dial is reported as membership.ErrUnreachable, which the engine treats as a failed probe.
// It is safe for concurrent use.
type GRPCMessenger struct {
	peers    *PeerRegistry
	dialOpts []grpc.DialOption

	mu      sync.Mutex
	conns   map[string]*grpc.ClientConn
	clients map[string]helixv1.MembershipServiceClient
}

var _ membership.Messenger = (*GRPCMessenger)(nil)

// NewGRPCMessenger builds a messenger backed by peers. With no dial options, clients connect
// plaintext; a later part supplies TLS credentials.
func NewGRPCMessenger(peers *PeerRegistry, dialOpts ...grpc.DialOption) *GRPCMessenger {
	return &GRPCMessenger{
		peers:    peers,
		dialOpts: dialOpts,
		conns:    make(map[string]*grpc.ClientConn),
		clients:  make(map[string]helixv1.MembershipServiceClient),
	}
}

// client returns a cached MembershipService client for nodeID, dialing on first use.
func (m *GRPCMessenger) client(nodeID string) (helixv1.MembershipServiceClient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[nodeID]; ok {
		return c, nil
	}
	addr, ok := m.peers.Address(nodeID)
	if !ok {
		return nil, membership.ErrUnreachable
	}
	opts := m.dialOpts
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, membership.ErrUnreachable
	}
	c := helixv1.NewMembershipServiceClient(conn)
	m.conns[nodeID] = conn
	m.clients[nodeID] = c
	return c, nil
}

// Ping directly probes to, carrying gossip, and returns the ack's gossip.
func (m *GRPCMessenger) Ping(ctx context.Context, from, to string, gossip []membership.Update) ([]membership.Update, error) {
	c, err := m.client(to)
	if err != nil {
		return nil, err
	}
	resp, err := c.Ping(ctx, &helixv1.PingRequest{From: from, To: to, Gossip: toProtoUpdates(gossip)})
	if err != nil {
		return nil, err
	}
	return fromProtoUpdates(resp.GetGossip()), nil
}

// PingReq asks via to probe target on from's behalf.
func (m *GRPCMessenger) PingReq(ctx context.Context, from, via, target string, gossip []membership.Update) ([]membership.Update, error) {
	c, err := m.client(via)
	if err != nil {
		return nil, err
	}
	resp, err := c.PingReq(ctx, &helixv1.PingReqRequest{From: from, Target: target, Gossip: toProtoUpdates(gossip)})
	if err != nil {
		return nil, err
	}
	return fromProtoUpdates(resp.GetGossip()), nil
}

// Close shuts every cached connection, returning the first error.
func (m *GRPCMessenger) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for id, conn := range m.conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(m.conns, id)
		delete(m.clients, id)
	}
	return firstErr
}
