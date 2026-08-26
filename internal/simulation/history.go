package simulation

import (
	"sync"
	"sync/atomic"
)

// OpKind is the kind of recorded operation.
type OpKind int

const (
	OpPut OpKind = iota
	OpGet
)

// Event is one recorded operation. StartSeq and EndSeq come from a single monotonic counter
// ticked just before the operation begins and just after it returns, so they linearize record
// boundaries in real-time order: if one event's StartSeq is greater than another's EndSeq, the
// first operation began after the second returned. That is the happens-before relation the
// freshness check relies on.
type Event struct {
	Kind     OpKind
	Key      string
	Value    string // Put: the value written. Get: the value read (empty when not found).
	Found    bool   // Get: whether the key was present.
	OK       bool   // whether the operation succeeded (no error).
	Via      string // the coordinating node.
	StartSeq int64
	EndSeq   int64
}

// History is a thread-safe, ordered record of operations.
type History struct {
	seq    int64
	mu     sync.Mutex
	events []Event
}

// NewHistory returns an empty history.
func NewHistory() *History { return &History{} }

// tick returns the next monotonic sequence number.
func (h *History) tick() int64 { return atomic.AddInt64(&h.seq, 1) }

// record appends an event.
func (h *History) record(e Event) {
	h.mu.Lock()
	h.events = append(h.events, e)
	h.mu.Unlock()
}

// Events returns a copy of the recorded events.
func (h *History) Events() []Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Event(nil), h.events...)
}
