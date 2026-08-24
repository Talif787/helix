package storage

import (
	"container/list"
	"sync"
)

type blockCacheKey struct {
	fileNum uint64
	offset  uint64
}

type blockCacheEntry struct {
	key   blockCacheKey
	value []byte
}

// BlockCache is a thread-safe LRU cache of SSTable data blocks, bounded by total
// bytes. Cached blocks are treated as immutable and may be shared by many readers.
// A nil *BlockCache is a valid no-op cache, so callers need not check for nil.
type BlockCache struct {
	mu       sync.Mutex
	capacity int64
	size     int64
	ll       *list.List
	items    map[blockCacheKey]*list.Element
}

// NewBlockCache returns a cache bounded by capacityBytes, or nil when capacityBytes
// is not positive (caching disabled).
func NewBlockCache(capacityBytes int64) *BlockCache {
	if capacityBytes <= 0 {
		return nil
	}
	return &BlockCache{
		capacity: capacityBytes,
		ll:       list.New(),
		items:    make(map[blockCacheKey]*list.Element),
	}
}

// Get returns the cached block for (fileNum, offset), marking it most-recently-used.
func (c *BlockCache) Get(fileNum, offset uint64) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[blockCacheKey{fileNum, offset}]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*blockCacheEntry).value, true
	}
	return nil, false
}

// Put inserts a block, evicting least-recently-used entries to stay within capacity.
func (c *BlockCache) Put(fileNum, offset uint64, block []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := blockCacheKey{fileNum, offset}
	if el, ok := c.items[key]; ok {
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&blockCacheEntry{key: key, value: block})
	c.items[key] = el
	c.size += int64(len(block))

	for c.size > c.capacity {
		back := c.ll.Back()
		if back == nil {
			break
		}
		ent := back.Value.(*blockCacheEntry)
		c.ll.Remove(back)
		delete(c.items, ent.key)
		c.size -= int64(len(ent.value))
	}
}
