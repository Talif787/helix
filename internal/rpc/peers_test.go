package rpc

import (
	"reflect"
	"sync"
	"testing"
)

func TestPeerRegistrySetAddressRemove(t *testing.T) {
	r := NewPeerRegistry()
	if _, ok := r.Address("node-a"); ok {
		t.Fatal("empty registry should not resolve node-a")
	}
	r.Set("node-a", "10.0.0.1:7000")
	r.Set("node-b", "10.0.0.2:7000")
	if addr, ok := r.Address("node-a"); !ok || addr != "10.0.0.1:7000" {
		t.Fatalf("node-a should resolve, got %q ok=%v", addr, ok)
	}
	// Set overwrites.
	r.Set("node-a", "10.0.0.9:7000")
	if addr, _ := r.Address("node-a"); addr != "10.0.0.9:7000" {
		t.Fatalf("Set should overwrite, got %q", addr)
	}
	if r.Len() != 2 {
		t.Fatalf("expected 2 peers, got %d", r.Len())
	}
	r.Remove("node-a")
	if _, ok := r.Address("node-a"); ok {
		t.Fatal("node-a should be gone after Remove")
	}
	if r.Len() != 1 {
		t.Fatalf("expected 1 peer after remove, got %d", r.Len())
	}
}

func TestPeerRegistryNodesSorted(t *testing.T) {
	r := NewPeerRegistryFromMap(map[string]string{
		"node-c": "c:1", "node-a": "a:1", "node-b": "b:1",
	})
	got := r.Nodes()
	want := []string{"node-a", "node-b", "node-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Nodes should be sorted, got %v want %v", got, want)
	}
}

func TestPeerRegistrySnapshotIsCopy(t *testing.T) {
	r := NewPeerRegistryFromMap(map[string]string{"node-a": "a:1"})
	snap := r.Snapshot()
	snap["node-a"] = "mutated"
	snap["node-z"] = "z:1"
	if addr, _ := r.Address("node-a"); addr != "a:1" {
		t.Fatalf("mutating the snapshot must not affect the registry, got %q", addr)
	}
	if _, ok := r.Address("node-z"); ok {
		t.Fatal("adding to the snapshot must not affect the registry")
	}
}

func TestPeerRegistryConcurrentAccess(t *testing.T) {
	r := NewPeerRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "node-" + string(rune('a'+i%26))
			r.Set(id, "addr")
			_, _ = r.Address(id)
			_ = r.Nodes()
			_ = r.Snapshot()
		}(i)
	}
	wg.Wait()
	if r.Len() == 0 {
		t.Fatal("expected some peers registered")
	}
}
