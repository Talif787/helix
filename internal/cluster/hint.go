package cluster

import (
	"sort"
	"sync"
)

// hintStore buffers writes that could not reach their intended replica, keyed by that
// replica's node id. When the intended node becomes reachable, the buffered writes are
// replayed to it and removed. It is safe for concurrent use. Multiple hints for the same
// key and intended node are reconciled, so only the causally latest survives.
type hintStore struct {
	mu    sync.Mutex
	hints map[string]map[string]VersionedValue // intended node -> key -> value
}

func newHintStore() *hintStore {
	return &hintStore{hints: make(map[string]map[string]VersionedValue)}
}

// add buffers a hint of vv for key destined for intended.
func (h *hintStore) add(intended string, key []byte, vv VersionedValue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.hints[intended]
	if m == nil {
		m = make(map[string]VersionedValue)
		h.hints[intended] = m
	}
	if cur, ok := m[string(key)]; ok {
		vv = Reconcile(vv, cur)
	}
	m[string(key)] = vv
}

// intendedNodes returns the node ids that currently have buffered hints, sorted.
func (h *hintStore) intendedNodes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.hints))
	for id := range h.hints {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// pendingFor returns a copy of the hints buffered for intended, sorted by key.
func (h *hintStore) pendingFor(intended string) []KeyVersion {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.hints[intended]
	out := make([]KeyVersion, 0, len(m))
	for k, vv := range m {
		out = append(out, KeyVersion{Key: []byte(k), Value: vv})
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i].Key) < string(out[j].Key) })
	return out
}

// remove deletes the given keys from intended's buffer, dropping the intended entry when
// it becomes empty.
func (h *hintStore) remove(intended string, keys [][]byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.hints[intended]
	if m == nil {
		return
	}
	for _, k := range keys {
		delete(m, string(k))
	}
	if len(m) == 0 {
		delete(h.hints, intended)
	}
}

// count returns the total number of buffered hints across all intended nodes.
func (h *hintStore) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.hints {
		n += len(m)
	}
	return n
}
