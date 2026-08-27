package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/talifpathan/helix/internal/storage"
)

// Coordinator replicates each key to the N nodes on its preference list and enforces
// tunable quorums: a write waits for W acknowledgements, a read waits for R responses and
// reconciles them. Reads repair replicas that answered with a stale version. When a
// preferred replica cannot be reached, the write is stored as a hint on a reachable
// fallback node (a sloppy quorum), so the write still succeeds and the intended replica
// receives the data once it recovers. There is no client-supplied causal context, so a
// write first does a best-effort read of the current versions to build a causal base, then
// increments the primary's clock entry.
type Coordinator struct {
	ring     *Ring
	tr       Transport
	n        int
	r        int
	w        int
	maxHints int // extra fallback nodes past the preference list that may hold hints
	now      func() int64
	log      *slog.Logger
	metrics  Metrics
	// reqTimeout bounds a single replica RPC so one hung or unreachable replica cannot consume
	// the caller's whole deadline. Zero disables the per-attempt bound.
	reqTimeout time.Duration
	// liveness, if set, reports whether a replica should be attempted. A node the local
	// membership view considers Dead is skipped (fast-failed) rather than dialed, so a
	// confirmed-dead replica costs nothing instead of a per-attempt timeout. Nil attempts all.
	liveness func(id string) bool
}

// NewCoordinator binds a ring and transport with replication factor n, quorums r and w, and
// maxHints fallback nodes for hinted handoff. A nil now uses the wall clock; a nil logger
// uses the default.
func NewCoordinator(ring *Ring, tr Transport, n, r, w, maxHints int, now func() int64, log *slog.Logger) *Coordinator {
	if now == nil {
		now = func() int64 { return time.Now().UnixNano() }
	}
	if log == nil {
		log = slog.Default()
	}
	if maxHints < 0 {
		maxHints = 0
	}
	return &Coordinator{ring: ring, tr: tr, n: n, r: r, w: w, maxHints: maxHints, now: now, log: log, metrics: nopMetrics{}}
}

// SetMetrics installs a metrics sink for per-request observations. Passing nil restores the
// no-op default. It is meant to be called once at wiring time, before serving.
func (c *Coordinator) SetMetrics(m Metrics) {
	if m == nil {
		m = nopMetrics{}
	}
	c.metrics = m
}

// SetRequestTimeout bounds how long the coordinator waits on a single replica RPC before
// treating it as failed, so one hung or unreachable replica cannot consume the caller's whole
// deadline. Zero (the default) disables the per-attempt bound. It is meant to be called once at
// wiring time, before serving.
func (c *Coordinator) SetRequestTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.reqTimeout = d
}

// attemptCtx derives the context for one replica RPC. With a request timeout set, it caps the
// attempt so a slow replica fails fast; otherwise it returns the parent unchanged. The returned
// cancel func must always be called.
func (c *Coordinator) attemptCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.reqTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.reqTimeout)
}

// SetLiveness installs a predicate that reports whether a replica should be attempted. A node the
// predicate reports as not attemptable (for example one the membership view considers Dead) is
// fast-failed instead of dialed, so it costs nothing rather than a per-attempt timeout. Passing
// nil (the default) attempts every node. It is meant to be called once at wiring time.
func (c *Coordinator) SetLiveness(fn func(id string) bool) {
	c.liveness = fn
}

// isAttemptable reports whether a replica should be contacted. With no liveness predicate set,
// every node is attemptable.
func (c *Coordinator) isAttemptable(id string) bool {
	if c.liveness == nil {
		return true
	}
	return c.liveness(id)
}

// resultOf classifies a request outcome for the result label. For reads, a missing key is a
// distinct "not_found" rather than an error.
func resultOf(err error, notFoundIsResult bool) string {
	switch {
	case err == nil:
		return "ok"
	case notFoundIsResult && errors.Is(err, storage.ErrNotFound):
		return "not_found"
	default:
		return "error"
	}
}

// Put replicates a write of value under key.
func (c *Coordinator) Put(ctx context.Context, key, value []byte) error {
	start := time.Now()
	err := c.write(ctx, key, value, false)
	c.metrics.ObserveRequest("put", resultOf(err, false), time.Since(start).Seconds())
	return err
}

// Delete replicates a tombstone for key.
func (c *Coordinator) Delete(ctx context.Context, key []byte) error {
	start := time.Now()
	err := c.write(ctx, key, nil, true)
	c.metrics.ObserveRequest("delete", resultOf(err, false), time.Since(start).Seconds())
	return err
}

