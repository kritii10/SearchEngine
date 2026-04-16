package cache

import (
	"context"
	"strings"
	"sync"
	"time"
)

type MemoryCache struct {
	mu      sync.RWMutex
	entries map[string]memoryEntry
}

type memoryEntry struct {
	value     string
	expiresAt time.Time
}

func NewMemoryCache() *MemoryCache {
	return &MemoryCache{
		entries: make(map[string]memoryEntry),
	}
}

func (c *MemoryCache) Get(_ context.Context, key string) (string, bool, error) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()
	if !ok {
		return "", false, nil
	}

	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, key)
		c.mu.Unlock()
		return "", false, nil
	}

	return entry.value, true, nil
}

func (c *MemoryCache) Set(_ context.Context, key, value string, ttl time.Duration) error {
	entry := memoryEntry{value: value}
	if ttl > 0 {
		entry.expiresAt = time.Now().Add(ttl)
	}

	c.mu.Lock()
	c.entries[key] = entry
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	return nil
}

func (c *MemoryCache) DeleteByPrefix(_ context.Context, prefix string) error {
	c.mu.Lock()
	for key := range c.entries {
		if strings.HasPrefix(key, prefix) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
	return nil
}
