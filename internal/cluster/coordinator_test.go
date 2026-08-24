package cluster

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// memReplica is an in-memory Replica for coordinator tests. It reconciles on write like a
// real node, and its failGet/failPut flags simulate an unreachable or failing node.
type memReplica struct {
	mu               sync.Mutex
	data             map[string]VersionedValue
	failGet, failPut bool
	puts             int
}

func newMemReplica() *memReplica { return &memReplica{data: make(map[string]VersionedValue)} }

func (m *memReplica) GetVersioned(_ context.Context, key []byte) (VersionedValue, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failGet {
		return VersionedValue{}, false, errors.New("get failed")
	}
	vv, ok := m.data[string(key)]
	return vv, ok, nil
}

func (m *memReplica) PutVersioned(_ context.Context, key []byte, vv VersionedValue) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return errors.New("put failed")
	}
	if cur, ok := m.data[string(key)]; ok {
		vv = Reconcile(vv, cur)
	}
	m.data[string(key)] = vv
	m.puts++
	return nil
}

func (m *memReplica) get(key string) (VersionedValue, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vv, ok := m.data[key]
	return vv, ok
}

// buildCoord makes a coordinator over three in-memory replicas with N=3 so every key's
// preference list is all three nodes. It returns the replicas keyed by node id.
func buildCoord(t *testing.T, r, w int) (*Coordinator, map[string]*memReplica) {
	t.Helper()
	ids := []string{"n1", "n2", "n3"}
	ring := NewRing(32)
	tr := NewInProcessTransport()
	reps := make(map[string]*memReplica, len(ids))
	for _, id := range ids {
		ring.Add(id)
		mr := newMemReplica()
		reps[id] = mr
		tr.Register(id, mr)
	}
	var tick int64
	now := func() int64 { tick++; return tick } // deterministic timestamps
	return NewCoordinator(ring, tr, 3, r, w, now, nil), reps
}

func TestCoordinatorWriteMeetsQuorumDespiteOneFailure(t *testing.T) {
	co, reps := buildCoord(t, 2, 2)
	ctx := context.Background()
	pref := co.ring.LookupN([]byte("k"), 3)
	reps[pref[2]].failPut = true

	if err := co.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("put should meet W=2 with one failure: %v", err)
	}
	got, err := co.Get(ctx, []byte("k"))
	if err != nil || string(got) != "v" {
		t.Fatalf("get after quorum write: got %q err %v", got, err)
	}
}

func TestCoordinatorWriteFailsBelowQuorum(t *testing.T) {
	co, reps := buildCoord(t, 2, 3) // W=3 requires all three
	ctx := context.Background()
	pref := co.ring.LookupN([]byte("k"), 3)
	reps[pref[0]].failPut = true

	if err := co.Put(ctx, []byte("k"), []byte("v")); !errors.Is(err, ErrWriteQuorum) {
		t.Fatalf("want ErrWriteQuorum, got %v", err)
	}
}

func TestCoordinatorReadFailsBelowQuorum(t *testing.T) {
	co, reps := buildCoord(t, 2, 1) // R=2
	ctx := context.Background()
	if err := co.Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	pref := co.ring.LookupN([]byte("k"), 3)
	reps[pref[0]].failGet = true
	reps[pref[1]].failGet = true

	if _, err := co.Get(ctx, []byte("k")); !errors.Is(err, ErrReadQuorum) {
		t.Fatalf("want ErrReadQuorum, got %v", err)
	}
}

func TestCoordinatorReadRepairsStaleReplica(t *testing.T) {
	co, reps := buildCoord(t, 2, 2)
	ctx := context.Background()
	key := []byte("k")
	pref := co.ring.LookupN(key, 3)
	stale := pref[2]

	if err := co.Put(ctx, key, []byte("v1")); err != nil {
		t.Fatalf("put v1: %v", err)
	}
	reps[stale].failPut = true
	if err := co.Put(ctx, key, []byte("v2")); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if vv, _ := reps[stale].get("k"); string(vv.Value) != "v1" {
		t.Fatalf("precondition: stale replica should still hold v1, has %q", vv.Value)
	}

	reps[stale].failPut = false
	got, err := co.Get(ctx, key)
	if err != nil || string(got) != "v2" {
		t.Fatalf("get should return v2: got %q err %v", got, err)
	}
	if vv, ok := reps[stale].get("k"); !ok || string(vv.Value) != "v2" {
		t.Fatalf("read repair should have written v2 to the stale replica, has %q", vv.Value)
	}
}

func TestCoordinatorNoNodes(t *testing.T) {
	co := NewCoordinator(NewRing(8), NewInProcessTransport(), 3, 2, 2, nil, nil)
	if _, err := co.Get(context.Background(), []byte("k")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Get: want ErrNoNodes, got %v", err)
	}
	if err := co.Put(context.Background(), []byte("k"), []byte("v")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Put: want ErrNoNodes, got %v", err)
	}
}