func (c *Coordinator) write(ctx context.Context, key, value []byte, deleted bool) error {
	candidates := c.ring.LookupN(key, c.n+c.maxHints)
	if len(candidates) == 0 {
		return ErrNoNodes
	}
	nPref := c.n
	if nPref > len(candidates) {
		nPref = len(candidates)
	}
	pref := candidates[:nPref]
	fallbacks := candidates[nPref:]

	need := c.w
	if need > len(candidates) {
		need = len(candidates)
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

	// Write to every preferred replica and wait for all of them, so we know exactly which
	// ones failed and therefore need a hint.
	type presult struct {
		id  string
		err error
	}
	results := make(chan presult, len(pref))
	for _, id := range pref {
		go func(id string) {
			if !c.isAttemptable(id) {
				results <- presult{id, ErrNodeUnavailable}
				return
			}
			rctx, cancel := c.attemptCtx(ctx)
			defer cancel()
			rep, ok := c.tr.Replica(id)
			if !ok {
				results <- presult{id, ErrNodeUnavailable}
				return
			}
			results <- presult{id, rep.PutVersioned(rctx, key, vv)}
		}(id)
	}
	acks := 0
	var failed []string
	var firstErr error
	for i := 0; i < len(pref); i++ {
		res := <-results
		if res.err == nil {
			acks++
		} else {
			failed = append(failed, res.id)
			if firstErr == nil {
				firstErr = res.err
			}
		}
	}

	// For each preferred replica we could not reach, store the write as a hint on the next
	// reachable fallback node. A hint counts toward the write quorum (sloppy quorum) and is
	// replayed to the intended replica once it recovers.
	fi := 0
	for _, downNode := range failed {
		for fi < len(fallbacks) {
			fb := fallbacks[fi]
			fi++
			if !c.isAttemptable(fb) {
				continue
			}
			rep, ok := c.tr.Replica(fb)
			if !ok {
				continue
			}
			hctx, hcancel := c.attemptCtx(ctx)
			err := rep.PutHint(hctx, downNode, key, vv)
			hcancel()
			if err == nil {
				acks++
				c.log.Info("stored hint", "for", downNode, "on", fb)
				break
			}
		}
	}

	if acks >= need {
		return nil
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
			if !c.isAttemptable(id) {
				ch <- res{}
				return
			}
			rctx, cancel := c.attemptCtx(ctx)
			defer cancel()
			rep, ok := c.tr.Replica(id)
			if !ok {
				ch <- res{}
				return
			}
			vv, found, err := rep.GetVersioned(rctx, key)
			ch <- res{vv: vv, ok: err == nil && found}
		}(id)
	}
	// Wait for every preferred replica and merge the clocks of those that answered, to build a
	// causal base. Each attempt is bounded by the per-attempt timeout, so a hung replica fails
	// fast rather than consuming the caller's deadline; waiting for all of them (rather than
	// returning early) ensures no replica goroutine outlives this call, which would otherwise race
	// with a concurrent crash that closes a replica's engine.
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
// any stale replica, and returns the winning value. Reads are served from the preference
// list only; a value held solely as a hint on a fallback becomes readable once the intended
// replica recovers and the hint is delivered.
func (c *Coordinator) Get(ctx context.Context, key []byte) ([]byte, error) {
	start := time.Now()
	v, err := c.get(ctx, key)
	c.metrics.ObserveRequest("get", resultOf(err, true), time.Since(start).Seconds())
	return v, err
}

func (c *Coordinator) get(ctx context.Context, key []byte) ([]byte, error) {
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
			if !c.isAttemptable(id) {
				ch <- readResult{id: id, err: ErrNodeUnavailable}
				return
			}
			rctx, cancel := c.attemptCtx(ctx)
			defer cancel()
			rep, ok := c.tr.Replica(id)
			if !ok {
				ch <- readResult{id: id, err: ErrNodeUnavailable}
				return
			}
			vv, found, err := rep.GetVersioned(rctx, key)
			ch <- readResult{id: id, vv: vv, found: found, err: err}
		}(id)
	}

	responders := make([]readResult, 0, len(pref))
	ok := 0
	var firstErr error
	// Wait for every preferred replica so read repair can heal each stale one. Each attempt is
	// bounded by the per-attempt timeout, so a hung replica fails fast instead of consuming the
	// caller's whole deadline; it just does not contribute a response.
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
// held a strictly older version. Replicas that did not respond are healed later by hinted
// handoff or anti-entropy. Repairs are best-effort and their errors are ignored.
func (c *Coordinator) readRepair(ctx context.Context, key []byte, responders []readResult, winner VersionedValue) {
	for _, rr := range responders {
		if rr.err != nil {
			continue
		}
		if rr.found && rr.vv.Clock.Compare(winner.Clock) != Before {
			continue // equal to the winner: already current
		}
		if rep, ok := c.tr.Replica(rr.id); ok {
			_ = rep.PutVersioned(ctx, key, winner)
		}
	}
}
