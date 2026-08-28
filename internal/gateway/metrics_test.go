package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePrometheus(t *testing.T) {
	text := `# HELP helix_up 1 if the node process is running
# TYPE helix_up gauge
helix_up 1
helix_uptime_seconds 42
helix_cluster_members{state="alive"} 3
helix_cluster_members{state="dead"} 0
helix_storage_sstables 2 1700000000000
garbage line that should be skipped
`
	samples := parsePrometheus(text)

	byKey := func(name, state string) (float64, bool) {
		for _, s := range samples {
			if s.Name == name && (state == "" || s.Labels["state"] == state) {
				return s.Value, true
			}
		}
		return 0, false
	}

	if v, ok := byKey("helix_up", ""); !ok || v != 1 {
		t.Errorf("helix_up = %v %v", v, ok)
	}
	if v, ok := byKey("helix_uptime_seconds", ""); !ok || v != 42 {
		t.Errorf("uptime = %v %v", v, ok)
	}
	if v, ok := byKey("helix_cluster_members", "alive"); !ok || v != 3 {
		t.Errorf("members alive = %v %v", v, ok)
	}
	// A trailing timestamp must not break value parsing.
	if v, ok := byKey("helix_storage_sstables", ""); !ok || v != 2 {
		t.Errorf("sstables (with timestamp) = %v %v", v, ok)
	}
	// Comment and garbage lines are skipped; count the real samples.
	if len(samples) != 5 {
		t.Errorf("expected 5 samples, got %d: %+v", len(samples), samples)
	}
}

func TestSplitLabelsQuoteAware(t *testing.T) {
	// A comma inside a quoted value must not split the pair.
	parts := splitLabels(`state="a,b",kind="x"`)
	if len(parts) != 2 {
		t.Fatalf("want 2 label pairs, got %d: %v", len(parts), parts)
	}
	labels := parseLabels(`state="a,b",kind="x"`)
	if labels["state"] != "a,b" || labels["kind"] != "x" {
		t.Fatalf("labels parsed wrong: %+v", labels)
	}
}

func TestClusterMetricsAggregates(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("helix_up 1\nhelix_cluster_members{state=\"alive\"} 3\n"))
	}))
	defer good.Close()

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

	cm := srv.clusterMetrics(context.Background())
	if len(cm.Nodes) != 2 {
		t.Fatalf("want 2 node results, got %d", len(cm.Nodes))
	}
	if !cm.Nodes[0].OK || len(cm.Nodes[0].Samples) != 2 {
		t.Fatalf("healthy node should have 2 samples: %+v", cm.Nodes[0])
	}
	if cm.Nodes[1].OK || cm.Nodes[1].Error == "" {
		t.Fatalf("failing node should be not-OK with an error: %+v", cm.Nodes[1])
	}
}

func TestClusterMembersEndpoint(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/members" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"members":[{"id":"helix-0","state":"alive"}]}`))
	}))
	defer node.Close()

	srv := New(Config{
		HTTPTimeout: 2 * time.Second,
		Nodes:       []Node{{ID: "helix-0", AdminAddr: strings.TrimPrefix(node.URL, "http://")}},
	}, nil)

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cluster/members", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("members endpoint status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"members"`) || !strings.Contains(rr.Body.String(), "helix-0") {
		t.Fatalf("members body should relay node data: %s", rr.Body.String())
	}
}
