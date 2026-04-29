package cache

import (
	"sync"
	"time"
)

// URLCache is the port consumed by the checkout resolver to short-circuit
// hot reloads / retries on the same order_id.
type URLCache interface {
	Get(orderID string) (string, bool)
	Put(orderID, url string)
	Invalidate(orderID string)
}

// urlCache is an in-process TTL cache. Sized for ~10k hot orders per pod
// with a default 5s TTL. Eviction on TTL expiry is lazy (on Get) plus
// opportunistic on Put.
type urlCache struct {
	mu      sync.RWMutex
	data    map[string]urlEntry
	maxSize int
	ttl     time.Duration
}

type urlEntry struct {
	url       string
	expiresAt time.Time
}

func NewURLCache(maxSize int, ttl time.Duration) URLCache {
	return &urlCache{data: make(map[string]urlEntry, maxSize), maxSize: maxSize, ttl: ttl}
}

func (c *urlCache) Get(orderID string) (string, bool) {
	c.mu.RLock()
	e, ok := c.data[orderID]
	c.mu.RUnlock()
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.url, true
}

func (c *urlCache) Put(orderID, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.data) >= c.maxSize {
		// Cheap eviction: drop one expired entry; if none, drop one arbitrary.
		now := time.Now()
		for k, e := range c.data {
			if now.After(e.expiresAt) {
				delete(c.data, k)
				break
			}
		}
		if len(c.data) >= c.maxSize {
			for k := range c.data {
				delete(c.data, k)
				break
			}
		}
	}
	c.data[orderID] = urlEntry{url: url, expiresAt: time.Now().Add(c.ttl)}
}

func (c *urlCache) Invalidate(orderID string) {
	c.mu.Lock()
	delete(c.data, orderID)
	c.mu.Unlock()
}
