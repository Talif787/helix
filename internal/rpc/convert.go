package rpc

import (
	"github.com/talifpathan/helix/internal/cluster"
	helixv1 "github.com/talifpathan/helix/internal/rpc/helixv1"
)

// toProtoClock converts a cluster VectorClock to its proto form. A nil or empty clock
// becomes a proto message with an empty (non-nil) map so the shape is stable on the wire.
func toProtoClock(vc cluster.VectorClock) *helixv1.VectorClock {
	entries := make(map[string]uint64, len(vc))
	for k, v := range vc {
		entries[k] = v
	}
	return &helixv1.VectorClock{Entries: entries}
}

// fromProtoClock converts a proto VectorClock to the cluster type, always returning a
// non-nil map so downstream clock operations never write to a nil map.
func fromProtoClock(pc *helixv1.VectorClock) cluster.VectorClock {
	out := make(cluster.VectorClock, len(pc.GetEntries()))
	for k, v := range pc.GetEntries() {
		out[k] = v
	}
	return out
}

// toProtoVersioned converts a cluster VersionedValue to its proto form.
func toProtoVersioned(vv cluster.VersionedValue) *helixv1.VersionedValue {
	return &helixv1.VersionedValue{
		Value:     vv.Value,
		Clock:     toProtoClock(vv.Clock),
		Timestamp: vv.Timestamp,
		Deleted:   vv.Deleted,
	}
}

// fromProtoVersioned converts a proto VersionedValue to the cluster type. A nil message
// yields a zero value with an empty clock.
func fromProtoVersioned(pv *helixv1.VersionedValue) cluster.VersionedValue {
	return cluster.VersionedValue{
		Value:     pv.GetValue(),
		Clock:     fromProtoClock(pv.GetClock()),
		Timestamp: pv.GetTimestamp(),
		Deleted:   pv.GetDeleted(),
	}
}
