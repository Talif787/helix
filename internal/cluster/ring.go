package cluster

import (
	"hash/fnv"
	"sort"
	"strconv"
	"sync"
)

// DefaultVNodes is the number of virtual nodes assigned to each physical node when the
// caller does not specify one. More virtual nodes smooth out the key distribution at the
// cost of a larger token table.
const DefaultVNodes = 128

// token is one point on the ring: a hash value owned by a physical node. Each physical
// node contributes vnodes tokens, so ownership of the hash space is finely interleaved
// and removing a node reassigns only its own share of keys.
type token struct {
	hash uint64
	node string
}

// Ring maps keys to nodes with consistent hashing over virtual nodes. It is safe for
// concurrent use: reads take a read lock, membership changes take the write lock and
// rebuild the sorted token table.
type Ring struct {
	mu     sync.RWMutex
	vnodes int
	nodes  map[string]struct{}
	tokens []token // sorted by (hash, node); rebuilt on membership change
}

// NewRing returns an empty ring assigning vnodes virtual nodes per physical node. A
// vnodes value below 1 falls back to DefaultVNodes.
func NewRing(vnodes int) *Ring {
	if vnodes < 1 {
		vnodes = DefaultVNodes
	}
	return &Ring{vnodes: vnodes, nodes: make(map[string]struct{})}
}

func hash64(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return mix64(h.Sum64())
}

// mix64 is the splitmix64 finalizer. FNV-1a alone avalanches poorly, so structured or
// similar inputs (keys sharing a prefix, short node names) land in clustered ring
// positions and skew ownership badly. Running the FNV output through this finalizer
// decorrelates nearby inputs and spreads tokens and keys evenly across the ring. uint64
// arithmetic wraps, which is exactly the modular behavior the mix relies on.
func mix64(z uint64) uint64 {
	z += 0x9e3779b97f4a7c15
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// tokenHash derives a stable ring position for a node's i-th virtual node. It is a pure
// function of (nodeID, i), so every ring built from the same members is identical
// regardless of the order members were added.
func tokenHash(nodeID string, i int) uint64 {
	return hash64([]byte(nodeID + "#" + strconv.Itoa(i)))
}

// Add inserts a node and its virtual nodes. Adding a node already present is a no-op.
func (r *Ring) Add(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[nodeID]; ok {
		return
	}
	r.nodes[nodeID] = struct{}{}
	r.rebuildLocked()
}

// Remove deletes a node and its virtual nodes. Removing an absent node is a no-op.
func (r *Ring) Remove(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[nodeID]; !ok {
		return
	}
	delete(r.nodes, nodeID)
	r.rebuildLocked()
}

func (r *Ring) rebuildLocked() {
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	tokens := make([]token, 0, len(ids)*r.vnodes)
	for _, id := range ids {
		for i := 0; i < r.vnodes; i++ {
			tokens = append(tokens, token{hash: tokenHash(id, i), node: id})
		}
	}
	sort.Slice(tokens, func(i, j int) bool {
		if tokens[i].hash != tokens[j].hash {
			return tokens[i].hash < tokens[j].hash
		}
		return tokens[i].node < tokens[j].node
	})
	r.tokens = tokens
}

// searchLocked returns the index of the first token clockwise from the key's hash,
// wrapping to the start of the ring. The caller holds at least the read lock and has
// checked that the ring is non-empty.
func (r *Ring) searchLocked(key []byte) int {
	h := hash64(key)
	idx := sort.Search(len(r.tokens), func(i int) bool { return r.tokens[i].hash >= h })
	if idx == len(r.tokens) {
		idx = 0
	}
	return idx
}

// Lookup returns the node that owns key, or ok=false if the ring is empty.
func (r *Ring) Lookup(key []byte) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tokens) == 0 {
		return "", false
	}
	return r.tokens[r.searchLocked(key)].node, true
}

// LookupN returns up to n distinct nodes for key, primary first, walking clockwise. It is
// the replication preference list: a later phase writes a key to these nodes. Asking for
// more nodes than exist returns all of them.
func (r *Ring) LookupN(key []byte, n int) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.tokens) == 0 || n <= 0 {
		return nil
	}
	if n > len(r.nodes) {
		n = len(r.nodes)
	}
	start := r.searchLocked(key)
	out := make([]string, 0, n)
	seen := make(map[string]struct{}, n)
	for count := 0; count < len(r.tokens) && len(out) < n; count++ {
		node := r.tokens[(start+count)%len(r.tokens)].node
		if _, ok := seen[node]; ok {
			continue
		}
		seen[node] = struct{}{}
		out = append(out, node)
	}
	return out
}

// Nodes returns the current members in sorted order.
func (r *Ring) Nodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Size returns the number of physical nodes on the ring.
func (r *Ring) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.nodes)
}
