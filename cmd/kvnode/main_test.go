package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/config"
)

func TestEnvInt(t *testing.T) {
	if v, err := envInt("HELIX_TEST_UNSET_INT", 7); err != nil || v != 7 {
		t.Fatalf("unset should return the fallback: got %d err %v", v, err)
	}
	t.Setenv("HELIX_TEST_INT", "42")
	if v, err := envInt("HELIX_TEST_INT", 7); err != nil || v != 42 {
		t.Fatalf("set should parse: got %d err %v", v, err)
	}
	t.Setenv("HELIX_TEST_INT", "notanint")
	if _, err := envInt("HELIX_TEST_INT", 7); err == nil {
		t.Fatal("a non-integer value should error")
	}
}

func TestEnvDuration(t *testing.T) {
	if v, err := envDuration("HELIX_TEST_UNSET_DUR", 2*time.Second); err != nil || v != 2*time.Second {
		t.Fatalf("unset should return the fallback: got %v err %v", v, err)
	}
	t.Setenv("HELIX_TEST_DUR", "1500ms")
	if v, err := envDuration("HELIX_TEST_DUR", time.Second); err != nil || v != 1500*time.Millisecond {
		t.Fatalf("set should parse: got %v err %v", v, err)
	}
	t.Setenv("HELIX_TEST_DUR", "notaduration")
	if _, err := envDuration("HELIX_TEST_DUR", time.Second); err == nil {
		t.Fatal("a non-duration value should error")
	}
}

func TestParsePeers(t *testing.T) {
	m, err := parsePeers("n0=h0:7070, n1=h1:7070")
	if err != nil {
		t.Fatalf("valid peers should parse: %v", err)
	}
	if m["n0"] != "h0:7070" || m["n1"] != "h1:7070" {
		t.Fatalf("unexpected map: %v", m)
	}
	for _, bad := range []string{"", "bad", "=addr", "id="} {
		if _, err := parsePeers(bad); err == nil {
			t.Errorf("parsePeers(%q) should error", bad)
		}
	}
}

func TestDaemonConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("HELIX_NODE_ID", "n0")
	t.Setenv("HELIX_PEERS", "n0=127.0.0.1:7070,n1=127.0.0.1:7071")

	cfg, err := daemonConfigFromEnv(config.Config{}, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.NodeID != "n0" {
		t.Errorf("NodeID = %q, want n0", cfg.NodeID)
	}
	if cfg.BindAddr != "127.0.0.1:7070" {
		t.Errorf("BindAddr defaults to the node's peer addr, got %q", cfg.BindAddr)
	}
	if cfg.N != 3 || cfg.R != 2 || cfg.W != 2 || cfg.VNodes != 128 {
		t.Errorf("defaults N/R/W/VNODES wrong: %d/%d/%d/%d", cfg.N, cfg.R, cfg.W, cfg.VNodes)
	}
	if cfg.MaxHints != 3 {
		t.Errorf("MaxHints defaults to N, got %d", cfg.MaxHints)
	}
	if cfg.RequestTimeout != 2*time.Second {
		t.Errorf("RequestTimeout default = %v, want 2s", cfg.RequestTimeout)
	}
	if cfg.SwimInterval != time.Second || cfg.HintInterval != 10*time.Second {
		t.Errorf("interval defaults wrong: swim=%v hint=%v", cfg.SwimInterval, cfg.HintInterval)
	}
}

func TestDaemonConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("HELIX_NODE_ID", "n1")
	t.Setenv("HELIX_PEERS", "n0=h0:7070,n1=h1:7070")
	t.Setenv("HELIX_BIND_ADDR", "0.0.0.0:7070")
	t.Setenv("HELIX_N", "5")
	t.Setenv("HELIX_REQUEST_TIMEOUT", "500ms")

	cfg, err := daemonConfigFromEnv(config.Config{}, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BindAddr != "0.0.0.0:7070" {
		t.Errorf("explicit bind addr should win, got %q", cfg.BindAddr)
	}
	if cfg.N != 5 {
		t.Errorf("N override = %d, want 5", cfg.N)
	}
	if cfg.MaxHints != 5 {
		t.Errorf("MaxHints should default to the overridden N, got %d", cfg.MaxHints)
	}
	if cfg.RequestTimeout != 500*time.Millisecond {
		t.Errorf("RequestTimeout override = %v, want 500ms", cfg.RequestTimeout)
	}
}

func TestDaemonConfigFromEnvErrors(t *testing.T) {
	// Missing node id.
	t.Setenv("HELIX_NODE_ID", "")
	if _, err := daemonConfigFromEnv(config.Config{}, slog.Default()); err == nil {
		t.Error("missing HELIX_NODE_ID should error")
	}

	// Node id not present in the peer list.
	t.Setenv("HELIX_NODE_ID", "nX")
	t.Setenv("HELIX_PEERS", "n0=h0:7070")
	if _, err := daemonConfigFromEnv(config.Config{}, slog.Default()); err == nil {
		t.Error("a node id absent from HELIX_PEERS should error")
	}

	// Bad request timeout.
	t.Setenv("HELIX_NODE_ID", "n0")
	t.Setenv("HELIX_PEERS", "n0=h0:7070")
	t.Setenv("HELIX_REQUEST_TIMEOUT", "notaduration")
	if _, err := daemonConfigFromEnv(config.Config{}, slog.Default()); err == nil {
		t.Error("a non-duration HELIX_REQUEST_TIMEOUT should error")
	}
}
