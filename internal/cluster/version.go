package cluster

import (
	"bytes"
	"encoding/json"
)

// Ordering is the result of comparing two vector clocks.
type Ordering int

const (
	// Equal means the clocks are identical.
	Equal Ordering = iota
	// Before means the receiver causally precedes the other (it is an ancestor).
	Before
	// After means the receiver causally follows the other (it is a descendant).
	After
	// Concurrent means neither precedes the other: a genuine conflict.
	Concurrent
)

// VectorClock maps a node id to the number of updates it has coordinated for a key. It
// captures causality between versions: comparing two clocks tells us whether one update
// descends from the other or whether they happened concurrently and must be reconciled.
type VectorClock map[string]uint64

// Clone returns an independent copy, never nil.
func (vc VectorClock) Clone() VectorClock {
	out := make(VectorClock, len(vc))
	for k, v := range vc {
		out[k] = v
	}
	return out
}

// Compare reports the causal relationship of vc to other.
func (vc VectorClock) Compare(other VectorClock) Ordering {
	less, greater := false, false
	for k, v := range vc {
		if v < other[k] {
			less = true
		} else if v > other[k] {
			greater = true
		}
	}
	for k, v := range other {
		if _, ok := vc[k]; !ok && v > 0 {
			less = true
		}
	}
	switch {
	case less && greater:
		return Concurrent
	case less:
		return Before
	case greater:
		return After
	default:
		return Equal
	}
}

// Merge returns the pointwise maximum of the two clocks, the causal join that descends
// from both.
func (vc VectorClock) Merge(other VectorClock) VectorClock {
	out := vc.Clone()
	for k, v := range other {
		if v > out[k] {
			out[k] = v
		}
	}
	return out
}

// Incr returns a copy with node's counter advanced by one, recording that node
// coordinated a new update.
func (vc VectorClock) Incr(node string) VectorClock {
	out := vc.Clone()
	out[node]++
	return out
}

// VersionedValue is a value tagged with the causal clock and wall-clock time of the write
// that produced it. Deleted marks a tombstone: a delete is stored as a version so its
// causal history survives for reconciliation and read repair.
type VersionedValue struct {
	Value     []byte
	Clock     VectorClock
	Timestamp int64 // UnixNano, used only to break ties between concurrent versions
	Deleted   bool
}

// Reconcile merges two versions of a key into one. If one causally descends from the
// other, the descendant wins with no data lost. If they are concurrent, last-write-wins
// by timestamp (then value bytes, then a preference for a live value) decides
// deterministically. Either way the result carries the merged clock, so it descends from
// both inputs and a later write built on it will too.
func Reconcile(a, b VersionedValue) VersionedValue {
	merged := a.Clock.Merge(b.Clock)
	var r VersionedValue
	switch a.Clock.Compare(b.Clock) {
	case After, Equal:
		r = a
	case Before:
		r = b
	default:
		r = lwwPick(a, b)
	}
	r.Clock = merged
	return r
}

func lwwPick(a, b VersionedValue) VersionedValue {
	if a.Timestamp != b.Timestamp {
		if a.Timestamp > b.Timestamp {
			return a
		}
		return b
	}
	if c := bytes.Compare(a.Value, b.Value); c != 0 {
		if c > 0 {
			return a
		}
		return b
	}
	// Identical value and timestamp: prefer a live value over a tombstone, else either.
	if a.Deleted == b.Deleted || !a.Deleted {
		return a
	}
	return b
}

// KeyVersion pairs a key with its versioned value. It is the unit exchanged by hinted
// handoff and, later, anti-entropy repair.
type KeyVersion struct {
	Key   []byte
	Value VersionedValue
}

// versionedWire is the JSON shape stored in the engine. JSON keeps the encoding simple
// and robust: []byte becomes base64 and VectorClock becomes an object automatically.
type versionedWire struct {
	V []byte            `json:"v,omitempty"`
	C map[string]uint64 `json:"c,omitempty"`
	T int64             `json:"t"`
	D bool              `json:"d,omitempty"`
}

func encodeVersioned(vv VersionedValue) ([]byte, error) {
	return json.Marshal(versionedWire{V: vv.Value, C: vv.Clock, T: vv.Timestamp, D: vv.Deleted})
}

func decodeVersioned(b []byte) (VersionedValue, error) {
	var w versionedWire
	if err := json.Unmarshal(b, &w); err != nil {
		return VersionedValue{}, err
	}
	clock := VectorClock(w.C)
	if clock == nil {
		clock = VectorClock{}
	}
	return VersionedValue{Value: w.V, Clock: clock, Timestamp: w.T, Deleted: w.D}, nil
}
