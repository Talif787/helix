package simulation

import (
	"fmt"
	"sort"
)

// Violation is a single invariant breach found in a history.
type Violation struct {
	Kind   string
	Detail string
}

func (v Violation) String() string { return fmt.Sprintf("%s: %s", v.Kind, v.Detail) }

// writtenValues returns, per key, every value any Put attempted, whether or not it succeeded: an
// errored Put may still have been partially applied and become readable, so its value is a
// legitimate thing for a read to return.
func writtenValues(events []Event) map[string]map[string]bool {
	m := map[string]map[string]bool{}
	for _, e := range events {
		if e.Kind != OpPut {
			continue
		}
		if m[e.Key] == nil {
			m[e.Key] = map[string]bool{}
		}
		m[e.Key][e.Value] = true
	}
	return m
}

// CheckNoFabrication verifies that every successful, present Get returned a value that some Put
// actually wrote. A read that returns a value no one ever wrote is a fabrication, the most basic
// safety breach.
func CheckNoFabrication(events []Event) []Violation {
	written := writtenValues(events)
	var vs []Violation
	for _, e := range events {
		if e.Kind != OpGet || !e.OK || !e.Found {
			continue
		}
		if !written[e.Key][e.Value] {
			vs = append(vs, Violation{
				Kind:   "fabrication",
				Detail: fmt.Sprintf("get(%s) returned %q, which was never written", e.Key, e.Value),
			})
		}
	}
	return vs
}

// CheckFreshness verifies read-after-write for a single-writer-per-key, sequential-write
// workload: a successful Get must return the most recent write that completed before the Get
// began, or a write that was still in flight when the Get began, never an older value, and never
// not-found once some write has completed.
//
// It assumes at most one write per key is in flight at a time. A workload that writes the same
// key from multiple goroutines breaks that assumption and must not be checked with this
// function.
func CheckFreshness(events []Event) []Violation {
	writesByKey := map[string][]Event{}
	for _, e := range events {
		if e.Kind == OpPut && e.OK {
			writesByKey[e.Key] = append(writesByKey[e.Key], e)
		}
	}
	for k := range writesByKey {
		ws := writesByKey[k]
		sort.Slice(ws, func(i, j int) bool { return ws[i].StartSeq < ws[j].StartSeq })
	}

	var vs []Violation
	for _, g := range events {
		if g.Kind != OpGet || !g.OK {
			continue
		}
		ws := writesByKey[g.Key]

		// iComplete: last write that fully completed before the read began.
		// iStarted: last write that had started before the read began (>= iComplete).
		iComplete, iStarted := -1, -1
		for idx, w := range ws {
			if w.EndSeq < g.StartSeq {
				iComplete = idx
			}
			if w.StartSeq < g.StartSeq {
				iStarted = idx
			}
		}

		if iComplete == -1 {
			// Nothing had completed before the read: not-found is fine, and so is any value from
			// a write that had started (was in flight) when the read began.
			if !g.Found {
				continue
			}
			if !valueInRange(ws, 0, iStarted, g.Value) {
				vs = append(vs, Violation{
					Kind:   "freshness",
					Detail: fmt.Sprintf("get(%s) returned %q but no write had completed and it matches no in-flight write", g.Key, g.Value),
				})
			}
			continue
		}

		// A write completed before the read: the read must find a value at least as fresh.
		if !g.Found {
			vs = append(vs, Violation{
				Kind:   "freshness",
				Detail: fmt.Sprintf("get(%s) returned not-found but write %q had already completed", g.Key, ws[iComplete].Value),
			})
			continue
		}
		if !valueInRange(ws, iComplete, iStarted, g.Value) {
			vs = append(vs, Violation{
				Kind:   "freshness",
				Detail: fmt.Sprintf("get(%s) returned stale %q; expected at least %q", g.Key, g.Value, ws[iComplete].Value),
			})
		}
	}
	return vs
}

// valueInRange reports whether value equals any write's value in the inclusive index range.
func valueInRange(ws []Event, lo, hi int, value string) bool {
	if lo < 0 {
		lo = 0
	}
	for m := lo; m <= hi && m < len(ws); m++ {
		if ws[m].Value == value {
			return true
		}
	}
	return false
}
