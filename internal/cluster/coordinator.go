package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/talifpathan/helix/internal/storage"
)

// Coordinator replicates each key to the N nodes on its preference list and enforces
// tunable quorums: a write waits for W acknowledgements, a read waits for R responses and
// reconciles them. Reads also repair replicas that answered with a stale version. There is
// no client-supplied causal context, so a write first does a best-effort read of the
// current versions to build a causal base, then increments the primary's clock entry; a
// write that races another produces concurrent clocks, which reconciliation resolves.
type Coordinator struct {
	ring *Ring
	tr   Transport
	n    int
	r    int
	w    int
	now  func() int64
	log  *slog.Logger
}

// NewCoordinator binds a ring and transport with replication factor n and quorums r and w.
// A nil now uses the wall clock; a nil logger uses the default.
func NewCoordinator(ring *Ring, tr Transport, n, r, w int, now func() int64, log *slog.Logger) *Coordinator {
	if now == nil {
		now = func() int64 { return time.Now().UnixNano() }
	}
	if log == nil {
		log = slog.Default()
	}
	return &Coordinator{ring: ring, tr: tr, n: n, r: r, w: w, now: now, log: log}
}

// Put replicates a write of value under key.
func (c *Coordinator) Put(ctx context.Context, key, value []byte) error {
	return c.write(ctx, key, value, false)
}

// Delete replicates a tombstone for key.
func (c *Coordinator) Delete(ctx context.Context, key []byte) error {
	return c.write(ctx, key, nil, true)
}

func (c *Coordinator) write(ctx context.Context, key, value []byte, deleted bool) error {
	pref := c.ring.LookupN(key, c.n)
	if len(pref) == 0 {
		return ErrNoNodes
	}
	need := c.w
	if need > len(pref) {
		need = len(pref)
	}

	// Best-effort pre-read to build a causal base, then increment the primary's entry.
	base := c.readClock(ctx, key, pref)
	vv := VersionedValue{
		Clock:     base.Incr(pref[0]),
		Timestamp: c.now(),
		Deleted:   deleted,
	}
	if !deleted {
		vv.Value = append([]byte(nil), value...)
	}

	results := make(chan error, len(pref))
	for _, id := range pref {
		go func(id string) {
			rep, ok := c.tr.Replica(id)
			if !ok {
				results <- ErrNodeUnavailable
				return
			}
			results <- rep.PutVersioned(ctx, key, vv)
		}(id)
	}

	acks := 0
	var firstErr error
	for i := 0; i < len(pref); i++ {
		err := <-results
		if err == nil {
			acks++
			if acks >= need {
				return nil
			}
		} else if firstErr == nil {
			firstErr = err
		}
		if acks+(len(pref)-1-i) < need { // remaining acks cannot reach the quorum
			break
		}
	}
	return fmt.Errorf("%w (%d/%d acks): %v", ErrWriteQuorum, acks, need, firstErr)
}

// readClock gathers the current versions from the preference list and merges their clocks.
// It is best-effort: unreachable or empty replicas contribute nothing, and it never fails
// the write.
func (c *Coordinator) readClock(ctx context.Context, key []byte, pref []string) VectorClock {
	type res struct {
		vv VersionedValue
		ok bool
	}
	ch := make(chan res, len(pref))
	for _, id := range pref {
		go func(id string) {
			rep, ok := c.tr.Replica(id)
			if !ok {
				ch <- res{}
				return
			}
			vv, found, err := rep.GetVersioned(ctx, key)
			ch <- res{vv: vv, ok: err == nil && found}
		}(id)
	}
	merged := VectorClock{}
	for i := 0; i < len(pref); i++ {
		if r := <-ch; r.ok {
			merged = merged.Merge(r.vv.Clock)
		}
	}
	return merged
}

type readResult struct {
	id    string
	vv    VersionedValue
	found bool
	err   error
}

// Get reads key from the preference list, requiring R responses, reconciles them, repairs
// any stale replica, and returns the winning value.
func (c *Coordinator) Get(ctx context.Context, key []byte) ([]byte, error) {
	pref := c.ring.LookupN(key, c.n)
	if len(pref) == 0 {
		return nil, ErrNoNodes
	}
	need := c.r
	if need > len(pref) {
		need = len(pref)
	}

	ch := make(chan readResult, len(pref))
	for _, id := range pref {
		go func(id string) {
			rep, ok := c.tr.Replica(id)
			if !ok {
				ch <- readResult{id: id, err: ErrNodeUnavailable}
				return
			}
			vv, found, err := rep.GetVersioned(ctx, key)
			ch <- readResult{id: id, vv: vv, found: found, err: err}
		}(id)
	}

	responders := make([]readResult, 0, len(pref))
	ok := 0
	var firstErr error
	for i := 0; i < len(pref); i++ {
		rr := <-ch
		responders = append(responders, rr)
		if rr.err == nil {
			ok++
		} else if firstErr == nil {
			firstErr = rr.err
		}
	}
	if ok < need {
		return nil, fmt.Errorf("%w (%d/%d responses): %v", ErrReadQuorum, ok, need, firstErr)
	}

	var winner VersionedValue
	have := false
	for _, rr := range responders {
		if rr.err == nil && rr.found {
			if !have {
				winner, have = rr.vv, true
			} else {
				winner = Reconcile(winner, rr.vv)
			}
		}
	}
	if !have {
		return nil, storage.ErrNotFound
	}

	c.readRepair(ctx, key, responders, winner)

	if winner.Deleted {
		return nil, storage.ErrNotFound
	}
	return append([]byte(nil), winner.Value...), nil
}

// readRepair pushes the reconciled winner to any responder that was missing the key or
// held a strictly older version. Replicas that did not respond are left for a later phase
// (hinted handoff). Repairs are best-effort and their errors are ignored.
func (c *Coordinator) readRepair(ctx context.Context, key []byte, responders []readResult, winner VersionedValue) {
	for _, rr := range responders {
		if rr.err != nil {
			continue
		}
		if rr.found && rr.vv.Clock.Compare(winner.Clock) != Before {
			continue // Equal to the winner: already current
		}
		if rep, ok := c.tr.Replica(rr.id); ok {
			_ = rep.PutVersioned(ctx, key, winner)
		}
	}
}
