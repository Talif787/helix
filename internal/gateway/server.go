// Package gateway is the backend-for-frontend for the Helix operator console. It exposes a small
// REST API over the cluster: read-only observability aggregated from each node's admin API (and,
// in later slices, KV read-write over gRPC and a live stream). It applies operator bearer-token
// auth and a strict CORS allowlist, since the console can mutate cluster state.
package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/talifpathan/helix/internal/rpc"
)

// Server holds the gateway's configuration and its HTTP handler.
type Server struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client
	kv   []kvClient
	mux  http.Handler
}

// New builds a Server. A nil logger uses slog.Default. It dials a KV client per node (lazily, so a
// momentarily unreachable node does not fail startup); the KV endpoints fail over across these.
func New(cfg Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Timeout: cfg.HTTPTimeout},
	}
	for _, n := range cfg.Nodes {
		if n.GRPCAddr == "" {
			continue
		}
		c, err := rpc.DialClient(n.GRPCAddr)
		if err != nil {
			log.Warn("gateway: could not create KV client", "node", n.ID, "addr", n.GRPCAddr, "err", err)
			continue
		}
		s.kv = append(s.kv, c)
	}
	s.mux = s.routes()
	return s
}

// Handler returns the fully wrapped HTTP handler (routes plus CORS).
func (s *Server) Handler() http.Handler { return s.mux }

// Close releases the gateway's gRPC connections to the cluster.
func (s *Server) Close() error {
	var firstErr error
	for _, c := range s.kv {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Open: the gateway's own liveness, so a load balancer or tunnel can probe it without a token.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	// Authenticated API.
	mux.Handle("GET /api/v1/cluster/status", s.auth(http.HandlerFunc(s.handleClusterStatus)))
	mux.Handle("GET /api/v1/kv/{key}", s.auth(http.HandlerFunc(s.handleKVGet)))
	mux.Handle("PUT /api/v1/kv/{key}", s.auth(http.HandlerFunc(s.handleKVPut)))
	mux.Handle("DELETE /api/v1/kv/{key}", s.auth(http.HandlerFunc(s.handleKVDelete)))

	return s.cors(mux)
}

// auth enforces the operator bearer token in constant time. With no token configured, auth is
// disabled (development only); main logs a warning in that case.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token == "" {
			next.ServeHTTP(w, r)
			return
		}
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, prefix) ||
			subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(h, prefix)), []byte(s.cfg.Token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// cors applies a strict single-origin allowlist when configured, and answers preflight requests.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.CORSOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", s.cfg.CORSOrigin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
