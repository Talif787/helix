package simulation

import (
	"context"
	"testing"
)

// TestWorkloadNoFaults runs concurrent writers and readers with no faults and asserts both
// safety invariants hold: no fabricated reads and read-after-write freshness.
func TestWorkloadNoFaults(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2,
		BaseDir: t.TempDir(), Seed: 1,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	all := []string{"n0", "n1", "n2"}
	h := RunWorkload(context.Background(), c, WorkloadConfig{
		Keys:         []string{"k0", "k1", "k2", "k3", "k4"},
		WritesPerKey: 20,
		Readers:      4,
		Seed:         1,
		WriterNodes:  all,
		ReaderNodes:  all,
	})
	events := h.Events()

	if len(events) < 5*20 {
		t.Fatalf("expected at least the writes to be recorded, got %d events", len(events))
	}
	if vs := CheckNoFabrication(events); len(vs) > 0 {
		t.Fatalf("fabrication violations: %v", vs)
	}
	if vs := CheckFreshness(events); len(vs) > 0 {
		t.Fatalf("freshness violations: %v", vs)
	}
}

// TestWorkloadMajorityUnderPartition isolates one node and drives the workload only through the
// majority side. Every operation should succeed and both invariants should hold: a minority
// partition does not compromise the majority's consistency.
func TestWorkloadMajorityUnderPartition(t *testing.T) {
	c, err := NewCluster(Config{
		IDs: []string{"n0", "n1", "n2"}, N: 3, R: 2, W: 2,
		BaseDir: t.TempDir(), Seed: 2,
	})
	if err != nil {
		t.Fatalf("new cluster: %v", err)
	}
	defer c.Close()

	c.Net.Partition("n2") // isolate the minority

	majority := []string{"n0", "n1"}
	h := RunWorkload(context.Background(), c, WorkloadConfig{
		Keys:         []string{"a", "b", "c"},
		WritesPerKey: 15,
		Readers:      3,
		Seed:         2,
		WriterNodes:  majority,
		ReaderNodes:  majority,
	})
	events := h.Events()

	for _, e := range events {
		if !e.OK {
			t.Fatalf("majority-side op failed under a minority partition: %+v", e)
		}
	}
	if vs := CheckNoFabrication(events); len(vs) > 0 {
		t.Fatalf("fabrication violations: %v", vs)
	}
	if vs := CheckFreshness(events); len(vs) > 0 {
		t.Fatalf("freshness violations: %v", vs)
	}
}

// TestFreshnessCatchesStaleRead feeds the checker a history where a read returns a value older
// than a write that had already completed, and asserts the checker reports it. This guards
// against a checker that passes vacuously.
func TestFreshnessCatchesStaleRead(t *testing.T) {
	h := NewHistory()
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#1", OK: true, StartSeq: 10, EndSeq: 11})
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#2", OK: true, StartSeq: 20, EndSeq: 21})
	// A read that began at 25 (after k#2 completed at 21) must not return k#1.
	h.record(Event{Kind: OpGet, Key: "k", Value: "k#1", Found: true, OK: true, StartSeq: 25, EndSeq: 26})

	if vs := CheckFreshness(h.Events()); len(vs) == 0 {
		t.Fatal("expected a freshness violation for a stale read")
	}
}

// TestFreshnessCatchesLostWrite feeds the checker a read that returns not-found after a write
// completed, and asserts it reports it.
func TestFreshnessCatchesLostWrite(t *testing.T) {
	h := NewHistory()
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#1", OK: true, StartSeq: 1, EndSeq: 2})
	h.record(Event{Kind: OpGet, Key: "k", Found: false, OK: true, StartSeq: 3, EndSeq: 4})

	if vs := CheckFreshness(h.Events()); len(vs) == 0 {
		t.Fatal("expected a freshness violation for a lost write (not-found after a completed write)")
	}
}

// TestNoFabricationCatchesPhantom feeds the checker a read of a value that was never written.
func TestNoFabricationCatchesPhantom(t *testing.T) {
	h := NewHistory()
	h.record(Event{Kind: OpPut, Key: "k", Value: "real", OK: true, StartSeq: 1, EndSeq: 2})
	h.record(Event{Kind: OpGet, Key: "k", Value: "phantom", Found: true, OK: true, StartSeq: 3, EndSeq: 4})

	if vs := CheckNoFabrication(h.Events()); len(vs) == 0 {
		t.Fatal("expected a fabrication violation")
	}
}

// TestValidHistoryHasNoViolations confirms a correct sequential history passes both checks, so
// the checkers are not simply always-failing.
func TestValidHistoryHasNoViolations(t *testing.T) {
	h := NewHistory()
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#1", OK: true, StartSeq: 1, EndSeq: 2})
	h.record(Event{Kind: OpGet, Key: "k", Value: "k#1", Found: true, OK: true, StartSeq: 3, EndSeq: 4})
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#2", OK: true, StartSeq: 5, EndSeq: 6})
	h.record(Event{Kind: OpGet, Key: "k", Value: "k#2", Found: true, OK: true, StartSeq: 7, EndSeq: 8})
	// A read concurrent with a write may return either the old or the new value.
	h.record(Event{Kind: OpPut, Key: "k", Value: "k#3", OK: true, StartSeq: 9, EndSeq: 12})
	h.record(Event{Kind: OpGet, Key: "k", Value: "k#2", Found: true, OK: true, StartSeq: 10, EndSeq: 11})

	if vs := CheckNoFabrication(h.Events()); len(vs) > 0 {
		t.Fatalf("unexpected fabrication violations: %v", vs)
	}
	if vs := CheckFreshness(h.Events()); len(vs) > 0 {
		t.Fatalf("unexpected freshness violations: %v", vs)
	}
}
