package rpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/talifpathan/helix/internal/cluster"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// NodeServer adapts a node's local Replica surface to the generated NodeService, so a
// coordinator on another machine can reach it over gRPC. It serves the data plane (get,
// put, hint); the anti-entropy control plane is added in a later part.
type NodeServer struct {
	helixv1.UnimplementedNodeServiceServer
	backend cluster.Replica
}

// NewNodeServer wraps backend (in practice a *cluster.LocalNode) as a NodeService.
func NewNodeServer(backend cluster.Replica) *NodeServer {
	return &NodeServer{backend: backend}
}

// NewGRPCServer builds a grpc.Server with the NodeService registered on it. The caller
// supplies any server options (for example TLS credentials in a later part) and drives
// Serve and GracefulStop.
func NewGRPCServer(backend cluster.Replica, opts ...grpc.ServerOption) *grpc.Server {
	s := grpc.NewServer(opts...)
	helixv1.RegisterNodeServiceServer(s, NewNodeServer(backend))
	return s
}

// Get serves a versioned read. A missing key is reported as found=false, not an error.
func (s *NodeServer) Get(ctx context.Context, req *helixv1.GetRequest) (*helixv1.GetResponse, error) {
	vv, found, err := s.backend.GetVersioned(ctx, req.GetKey())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	resp := &helixv1.GetResponse{Found: found}
	if found {
		resp.Value = toProtoVersioned(vv)
	}
	return resp, nil
}

// Put serves a versioned write. The node reconciles it against any version it already holds.
func (s *NodeServer) Put(ctx context.Context, req *helixv1.PutRequest) (*helixv1.PutResponse, error) {
	if err := s.backend.PutVersioned(ctx, req.GetKey(), fromProtoVersioned(req.GetValue())); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &helixv1.PutResponse{}, nil
}

// PutHint buffers a write on behalf of an unreachable intended replica.
func (s *NodeServer) PutHint(ctx context.Context, req *helixv1.PutHintRequest) (*helixv1.PutHintResponse, error) {
	if err := s.backend.PutHint(ctx, req.GetIntended(), req.GetKey(), fromProtoVersioned(req.GetValue())); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &helixv1.PutHintResponse{}, nil
}
