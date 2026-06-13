package proxy

import (
	"container/list"
	"sync"
	"time"
)

// cacheEntry is one cached upstream response body.
type cacheEntry struct {
	key         string
	data        []byte
	contentType string
	expires     time.Time
}

// lruCache is a byte-bounded LRU cache with per-entry TTL.
// Safe for concurrent use.
type lruCache struct {
	mu       sync.Mutex
	maxBytes int64
	maxItem  int64
	size     int64
	ll       *list.List // front = most recently used
	items    map[string]*list.Element
	now      func() time.Time
}

func newLRUCache(maxBytes, maxItem int64) *lruCache {
	return &lruCache{
		maxBytes: maxBytes,
		maxItem:  maxItem,
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		now:      time.Now,
	}
}

// get returns the cached body and content type for key, if present and fresh.
func (c *lruCache) get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	ent := el.Value.(*cacheEntry)
	if c.now().After(ent.expires) {
		c.removeElement(el)
		return nil, "", false
	}
	c.ll.MoveToFront(el)
	return ent.data, ent.contentType, true
}

// set stores body under key for ttl. Bodies larger than the per-item limit
// are silently skipped — they would evict the whole cache for one segment.
func (c *lruCache) set(key string, data []byte, contentType string, ttl time.Duration) {
	if int64(len(data)) > c.maxItem || int64(len(data)) > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.removeElement(el)
	}
	ent := &cacheEntry{key: key, data: data, contentType: contentType, expires: c.now().Add(ttl)}
	c.items[key] = c.ll.PushFront(ent)
	c.size += int64(len(data))

	for c.size > c.maxBytes {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.removeElement(back)
	}
}

// removeElement must be called with c.mu held.
func (c *lruCache) removeElement(el *list.Element) {
	ent := el.Value.(*cacheEntry)
	c.ll.Remove(el)
	delete(c.items, ent.key)
	c.size -= int64(len(ent.data))
}
