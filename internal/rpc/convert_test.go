package rpc

import (
	"bytes"
	"testing"

	"github.com/talifpathan/helix/internal/cluster"
)

func TestVersionedValueProtoRoundTrip(t *testing.T) {
	cases := []cluster.VersionedValue{
		{Value: []byte("hello"), Clock: cluster.VectorClock{"node-a": 3, "node-b": 1}, Timestamp: 12345, Deleted: false},
		{Value: nil, Clock: cluster.VectorClock{"node-a": 7}, Timestamp: 99, Deleted: true}, // a tombstone
		{Value: []byte(""), Clock: cluster.VectorClock{}, Timestamp: 0, Deleted: false},     // empty clock
	}
	for i, in := range cases {
		out := fromProtoVersioned(toProtoVersioned(in))
		if !bytes.Equal(out.Value, in.Value) {
			t.Errorf("case %d: value %q != %q", i, out.Value, in.Value)
		}
		if out.Timestamp != in.Timestamp || out.Deleted != in.Deleted {
			t.Errorf("case %d: meta mismatch: %+v vs %+v", i, out, in)
		}
		if len(out.Clock) != len(in.Clock) {
			t.Errorf("case %d: clock size %d != %d", i, len(out.Clock), len(in.Clock))
		}
		for k, v := range in.Clock {
			if out.Clock[k] != v {
				t.Errorf("case %d: clock[%s] %d != %d", i, k, out.Clock[k], v)
			}
		}
		// The reconciled ordering must be preserved across the round trip.
		if in.Clock.Compare(out.Clock) != cluster.Equal {
			t.Errorf("case %d: clocks should compare Equal after round trip", i)
		}
	}
}
