package cluster

import (
	"context"
	"errors"
	"testing"
)

type stubStore struct{ id string }

func (s stubStore) Get(context.Context, []byte) ([]byte, error) { return []byte(s.id), nil }
func (s stubStore) Put(context.Context, []byte, []byte) error   { return nil }
func (s stubStore) Delete(context.Context, []byte) error        { return nil }

func TestInProcessTransportRegistration(t *testing.T) {
	tr := NewInProcessTransport()
	tr.Register("n1", stubStore{"n1"})

	s, ok := tr.Store("n1")
	if !ok {
		t.Fatal("expected n1 to resolve")
	}
	v, _ := s.Get(context.Background(), []byte("k"))
	if string(v) != "n1" {
		t.Fatalf("resolved the wrong store: %s", v)
	}
	if _, ok := tr.Store("missing"); ok {
		t.Fatal("unknown node should not resolve")
	}
	tr.Deregister("n1")
	if _, ok := tr.Store("n1"); ok {
		t.Fatal("deregistered node should not resolve")
	}
}

func TestCoordinatorErrorsWithoutNodes(t *testing.T) {
	co := NewCoordinator(NewRing(8), NewInProcessTransport())
	ctx := context.Background()
	if _, err := co.Get(ctx, []byte("k")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Get: want ErrNoNodes, got %v", err)
	}
	if err := co.Put(ctx, []byte("k"), []byte("v")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Put: want ErrNoNodes, got %v", err)
	}
	if err := co.Delete(ctx, []byte("k")); !errors.Is(err, ErrNoNodes) {
		t.Fatalf("Delete: want ErrNoNodes, got %v", err)
	}
}

func TestCoordinatorErrorsWhenOwnerUnregistered(t *testing.T) {
	ring := NewRing(8)
	ring.Add("ghost") // present on the ring but never registered in the transport
	co := NewCoordinator(ring, NewInProcessTransport())
	if _, err := co.Get(context.Background(), []byte("k")); !errors.Is(err, ErrNodeUnavailable) {
		t.Fatalf("want ErrNodeUnavailable, got %v", err)
	}
}

func TestCoordinatorRoutesToOwner(t *testing.T) {
	ring := NewRing(32)
	ring.Add("n1")
	ring.Add("n2")
	tr := NewInProcessTransport()
	tr.Register("n1", stubStore{"n1"})
	tr.Register("n2", stubStore{"n2"})
	co := NewCoordinator(ring, tr)

	// The stub returns its own id from Get, so the value reveals which node was chosen,
	// and it must match the ring's own Lookup for the same key.
	for i := 0; i < 200; i++ {
		k := []byte("route-key-" + string(rune('A'+i%26)) + string(rune('0'+i%10)))
		want, _ := ring.Lookup(k)
		got, err := co.Get(context.Background(), k)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if string(got) != want {
			t.Fatalf("coordinator routed %q to %s, ring says %s", k, got, want)
		}
	}
}
