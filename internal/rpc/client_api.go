package rpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// Client is a handle to a Helix cluster's client plane. It dials one node, which coordinates
// each request across the cluster. Any program (including helixctl) can use it. It is safe for
// concurrent use.
type Client struct {
	conn *grpc.ClientConn
	cli  helixv1.ClientServiceClient
}

// DialClient connects to a node's ClientService at addr ("host:port"). With no options it uses
// an insecure (plaintext) connection; pass ClientTLSOption(cfg) for mutual TLS. The connection
// is lazy, so DialClient does not fail merely because the node is momentarily unreachable.
func DialClient(addr string, opts ...grpc.DialOption) (*Client, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, cli: helixv1.NewClientServiceClient(conn)}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() error { return c.conn.Close() }

// Put commits a coordinated write.
func (c *Client) Put(ctx context.Context, key, value []byte) error {
	_, err := c.cli.Put(ctx, &helixv1.ClientPutRequest{Key: key, Value: value})
	return err
}

// Get performs a coordinated read. found is false when the key is absent.
func (c *Client) Get(ctx context.Context, key []byte) (value []byte, found bool, err error) {
	resp, err := c.cli.Get(ctx, &helixv1.ClientGetRequest{Key: key})
	if err != nil {
		return nil, false, err
	}
	if !resp.GetFound() {
		return nil, false, nil
	}
	return resp.GetValue(), true, nil
}

// Delete commits a coordinated delete.
func (c *Client) Delete(ctx context.Context, key []byte) error {
	_, err := c.cli.Delete(ctx, &helixv1.ClientDeleteRequest{Key: key})
	return err
}
