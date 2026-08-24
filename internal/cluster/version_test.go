package cluster

import (
	"bytes"
	"testing"
)

func TestVectorClockCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b VectorClock
		want Ordering
	}{
		{"equal empty", VectorClock{}, VectorClock{}, Equal},
		{"equal", VectorClock{"a": 2}, VectorClock{"a": 2}, Equal},
		{"before by counter", VectorClock{"a": 1}, VectorClock{"a": 2}, Before},
		{"after by counter", VectorClock{"a": 3}, VectorClock{"a": 2}, After},
		{"before by missing key", VectorClock{"a": 1}, VectorClock{"a": 1, "b": 1}, Before},
		{"after by extra key", VectorClock{"a": 1, "b": 1}, VectorClock{"a": 1}, After},
		{"concurrent", VectorClock{"a": 1}, VectorClock{"b": 1}, Concurrent},
		{"concurrent mixed", VectorClock{"a": 2, "b": 1}, VectorClock{"a": 1, "b": 2}, Concurrent},
	}
	for _, tc := range cases {
		if got := tc.a.Compare(tc.b); got != tc.want {
			t.Errorf("%s: Compare = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestVectorClockMergeAndIncr(t *testing.T) {
	a := VectorClock{"x": 2, "y": 1}
	b := VectorClock{"y": 3, "z": 1}
	m := a.Merge(b)
	if m["x"] != 2 || m["y"] != 3 || m["z"] != 1 {
		t.Fatalf("merge wrong: %v", m)
	}
	// Merge must not mutate its inputs.
	if a["z"] != 0 || b["x"] != 0 {
		t.Fatalf("merge mutated an input: a=%v b=%v", a, b)
	}
	inc := a.Incr("x")
	if inc["x"] != 3 || a["x"] != 2 {
		t.Fatalf("incr wrong or mutated original: inc=%v a=%v", inc, a)
	}
}

func TestReconcileCausalWinsOverTimestamp(t *testing.T) {
	// v2 descends from v1 but has an earlier timestamp; causality must still win.
	v1 := VersionedValue{Value: []byte("a"), Clock: VectorClock{"p": 1}, Timestamp: 100}
	v2 := VersionedValue{Value: []byte("b"), Clock: VectorClock{"p": 2}, Timestamp: 50}
	got := Reconcile(v1, v2)
	if string(got.Value) != "b" {
		t.Fatalf("causal descendant should win: got %q", got.Value)
	}
	if got.Clock["p"] != 2 {
		t.Fatalf("winner should carry merged clock: %v", got.Clock)
	}
}

func TestReconcileConcurrentUsesLWW(t *testing.T) {
	a := VersionedValue{Value: []byte("x"), Clock: VectorClock{"p": 1}, Timestamp: 100}
	b := VersionedValue{Value: []byte("y"), Clock: VectorClock{"q": 1}, Timestamp: 200}
	if a.Clock.Compare(b.Clock) != Concurrent {
		t.Fatal("precondition: clocks should be concurrent")
	}
	got := Reconcile(a, b)
	if string(got.Value) != "y" {
		t.Fatalf("higher timestamp should win: got %q", got.Value)
	}
	if got.Clock["p"] != 1 || got.Clock["q"] != 1 {
		t.Fatalf("winner should carry merged clock: %v", got.Clock)
	}
}

func TestReconcileTombstone(t *testing.T) {
	live := VersionedValue{Value: []byte("z"), Clock: VectorClock{"p": 2}, Timestamp: 1}
	tomb := VersionedValue{Clock: VectorClock{"p": 3}, Timestamp: 9, Deleted: true}
	got := Reconcile(live, tomb)
	if !got.Deleted {
		t.Fatal("causally later tombstone should win")
	}
}

func TestVersionedEncodeDecodeRoundTrip(t *testing.T) {
	in := VersionedValue{
		Value:     []byte("hello world"),
		Clock:     VectorClock{"node-a": 3, "node-b": 1},
		Timestamp: 1234567890,
		Deleted:   false,
	}
	raw, err := encodeVersioned(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodeVersioned(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(in.Value, out.Value) || in.Timestamp != out.Timestamp || in.Deleted != out.Deleted {
		t.Fatalf("round trip mismatch: %+v vs %+v", in, out)
	}
	if out.Clock.Compare(in.Clock) != Equal {
		t.Fatalf("clock round trip mismatch: %v vs %v", in.Clock, out.Clock)
	}
}

func TestVersionedDecodeTombstoneHasClock(t *testing.T) {
	raw, err := encodeVersioned(VersionedValue{Clock: VectorClock{"p": 1}, Deleted: true})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out, err := decodeVersioned(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Deleted || out.Clock == nil || out.Clock["p"] != 1 {
		t.Fatalf("tombstone lost its clock: %+v", out)
	}
}
