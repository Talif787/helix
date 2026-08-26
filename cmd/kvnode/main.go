// Command kvnode is the Helix node entrypoint. It builds a clustered node from environment
// configuration, serves the data and membership planes over gRPC (optionally with mutual
// TLS), joins the cluster through the configured peer set, and runs the membership,
// anti-entropy, and hint-delivery loops until it receives a shutdown signal. The heavy
// lifting lives in internal/daemon; this file is configuration and lifecycle.
//
// Configuration (environment variables, layered over the storage and logging config):
//
//	HELIX_NODE_ID              this node's id (required; must appear in HELIX_PEERS)
//	HELIX_PEERS                comma-separated id=addr for every node, e.g.
//	                           "node-a=127.0.0.1:7070,node-b=127.0.0.1:7071"
//	HELIX_BIND_ADDR            address to listen on (defaults to this node's peer address)
//	HELIX_N, HELIX_R, HELIX_W  replication factor and read/write quorums (default 3, 2, 2)
//	HELIX_VNODES               virtual nodes per node on the ring (default 128)
//	HELIX_MAX_HINTS            max hinted-handoff entries per node (default N)
//	HELIX_TLS_CERT/KEY/CA      PEM paths for mutual TLS (all three, or none for plaintext)
//	HELIX_METRICS_ADDR         if set, serve /metrics and /healthz over HTTP here
//	HELIX_SWIM_INTERVAL        membership round period (default 1s)
//	HELIX_ANTIENTROPY_INTERVAL anti-entropy round period (default 30s)
//	HELIX_HINT_INTERVAL        hint-delivery period (default 10s)
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/talifpathan/helix/internal/config"
	"github.com/talifpathan/helix/internal/daemon"
	"github.com/talifpathan/helix/internal/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	base, err := config.Load()
	if err != nil {
		return err
	}
	logger := observability.NewLogger(base.LogLevel, base.LogFormat)

	dcfg, err := daemonConfigFromEnv(base, logger)
	if err != nil {
		return err
	}

	d, err := daemon.New(dcfg)
	if err != nil {
		return err
	}
	if err := d.Start(); err != nil {
		return err
	}
	logger.Info("kvnode ready", "node", dcfg.NodeID, "addr", d.Addr())

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	logger.Info("shutdown signal received")
	return d.Stop()
}

// daemonConfigFromEnv builds the daemon config from the environment, reusing the storage and
// logging config already parsed by config.Load.
func daemonConfigFromEnv(base config.Config, logger *slog.Logger) (daemon.Config, error) {
	nodeID := os.Getenv("HELIX_NODE_ID")
	if nodeID == "" {
		return daemon.Config{}, fmt.Errorf("HELIX_NODE_ID is required")
	}
	peers, err := parsePeers(os.Getenv("HELIX_PEERS"))
	if err != nil {
		return daemon.Config{}, err
	}
	if _, ok := peers[nodeID]; !ok {
		return daemon.Config{}, fmt.Errorf("HELIX_PEERS must include this node %q", nodeID)
	}

	bind := os.Getenv("HELIX_BIND_ADDR")
	if bind == "" {
		bind = peers[nodeID]
	}

	n, err := envInt("HELIX_N", 3)
	if err != nil {
		return daemon.Config{}, err
	}
	r, err := envInt("HELIX_R", 2)
	if err != nil {
		return daemon.Config{}, err
	}
	w, err := envInt("HELIX_W", 2)
	if err != nil {
		return daemon.Config{}, err
	}
	vnodes, err := envInt("HELIX_VNODES", 128)
	if err != nil {
		return daemon.Config{}, err
	}
	maxHints, err := envInt("HELIX_MAX_HINTS", n)
	if err != nil {
		return daemon.Config{}, err
	}

	swimIv, err := envDuration("HELIX_SWIM_INTERVAL", time.Second)
	if err != nil {
		return daemon.Config{}, err
	}
	aeIv, err := envDuration("HELIX_ANTIENTROPY_INTERVAL", 30*time.Second)
	if err != nil {
		return daemon.Config{}, err
	}
	hintIv, err := envDuration("HELIX_HINT_INTERVAL", 10*time.Second)
	if err != nil {
		return daemon.Config{}, err
	}

	return daemon.Config{
		NodeID:              nodeID,
		BindAddr:            bind,
		Peers:               peers,
		N:                   n,
		R:                   r,
		W:                   w,
		VNodes:              vnodes,
		MaxHints:            maxHints,
		TLSCert:             os.Getenv("HELIX_TLS_CERT"),
		TLSKey:              os.Getenv("HELIX_TLS_KEY"),
		TLSCA:               os.Getenv("HELIX_TLS_CA"),
		MetricsAddr:         os.Getenv("HELIX_METRICS_ADDR"),
		Storage:             base.ToStorageOptions(logger),
		SwimInterval:        swimIv,
		AntiEntropyInterval: aeIv,
		HintInterval:        hintIv,
		Logger:              logger,
	}, nil
}

// parsePeers parses "id=addr,id=addr" into a map.
func parsePeers(s string) (map[string]string, error) {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 || eq == len(pair)-1 {
			return nil, fmt.Errorf("HELIX_PEERS entry %q must be id=addr", pair)
		}
		id := strings.TrimSpace(pair[:eq])
		addr := strings.TrimSpace(pair[eq+1:])
		out[id] = addr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("HELIX_PEERS must list at least one id=addr")
	}
	return out, nil
}

func envInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, v)
	}
	return n, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration, got %q", key, v)
	}
	return d, nil
}
