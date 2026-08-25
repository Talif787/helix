package cluster

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
)

// merkleLeaves is the fixed number of leaf buckets in every Merkle tree. It is a power of
// two so the tree is a complete binary tree, and it is a constant so any two nodes build
// comparable trees. More leaves localize a difference to a smaller key set at the cost of a
// larger tree.
const merkleLeaves = 256

// MerkleTree summarizes a set of keyed versions as a tree of hashes. Two nodes compare
// their trees to find, without exchanging any data, which leaf buckets differ; only those
// buckets' entries then need to be exchanged to reconcile. Nodes are stored in a heap
// array: node i has children 2i and 2i+1, the root is at index 1, and the leaves occupy
// indices merkleLeaves..2*merkleLeaves-1.
type MerkleTree struct {
	nodes [][32]byte
}

// bucketOf maps a key to a leaf bucket using the same well-mixed hash the ring uses.
func bucketOf(key []byte) int {
	return int(hash64(key) % uint64(merkleLeaves))
}

// BuildMerkleTree constructs a tree over the given entries. Entries are grouped into leaf
// buckets by key; each leaf hashes its entries (sorted by key) together with a hash of each
// entry's encoded version, so any difference in value, clock, timestamp, or tombstone flag
// changes the leaf and propagates to the root.
func BuildMerkleTree(entries []KeyVersion) (*MerkleTree, error) {
	buckets := make([][]KeyVersion, merkleLeaves)
	for _, kv := range entries {
		b := bucketOf(kv.Key)
		buckets[b] = append(buckets[b], kv)
	}

	nodes := make([][32]byte, 2*merkleLeaves)
	for i := 0; i < merkleLeaves; i++ {
		bucket := buckets[i]
		sort.Slice(bucket, func(a, b int) bool { return string(bucket[a].Key) < string(bucket[b].Key) })
		h := sha256.New()
		var lenbuf [4]byte
		for _, kv := range bucket {
			raw, err := encodeVersioned(kv.Value)
			if err != nil {
				return nil, err
			}
			binary.BigEndian.PutUint32(lenbuf[:], uint32(len(kv.Key)))
			h.Write(lenbuf[:])
			h.Write(kv.Key)
			vh := sha256.Sum256(raw)
			h.Write(vh[:])
		}
		copy(nodes[merkleLeaves+i][:], h.Sum(nil))
	}
	for i := merkleLeaves - 1; i >= 1; i-- {
		nodes[i] = sha256.Sum256(append(append([]byte{}, nodes[2*i][:]...), nodes[2*i+1][:]...))
	}
	return &MerkleTree{nodes: nodes}, nil
}

// Root returns the tree's root hash. Equal roots mean the two trees are identical.
func (t *MerkleTree) Root() [32]byte {
	if t == nil || len(t.nodes) < 2 {
		return [32]byte{}
	}
	return t.nodes[1]
}

// Serialize flattens the tree's node hashes into a byte slice for transport. The layout is
// fixed (2*merkleLeaves hashes of 32 bytes), so DeserializeMerkleTree can rebuild it.
func (t *MerkleTree) Serialize() []byte {
	out := make([]byte, 0, len(t.nodes)*32)
	for i := range t.nodes {
		out = append(out, t.nodes[i][:]...)
	}
	return out
}

// DeserializeMerkleTree rebuilds a tree from the bytes produced by Serialize, validating the
// length so a comparison never runs against a malformed tree.
func DeserializeMerkleTree(b []byte) (*MerkleTree, error) {
	want := 2 * merkleLeaves * 32
	if len(b) != want {
		return nil, fmt.Errorf("merkle: serialized length %d, want %d", len(b), want)
	}
	nodes := make([][32]byte, 2*merkleLeaves)
	for i := range nodes {
		copy(nodes[i][:], b[i*32:(i+1)*32])
	}
	return &MerkleTree{nodes: nodes}, nil
}

// Diff returns the leaf bucket indices where this tree and other differ, walking down from
// the root and skipping subtrees whose hashes match. The two trees must have the same shape
// (they always do, since merkleLeaves is fixed).
func (t *MerkleTree) Diff(other *MerkleTree) []int {
	if t == nil || other == nil {
		return nil
	}
	var out []int
	stack := []int{1}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if t.nodes[i] == other.nodes[i] {
			continue
		}
		if i >= merkleLeaves {
			out = append(out, i-merkleLeaves)
			continue
		}
		stack = append(stack, 2*i, 2*i+1)
	}
	sort.Ints(out)
	return out
}
