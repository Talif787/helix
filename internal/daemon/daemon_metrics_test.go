package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/storage"
)

func TestDaemonMetricsEndpoint(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()

	d, err := New(Config{
		NodeID:              "solo",
		BindAddr:            addr,
		Listener:            lis,
		Peers:               map[string]string{"solo": addr},
		MetricsAddr:         "127.0.0.1:0",
		Storage:             storage.Options{DataDir: t.TempDir()},
		SwimInterval:        time.Hour,
		AntiEntropyInterval: time.Hour,
		HintInterval:        time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	base := "http://" + d.MetricsAddr()

	// /healthz
	hresp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("get healthz: %v", err)
	}
	defer hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", hresp.StatusCode)
	}

	// /metrics
	mresp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("get metrics: %v", err)
	}
	defer mresp.Body.Close()
	if mresp.StatusCode != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", mresp.StatusCode)
	}
	body, _ := io.ReadAll(mresp.Body)
	text := string(body)

	for _, want := range []string{
		"helix_up 1",
		"# TYPE helix_cluster_members gauge",
		`helix_cluster_members{state="alive"}`,
		"helix_storage_sstables",
		"helix_uptime_seconds",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, text)
		}
	}
}

func TestDaemonRequestMetrics(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	d, err := New(Config{
		NodeID: "solo", BindAddr: addr, Listener: lis,
		Peers: map[string]string{"solo": addr},
		N:     1, R: 1, W: 1, // single-node quorum so the write commits
		MetricsAddr:  "127.0.0.1:0",
		Storage:      storage.Options{DataDir: t.TempDir()},
		SwimInterval: time.Hour, AntiEntropyInterval: time.Hour, HintInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	ctx := context.Background()
	if err := d.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := d.Get(ctx, []byte("k")); err != nil {
		t.Fatalf("get: %v", err)
	}

	resp, err := http.Get("http://" + d.MetricsAddr() + "/metrics")
	if err != nil {
		t.Fatalf("scrape: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)

	for _, want := range []string{
		"# TYPE helix_requests_total counter",
		`helix_requests_total{op="put",result="ok"} 1`,
		`helix_requests_total{op="get",result="ok"} 1`,
		"# TYPE helix_request_duration_seconds histogram",
		`helix_request_duration_seconds_count{op="put"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in exposition:\n%s", want, text)
		}
	}
}

func TestDaemonMetricsDisabledByDefault(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	d, err := New(Config{
		NodeID: "solo", BindAddr: addr, Listener: lis,
		Peers:               map[string]string{"solo": addr},
		Storage:             storage.Options{DataDir: t.TempDir()},
		SwimInterval:        time.Hour,
		AntiEntropyInterval: time.Hour,
		HintInterval:        time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()
	if d.MetricsAddr() != "" {
		t.Fatalf("metrics server should be disabled without MetricsAddr, got %q", d.MetricsAddr())
	}
}
