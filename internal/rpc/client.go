package rpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/talifpathan/helix/internal/cluster"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// NodeClient is a gRPC client to a remote node that satisfies cluster.Replica, so a
// coordinator can treat a remote replica exactly like a local one. Connections are lazy:
// grpc.NewClient does not dial until the first RPC.
type NodeClient struct {
	conn *grpc.ClientConn
	cli  helixv1.NodeServiceClient
}

// DialNode creates a client to the node listening at addr ("host:port"). With no options it
// uses an insecure (plaintext) connection; a later part supplies TLS credentials instead.
func DialNode(addr string, opts ...grpc.DialOption) (*NodeClient, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}
	return &NodeClient{conn: conn, cli: helixv1.NewNodeServiceClient(conn)}, nil
}

// Close releases the underlying connection.
func (c *NodeClient) Close() error { return c.conn.Close() }

// GetVersioned implements cluster.Replica.
func (c *NodeClient) GetVersioned(ctx context.Context, key []byte) (cluster.VersionedValue, bool, error) {
	resp, err := c.cli.Get(ctx, &helixv1.GetRequest{Key: key})
	if err != nil {
		return cluster.VersionedValue{}, false, err
	}
	if !resp.GetFound() {
		return cluster.VersionedValue{}, false, nil
	}
	return fromProtoVersioned(resp.GetValue()), true, nil
}

// PutVersioned implements cluster.Replica.
func (c *NodeClient) PutVersioned(ctx context.Context, key []byte, vv cluster.VersionedValue) error {
	_, err := c.cli.Put(ctx, &helixv1.PutRequest{Key: key, Value: toProtoVersioned(vv)})
	return err
}

// PutHint implements cluster.Replica.
func (c *NodeClient) PutHint(ctx context.Context, intended string, key []byte, vv cluster.VersionedValue) error {
	_, err := c.cli.PutHint(ctx, &helixv1.PutHintRequest{
		Intended: intended,
		Key:      key,
		Value:    toProtoVersioned(vv),
	})
	return err
}

// MerkleTree implements cluster.Replica. Anti-entropy over the wire is added in a later
// part; until then this reports Unimplemented rather than silently doing nothing.
func (c *NodeClient) MerkleTree(_ context.Context, _ cluster.KeyFilter) (*cluster.MerkleTree, error) {
	return nil, status.Error(codes.Unimplemented, "merkle exchange over gRPC arrives in a later part")
}

// BucketEntries implements cluster.Replica. See MerkleTree.
func (c *NodeClient) BucketEntries(_ context.Context, _ []int, _ cluster.KeyFilter) ([]cluster.KeyVersion, error) {
	return nil, status.Error(codes.Unimplemented, "bucket exchange over gRPC arrives in a later part")
}
