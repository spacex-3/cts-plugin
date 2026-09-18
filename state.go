package main

import (
	"strings"
	"sync"
	"time"
)

type cacheKey struct {
	AuthID string
	Model  string
}

type cacheEntry struct {
	AuthID   string
	Model    string
	State    string
	Length   int
	StoredAt time.Time
	Source   string
}

type probeRecord struct {
	AuthID       string
	Model        string
	LastAttempt  time.Time
	LastError    string
	LastLength   int
	LastAccepted bool
	LastSource   string
}

type stateCache struct {
	mu           sync.Mutex
	entries      map[cacheKey]cacheEntry
	ttl          time.Duration
	targetLength int
	nowFunc      func() time.Time
}

func newStateCache(ttl time.Duration, targetLength int, nowFunc func() time.Time) *stateCache {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &stateCache{
		entries:      make(map[cacheKey]cacheEntry),
		ttl:          ttl,
		targetLength: targetLength,
		nowFunc:      nowFunc,
	}
}

func (c *stateCache) reconfigure(ttl time.Duration, targetLength int, nowFunc func() time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
	c.targetLength = targetLength
	if nowFunc != nil {
		c.nowFunc = nowFunc
	}
	for key, entry := range c.entries {
		if targetLength > 0 && entry.Length != targetLength {
			delete(c.entries, key)
		}
	}
	c.removeExpiredLocked(c.nowLocked())
}

func (c *stateCache) nowLocked() time.Time {
	if c.nowFunc == nil {
		return time.Now()
	}
	return c.nowFunc()
}

func (c *stateCache) putIfTarget(authID, model, state, source string) bool {
	_, accepted, _ := c.storeTarget(authID, model, state, source, true)
	return accepted
}

func (c *stateCache) storeTarget(authID, model, state, source string, refresh bool) (cacheEntry, bool, bool) {
	if c == nil {
		return cacheEntry{}, false, false
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	state = strings.TrimSpace(state)
	if authID == "" || model == "" || state == "" || len(state) != c.targetLength {
		return cacheEntry{}, false, false
	}
	key := cacheKey{AuthID: authID, Model: model}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	current, exists := c.entries[key]
	if exists && c.expiredLocked(current, now) {
		delete(c.entries, key)
		exists = false
	}
	if exists && current.State == state && !refresh {
		return current, true, false
	}
	entry := cacheEntry{
		AuthID:   authID,
		Model:    model,
		State:    state,
		Length:   len(state),
		StoredAt: now,
		Source:   source,
	}
	c.entries[key] = entry
	return entry, true, !exists || current.State != state || refresh
}

func (c *stateCache) lookup(authID, model string) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}
	key := cacheKey{AuthID: strings.TrimSpace(authID), Model: strings.TrimSpace(model)}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	if c.expiredLocked(entry, c.nowLocked()) {
		delete(c.entries, key)
		return cacheEntry{}, false
	}
	return entry, true
}

func (c *stateCache) snapshot() []cacheEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeExpiredLocked(c.nowLocked())
	out := make([]cacheEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	return out
}

func (c *stateCache) remaining(entry cacheEntry) time.Duration {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ttl <= 0 || entry.StoredAt.IsZero() {
		return 0
	}
	left := c.ttl - c.nowLocked().Sub(entry.StoredAt)
	if left < 0 {
		return 0
	}
	return left
}

func (c *stateCache) expiredLocked(entry cacheEntry, now time.Time) bool {
	return c.ttl > 0 && !entry.StoredAt.IsZero() && now.Sub(entry.StoredAt) >= c.ttl
}

func (c *stateCache) removeExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if c.expiredLocked(entry, now) {
			delete(c.entries, key)
		}
	}
}

func makeCacheKey(authID, model string) cacheKey {
	return cacheKey{AuthID: strings.TrimSpace(authID), Model: strings.TrimSpace(model)}
}
