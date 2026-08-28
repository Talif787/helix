package gateway

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Node is one Helix node the gateway talks to: its gRPC address for the KV data plane (used from
// slice 2b) and its admin HTTP address for the status API and metrics.
type Node struct {
	ID        string
	GRPCAddr  string
	AdminAddr string
}

// Config controls the gateway. It is populated from HELIX_GATEWAY_* environment variables.
type Config struct {
	Addr        string        // listen address for the gateway HTTP server
	Token       string        // operator bearer token; empty disables auth (development only)
	CORSOrigin  string        // allowed browser origin; empty disables CORS
	Nodes       []Node        // cluster nodes to aggregate and proxy
	HTTPTimeout time.Duration // per-request timeout when calling a node
}

// FromEnv builds a Config from the environment:
//
//	HELIX_GATEWAY_ADDR          listen address (default ":8080")
//	HELIX_GATEWAY_TOKEN         operator bearer token (recommended; empty disables auth)
//	HELIX_GATEWAY_CORS_ORIGIN   allowed browser origin (empty disables CORS)
//	HELIX_GATEWAY_NODES         comma list of "id=grpcHost:7070;adminHost:9090"
//	HELIX_GATEWAY_HTTP_TIMEOUT  per-node call timeout (default "5s")
func FromEnv() (Config, error) {
	cfg := Config{
		Addr:       envOr("HELIX_GATEWAY_ADDR", ":8080"),
		Token:      os.Getenv("HELIX_GATEWAY_TOKEN"),
		CORSOrigin: os.Getenv("HELIX_GATEWAY_CORS_ORIGIN"),
	}

	d, err := time.ParseDuration(envOr("HELIX_GATEWAY_HTTP_TIMEOUT", "5s"))
	if err != nil {
		return Config{}, fmt.Errorf("HELIX_GATEWAY_HTTP_TIMEOUT: %w", err)
	}
	cfg.HTTPTimeout = d

	nodes, err := parseNodes(os.Getenv("HELIX_GATEWAY_NODES"))
	if err != nil {
		return Config{}, err
	}
	cfg.Nodes = nodes
	return cfg, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseNodes parses "id=grpcAddr;adminAddr,id=grpcAddr;adminAddr" into Nodes.
func parseNodes(s string) ([]Node, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("HELIX_GATEWAY_NODES must list at least one node (id=grpcAddr;adminAddr)")
	}
	var out []Node
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idRest := strings.SplitN(entry, "=", 2)
		if len(idRest) != 2 || strings.TrimSpace(idRest[0]) == "" {
			return nil, fmt.Errorf("bad node entry %q: want id=grpcAddr;adminAddr", entry)
		}
		addrs := strings.SplitN(idRest[1], ";", 2)
		if len(addrs) != 2 || strings.TrimSpace(addrs[0]) == "" || strings.TrimSpace(addrs[1]) == "" {
			return nil, fmt.Errorf("bad node entry %q: want id=grpcAddr;adminAddr", entry)
		}
		out = append(out, Node{
			ID:        strings.TrimSpace(idRest[0]),
			GRPCAddr:  strings.TrimSpace(addrs[0]),
			AdminAddr: strings.TrimSpace(addrs[1]),
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("HELIX_GATEWAY_NODES must list at least one node (id=grpcAddr;adminAddr)")
	}
	return out, nil
}
