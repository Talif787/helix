package rpc

import (
	"context"
	"errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/talifpathan/helix/internal/cluster"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
	"github.com/talifpathan/helix/internal/storage"
)

// CoordinatorAPI is the coordinated key-value surface the client plane serves. A
// *cluster.Coordinator satisfies it; the interface keeps this file from depending on the
// coordinator's concrete construction.
type CoordinatorAPI interface {
	Get(ctx context.Context, key []byte) ([]byte, error)
	Put(ctx context.Context, key, value []byte) error
	Delete(ctx context.Context, key []byte) error
}

// compile-time check that the coordinator satisfies the client-plane contract.
var _ CoordinatorAPI = (*cluster.Coordinator)(nil)

// ClientServer adapts a coordinator to the generated ClientService, so applications can issue
// coordinated reads and writes over gRPC.
type ClientServer struct {
	helixv1.UnimplementedClientServiceServer
	coord CoordinatorAPI
}

// NewClientServer wraps coord as a ClientService.
func NewClientServer(coord CoordinatorAPI) *ClientServer {
	return &ClientServer{coord: coord}
}

// RegisterClientService registers the client plane on an existing gRPC server, alongside the
// node and membership planes.
func RegisterClientService(s *grpc.Server, coord CoordinatorAPI) {
	helixv1.RegisterClientServiceServer(s, NewClientServer(coord))
}

// Get runs a quorum read. A missing key is reported as found=false, not an error.
func (s *ClientServer) Get(ctx context.Context, req *helixv1.ClientGetRequest) (*helixv1.ClientGetResponse, error) {
	v, err := s.coord.Get(ctx, req.GetKey())
	if errors.Is(err, storage.ErrNotFound) {
		return &helixv1.ClientGetResponse{Found: false}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &helixv1.ClientGetResponse{Value: v, Found: true}, nil
}

// Put runs a quorum write.
func (s *ClientServer) Put(ctx context.Context, req *helixv1.ClientPutRequest) (*helixv1.ClientPutResponse, error) {
	if err := s.coord.Put(ctx, req.GetKey(), req.GetValue()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &helixv1.ClientPutResponse{}, nil
}

// Delete runs a quorum delete (a tombstone write).
func (s *ClientServer) Delete(ctx context.Context, req *helixv1.ClientDeleteRequest) (*helixv1.ClientDeleteResponse, error) {
	if err := s.coord.Delete(ctx, req.GetKey()); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &helixv1.ClientDeleteResponse{}, nil
}
