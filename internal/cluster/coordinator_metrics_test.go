package cluster

import (
	"context"
	"sync"
	"testing"

	"github.com/talifpathan/helix/internal/storage"
)

type recordedObs struct {
	op, result string
}

type fakeMetrics struct {
	mu  sync.Mutex
	obs []recordedObs
}

func (f *fakeMetrics) ObserveRequest(op, result string, _ float64) {
	f.mu.Lock()
	f.obs = append(f.obs, recordedObs{op, result})
	f.mu.Unlock()
}

func (f *fakeMetrics) results(op string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, o := range f.obs {
		if o.op == op {
			out = append(out, o.result)
		}
	}
	return out
}

func TestCoordinatorReportsRequestMetrics(t *testing.T) {
	// Single in-process replica with N=R=W=1 so every op commits.
	tr := NewInProcessTransport()
	tr.Register("n1", newMemReplica())
	ring := NewRing(64)
	ring.Add("n1")
	coord := NewCoordinator(ring, tr, 1, 1, 1, 0, nil, nil)

	fm := &fakeMetrics{}
	coord.SetMetrics(fm)
	ctx := context.Background()

	if err := coord.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := coord.Get(ctx, []byte("k")); err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, err := coord.Get(ctx, []byte("missing")); err != storage.ErrNotFound {
		t.Fatalf("get missing: want ErrNotFound, got %v", err)
	}
	if err := coord.Delete(ctx, []byte("k")); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if got := fm.results("put"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("put results = %v, want [ok]", got)
	}
	if got := fm.results("delete"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("delete results = %v, want [ok]", got)
	}
	if got := fm.results("get"); len(got) != 2 || got[0] != "ok" || got[1] != "not_found" {
		t.Fatalf("get results = %v, want [ok not_found]", got)
	}
}
