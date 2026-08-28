package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseNodes(t *testing.T) {
	nodes, err := parseNodes("helix-0=127.0.0.1:7070;127.0.0.1:9090, helix-1=127.0.0.1:7071;127.0.0.1:9091")
	if err != nil {
		t.Fatalf("valid nodes should parse: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(nodes))
	}
	if nodes[0].ID != "helix-0" || nodes[0].GRPCAddr != "127.0.0.1:7070" || nodes[0].AdminAddr != "127.0.0.1:9090" {
		t.Fatalf("node 0 parsed wrong: %+v", nodes[0])
	}
	for _, bad := range []string{"", "helix-0", "helix-0=onlyone", "=grpc;admin", "helix-0=;admin", "helix-0=grpc;"} {
		if _, err := parseNodes(bad); err == nil {
			t.Errorf("parseNodes(%q) should error", bad)
		}
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("HELIX_GATEWAY_NODES", "helix-0=127.0.0.1:7070;127.0.0.1:9090")
	t.Setenv("HELIX_GATEWAY_TOKEN", "op-secret")
	t.Setenv("HELIX_GATEWAY_CORS_ORIGIN", "https://console.example")
	t.Setenv("HELIX_GATEWAY_HTTP_TIMEOUT", "3s")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != ":8080" {
		t.Errorf("default addr wrong: %q", cfg.Addr)
	}
	if cfg.Token != "op-secret" || cfg.CORSOrigin != "https://console.example" {
		t.Errorf("token/cors wrong: %+v", cfg)
	}
	if cfg.HTTPTimeout != 3*time.Second || len(cfg.Nodes) != 1 {
		t.Errorf("timeout/nodes wrong: %+v", cfg)
	}

	t.Setenv("HELIX_GATEWAY_HTTP_TIMEOUT", "notaduration")
	if _, err := FromEnv(); err == nil {
		t.Error("a bad HTTP timeout should error")
	}
}

func TestAuthMiddleware(t *testing.T) {
	srv := New(Config{Token: "op-secret"}, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := srv.auth(ok)

	cases := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"not bearer", "op-secret", http.StatusUnauthorized},
		{"correct", "Bearer op-secret", http.StatusTeapot},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/cluster/status", nil)
		if c.header != "" {
			req.Header.Set("Authorization", c.header)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rr.Code, c.want)
		}
	}
}

func TestAuthDisabledWhenNoToken(t *testing.T) {
	srv := New(Config{Token: ""}, nil)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	rr := httptest.NewRecorder()
	srv.auth(ok).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusTeapot {
		t.Fatalf("with no token, auth should pass through: got %d", rr.Code)
	}
}

func TestCORSAndHealthz(t *testing.T) {
	srv := New(Config{CORSOrigin: "https://console.example"}, nil)

	// Preflight is answered with the allowlist and 204.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/api/v1/cluster/status", nil))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://console.example" {
		t.Fatalf("missing CORS origin header: %v", rr.Header())
	}

	// Health is open and needs no token.
	hr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(hr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if hr.Code != http.StatusOK || !strings.Contains(hr.Body.String(), "ok") {
		t.Fatalf("healthz wrong: %d %q", hr.Code, hr.Body.String())
	}
}

func TestClusterStatusAggregatesPartialFailures(t *testing.T) {
	// A healthy node serving /api/v1/status.
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_id":"helix-0","config":{"n":3}}`))
	}))
	defer good.Close()

	// A failing node.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()

	srv := New(Config{
		HTTPTimeout: 2 * time.Second,
		Nodes: []Node{
			{ID: "helix-0", AdminAddr: strings.TrimPrefix(good.URL, "http://")},
			{ID: "helix-1", AdminAddr: strings.TrimPrefix(bad.URL, "http://")},
		},
	}, nil)

	cs := srv.clusterStatus(context.Background())
	if len(cs.Nodes) != 2 {
		t.Fatalf("want 2 node results, got %d", len(cs.Nodes))
	}
	if !cs.Nodes[0].OK || cs.Nodes[0].NodeID != "helix-0" {
		t.Fatalf("healthy node should be OK: %+v", cs.Nodes[0])
	}
	var status struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(cs.Nodes[0].Status, &status); err != nil || status.NodeID != "helix-0" {
		t.Fatalf("raw status should be relayed: %v %+v", err, status)
	}
	if cs.Nodes[1].OK || cs.Nodes[1].Error == "" {
		t.Fatalf("failing node should be reported not-OK with an error: %+v", cs.Nodes[1])
	}
}
