package rpc

import (
	"context"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/membership"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// MembershipServer adapts a SWIM engine's Handler to the generated MembershipService, so a
// peer can probe this node over gRPC. The engine's own outbound probes go through a
// GRPCMessenger; this server is only the receiving side.
type MembershipServer struct {
	helixv1.UnimplementedMembershipServiceServer
	handler membership.Handler
}

// NewMembershipServer wraps handler (in practice a *membership.Swim).
func NewMembershipServer(handler membership.Handler) *MembershipServer {
	return &MembershipServer{handler: handler}
}

// RegisterMembershipService registers a membership handler on an existing gRPC server.
func RegisterMembershipService(s *grpc.Server, handler membership.Handler) {
	helixv1.RegisterMembershipServiceServer(s, NewMembershipServer(handler))
}

// NewServer builds a gRPC server hosting both planes a node needs: the data plane
// (NodeService) when node is non-nil, and the membership plane (MembershipService) when
// member is non-nil. The daemon uses this to serve everything on one listener.
func NewServer(node cluster.Replica, member membership.Handler, opts ...grpc.ServerOption) *grpc.Server {
	s := grpc.NewServer(opts...)
	if node != nil {
		helixv1.RegisterNodeServiceServer(s, NewNodeServer(node))
	}
	if member != nil {
		RegisterMembershipService(s, member)
	}
	return s
}

// Ping applies the caller's gossip and returns this node's gossip as the ack.
func (s *MembershipServer) Ping(_ context.Context, req *helixv1.PingRequest) (*helixv1.PingResponse, error) {
	out := s.handler.HandlePing(req.GetFrom(), fromProtoUpdates(req.GetGossip()))
	return &helixv1.PingResponse{Gossip: toProtoUpdates(out)}, nil
}

// PingReq asks this node to probe target on the caller's behalf and relays the outcome.
func (s *MembershipServer) PingReq(ctx context.Context, req *helixv1.PingReqRequest) (*helixv1.PingReqResponse, error) {
	out, err := s.handler.HandlePingReq(ctx, req.GetFrom(), req.GetTarget(), fromProtoUpdates(req.GetGossip()))
	if err != nil {
		// A relayed probe that could not reach the target is a normal negative result, not a
		// server fault, so it is reported to the caller as an error on the RPC.
		return nil, err
	}
	return &helixv1.PingReqResponse{Gossip: toProtoUpdates(out)}, nil
}
