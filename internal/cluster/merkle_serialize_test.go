package cluster

import "testing"

func TestMerkleSerializeRoundTrip(t *testing.T) {
	entries := []KeyVersion{
		kv("a", "1", VectorClock{"p": 1}, 10),
		kv("b", "2", VectorClock{"p": 2}, 11),
		kv("c", "3", VectorClock{"p": 1}, 12),
	}
	tree, err := BuildMerkleTree(entries)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	blob := tree.Serialize()
	if want := 2 * merkleLeaves * 32; len(blob) != want {
		t.Fatalf("serialized length %d, want %d", len(blob), want)
	}

	back, err := DeserializeMerkleTree(blob)
	if err != nil {
		t.Fatalf("deserialize: %v", err)
	}
	if back.Root() != tree.Root() {
		t.Fatal("root changed across serialize round trip")
	}
	if d := back.Diff(tree); len(d) != 0 {
		t.Fatalf("round-tripped tree should match original, differing buckets %v", d)
	}
}

func TestDeserializeMerkleTreeRejectsBadLength(t *testing.T) {
	if _, err := DeserializeMerkleTree([]byte{1, 2, 3}); err == nil {
		t.Fatal("expected an error for a too-short blob")
	}
}
