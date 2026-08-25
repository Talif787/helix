package rpc

import (
	"crypto/tls"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// ServerTLSOption turns a TLS config into a gRPC server option, so a node's server presents
// its certificate and requires a client certificate. Pass it to NewGRPCServer or NewServer.
func ServerTLSOption(cfg *tls.Config) grpc.ServerOption {
	return grpc.Creds(credentials.NewTLS(cfg))
}

// ClientTLSOption turns a TLS config into a gRPC dial option, so the transport and messenger
// present the node's certificate and verify the peer. Pass it to NewGRPCTransport,
// NewGRPCMessenger, or DialNode.
func ClientTLSOption(cfg *tls.Config) grpc.DialOption {
	return grpc.WithTransportCredentials(credentials.NewTLS(cfg))
}
