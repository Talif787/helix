package cluster

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// memReplica is an in-memory Replica for coordinator tests. It reconciles on write like a
// real node. failGet/failPut simulate an unreachable or failing node; they are atomic
// because a coordinator returns once its quorum is met, leaving other fan-out goroutines
// still calling in while a test toggles these flags for the next step.
type memReplica struct {
	mu      sync.Mutex
	data    map[string]VersionedValue
	hints   map[string][]KeyVersion // intended node -> buffered hints
	failGet atomic.Bool
	failPut atomic.Bool
	puts    int
}

func newMemReplica() *memReplica {
	return &memReplica{data: make(map[string]VersionedValue), hints: make(map[string][]KeyVersion)}
}

func (m *memReplica) GetVersioned(_ context.Context, key []byte) (VersionedValue, bool, error) {
	if m.failGet.Load() {
		return VersionedValue{}, false, errors.New("get failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	vv, ok := m.data[string(key)]
	return vv, ok, nil
}

func (m *memReplica) PutVersioned(_ context.Context, key []byte, vv VersionedValue) error {
	if m.failPut.Load() {
		return errors.New("put failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.data[string(key)]; ok {
		vv = Reconcile(vv, cur)
	}
	m.data[string(key)] = vv
	m.puts++
	return nil
}

func (m *memReplica) PutHint(_ context.Context, intended string, key []byte, vv VersionedValue) error {
	if m.failPut.Load() {
		return errors.New("hint failed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hints[intended] = append(m.hints[intended], KeyVersion{Key: append([]byte(nil), key...), Value: vv})
	return nil
}

func (m *memReplica) hintCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, hs := range m.hints {
		n += len(hs)
	}
	return n
}

func (m *memReplica) entries(filter KeyFilter) []KeyVersion {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]KeyVersion, 0, len(m.data))
	for k, vv := range m.data {
		key := []byte(k)
		if filter != nil && !filter(key) {
			continue
		}
		out = append(out, KeyVersion{Key: key, Value: vv})
	}
	return out
}

func (m *memReplica) MerkleTree(_ context.Context, filter KeyFilter) (*MerkleTree, error) {
	return BuildMerkleTree(m.entries(filter))
}

func (m *memReplica) BucketEntries(_ context.Context, buckets []int, filter KeyFilter) ([]KeyVersion, error) {
	want := make(map[int]bool, len(buckets))
	for _, b := range buckets {
		want[b] = true
	}
	var out []KeyVersion
	for _, kv := range m.entries(filter) {
		if want[bucketOf(kv.Key)] {
			out = append(out, kv)
		}
	}
	return out, nil
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
	return NewCoordinator(ring, tr, 3, r, w, 0, now, nil), reps
}

func TestCoordinatorWriteMeetsQuorumDespiteOneFailure(t *testing.T) {
	co, reps := buildCoord(t, 2, 2)
	ctx := context.Background()
	pref := co.ring.LookupN([]byte("k"), 3)
	reps[pref[2]].failPut.Store(true)

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
	reps[pref[0]].failPut.Store(true)

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
	reps[pref[0]].failGet.Store(true)
	reps[pref[1]].failGet.Store(true)

	if _, err := co.Get(ctx, []byte("k")); !errors.Is(err, ErrReadQuorum) {
		t.Fatalf("want ErrReadQuorum, got %v", err)
	}
}

func TestCoordinatorReadRepairsStaleReplica(t *testing.T) {
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
	now := func() int64 { tick++; return tick }
	ctx := context.Background()
	key := []byte("k")
	stale := ring.LookupN(key, 3)[2]

	// Seed v1 with W=3 so every replica, including the one we will fail, deterministically
	// holds v1 before we start. A W=2 seed would only guarantee two replicas, leaving it a
	// race whether the third ever received v1.
	seed := NewCoordinator(ring, tr, 3, 2, 3, 0, now, nil)
	if err := seed.Put(ctx, key, []byte("v1")); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	if vv, ok := reps[stale].get("k"); !ok || string(vv.Value) != "v1" {
		t.Fatalf("precondition: stale replica should hold v1 after a W=3 seed, has %q", vv.Value)
	}

	// Write v2 with W=2 while the stale replica rejects writes; it must retain v1.
	co := NewCoordinator(ring, tr, 3, 2, 2, 0, now, nil)
	reps[stale].failPut.Store(true)
	if err := co.Put(ctx, key, []byte("v2")); err != nil {
		t.Fatalf("put v2: %v", err)
	}
	if vv, _ := reps[stale].get("k"); string(vv.Value) != "v1" {
		t.Fatalf("stale replica should still hold v1, has %q", vv.Value)
	}

	// A read reconciles to v2 and repairs the stale replica.
	reps[stale].failPut.Store(false)
	got, err := co.Get(ctx, key)
	if err != nil || string(got) != "v2" {
		t.Fatalf("get should return v2: got %q err %v", got, err)
	}
	if vv, ok := reps[stale].get("k"); !ok || string(vv.Value) != "v2" {
		t.Fatalf("read repair should have written v2 to the stale replica, has %q", vv.Value)
	}
}

func TestCoordinatorNoNodes(t *testing.T) {
	co := NewCoordinator(NewRing(8), NewInProcessTransport(), 3, 2, 2, 0, nil, nil)
	if _, err := co.Get(context.Background(), []byte("k")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Get: want ErrNoNodes, got %v", err)
	}
	if err := co.Put(context.Background(), []byte("k"), []byte("v")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Put: want ErrNoNodes, got %v", err)
	}
}
