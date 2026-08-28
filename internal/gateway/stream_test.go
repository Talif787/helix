package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSnapshotAggregates(t *testing.T) {
	node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/members":
			_, _ = w.Write([]byte(`{"members":[{"id":"helix-0","state":"alive"}]}`))
		case "/metrics":
			_, _ = w.Write([]byte("helix_up 1\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer node.Close()

	srv := New(Config{
		HTTPTimeout: 2 * time.Second,
		Nodes:       []Node{{ID: "helix-0", AdminAddr: strings.TrimPrefix(node.URL, "http://")}},
	}, nil)

	snap := srv.snapshot(context.Background())
	if snap.TS == 0 {
		t.Error("snapshot should carry a timestamp")
	}
	if len(snap.Members.Nodes) != 1 || !snap.Members.Nodes[0].OK {
		t.Fatalf("members not aggregated: %+v", snap.Members)
	}
	if len(snap.Metrics.Nodes) != 1 || len(snap.Metrics.Nodes[0].Samples) != 1 {
		t.Fatalf("metrics not aggregated: %+v", snap.Metrics)
	}
}

func TestStreamInterval(t *testing.T) {
	if (&Server{}).streamInterval() != 2*time.Second {
		t.Error("zero interval should default to 2s")
	}
	if (&Server{cfg: Config{StreamInterval: 500 * time.Millisecond}}).streamInterval() != 500*time.Millisecond {
		t.Error("configured interval should be used")
	}
}

func TestStreamWritesSnapshotEventThenExits(t *testing.T) {
	srv := New(Config{HTTPTimeout: time.Second}, nil) // no nodes: snapshot is empty but valid

	// A canceled context makes the handler write the initial frame, then exit at the first select.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil).WithContext(ctx)
	rr := httptest.NewRecorder()

	srv.handleStream(rr, req)

	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("wrong content type: %q", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: snapshot") || !strings.Contains(body, "data: {") {
		t.Fatalf("expected an SSE snapshot frame, got: %q", body)
	}
	if !rr.Flushed {
		t.Error("the stream should have flushed the frame")
	}
}

// nonFlusher is a ResponseWriter that does not implement http.Flusher.
type nonFlusher struct {
	header http.Header
	code   int
}

func (n *nonFlusher) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}
func (n *nonFlusher) Write(b []byte) (int, error) { return len(b), nil }
func (n *nonFlusher) WriteHeader(code int)        { n.code = code }

func TestStreamRequiresFlusher(t *testing.T) {
	srv := New(Config{}, nil)
	w := &nonFlusher{}
	srv.handleStream(w, httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil))
	if w.code != http.StatusInternalServerError {
		t.Fatalf("without a Flusher the stream should 500, got %d", w.code)
	}
}
