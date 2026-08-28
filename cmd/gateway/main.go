// Command gateway is the backend-for-frontend for the Helix operator console. It reads its
// configuration from HELIX_GATEWAY_* environment variables (see internal/gateway.FromEnv) and
// serves a small REST API that aggregates the cluster's admin endpoints and, in later slices,
// proxies KV read-write over gRPC and streams live updates.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/talifpathan/helix/internal/gateway"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := gateway.FromEnv()
	if err != nil {
		log.Error("gateway configuration", "err", err)
		os.Exit(1)
	}
	if cfg.Token == "" {
		log.Warn("HELIX_GATEWAY_TOKEN is empty: the gateway API is unauthenticated (development only)")
	}

	srv := gateway.New(cfg, log)
	log.Info("gateway listening", "addr", cfg.Addr, "nodes", len(cfg.Nodes))

	httpSrv := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}
	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("gateway server", "err", err)
		os.Exit(1)
	}
}
