package storage

import "testing"

func TestBlockCacheLRUEviction(t *testing.T) {
	c := NewBlockCache(30)
	if c == nil {
		t.Fatal("expected a cache for positive capacity")
	}

	c.Put(1, 0, make([]byte, 10))
	c.Put(1, 10, make([]byte, 10))
	c.Put(1, 20, make([]byte, 10)) // exactly at capacity

	// Touch offset 0 so it becomes most-recently-used.
	if _, ok := c.Get(1, 0); !ok {
		t.Fatal("offset 0 should be present before eviction")
	}

	// Inserting a fourth block forces eviction of the least-recently-used (offset 10).
	c.Put(1, 30, make([]byte, 10))

	if _, ok := c.Get(1, 10); ok {
		t.Fatal("offset 10 should have been evicted as least-recently-used")
	}
	if _, ok := c.Get(1, 0); !ok {
		t.Fatal("offset 0 was recently used and should remain")
	}
	if _, ok := c.Get(1, 30); !ok {
		t.Fatal("freshly inserted offset 30 should be present")
	}
}

func TestBlockCacheKeyedByFileNum(t *testing.T) {
	c := NewBlockCache(1 << 20)
	c.Put(1, 0, []byte{0xAA})
	c.Put(2, 0, []byte{0xBB})

	b1, ok := c.Get(1, 0)
	if !ok || len(b1) != 1 || b1[0] != 0xAA {
		t.Fatalf("file 1 offset 0 wrong: %v ok=%v", b1, ok)
	}
	b2, ok := c.Get(2, 0)
	if !ok || len(b2) != 1 || b2[0] != 0xBB {
		t.Fatalf("file 2 offset 0 wrong: %v ok=%v", b2, ok)
	}
}

func TestBlockCacheNilIsNoOp(t *testing.T) {
	if NewBlockCache(0) != nil {
		t.Fatal("non-positive capacity should yield a nil cache")
	}
	var c *BlockCache // nil
	c.Put(1, 0, []byte{1, 2, 3})
	if _, ok := c.Get(1, 0); ok {
		t.Fatal("a nil cache must never report a hit")
	}
}

func TestBlockCacheUpdateSameKey(t *testing.T) {
	c := NewBlockCache(100)
	c.Put(1, 0, make([]byte, 10))
	c.Put(1, 0, make([]byte, 10)) // same key again should not double-count size
	if c.size != 10 {
		t.Fatalf("expected cached size 10 after repeat put, got %d", c.size)
	}
}
