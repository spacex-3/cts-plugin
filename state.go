package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
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
	IssuedAt time.Time `json:",omitempty"`
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
	mu          sync.Mutex
	entries     map[cacheKey]cacheEntry
	ttl         time.Duration
	useIssuedAt bool
	nowFunc     func() time.Time
}

func newStateCache(ttl time.Duration, nowFunc func() time.Time) *stateCache {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &stateCache{
		entries: make(map[cacheKey]cacheEntry),
		ttl:     ttl,
		nowFunc: nowFunc,
	}
}

func (c *stateCache) reconfigure(ttl time.Duration, nowFunc func() time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ttl = ttl
	if nowFunc != nil {
		c.nowFunc = nowFunc
	}
	c.removeExpiredLocked(c.nowLocked())
}

func (c *stateCache) nowLocked() time.Time {
	if c.nowFunc == nil {
		return time.Now()
	}
	return c.nowFunc()
}

func (c *stateCache) putState(authID, model, state, source string) bool {
	_, stored, _ := c.store(authID, model, state, source, true)
	return stored
}

// store admits every state the upstream hands back. Length and Fernet block
// count are recorded for the status page, never used to refuse a ticket: the
// shape upstream returns is a symptom of that turn (whether it produced
// reasoning), it is not a routing or quality label, and refusing a shape we did
// not expect simply throws away a working ticket.
func (c *stateCache) store(authID, model, state, source string, refresh bool) (cacheEntry, bool, bool) {
	if c == nil {
		return cacheEntry{}, false, false
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	state = strings.TrimSpace(state)
	if authID == "" || model == "" || state == "" {
		return cacheEntry{}, false, false
	}
	key := cacheKey{AuthID: authID, Model: model}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	var issuedAt time.Time
	if c.useIssuedAt {
		var err error
		issuedAt, err = parseStateIssuedAt(state)
		if err != nil || issuedAt.After(now.Add(time.Minute)) || (c.ttl > 0 && !now.Before(issuedAt.Add(c.ttl))) {
			return cacheEntry{}, false, false
		}
	}
	current, exists := c.entries[key]
	if exists && c.expiredLocked(current, now) {
		delete(c.entries, key)
		exists = false
	}
	if exists && c.useIssuedAt && current.IssuedAt.After(issuedAt) {
		return current, false, false
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
		IssuedAt: issuedAt,
		Source:   source,
	}
	c.entries[key] = entry
	return entry, true, !exists || current.State != state || refresh
}

func (c *stateCache) putManual(authID, model, state string) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}
	authID = strings.TrimSpace(authID)
	model = strings.TrimSpace(model)
	state = strings.TrimSpace(state)
	if authID == "" || model == "" || state == "" {
		return cacheEntry{}, false
	}
	key := cacheKey{AuthID: authID, Model: model}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := cacheEntry{
		AuthID:   authID,
		Model:    model,
		State:    state,
		Length:   len(state),
		StoredAt: c.nowLocked(),
		Source:   "manual",
	}
	if c.useIssuedAt {
		issuedAt, err := parseStateIssuedAt(state)
		if err != nil || issuedAt.After(c.nowLocked().Add(time.Minute)) || (c.ttl > 0 && !c.nowLocked().Before(issuedAt.Add(c.ttl))) {
			return cacheEntry{}, false
		}
		entry.IssuedAt = issuedAt
	}
	c.entries[key] = entry
	return entry, true
}

func (c *stateCache) lookup(authID, model string) (cacheEntry, bool) {
	if c == nil {
		return cacheEntry{}, false
	}
	key := cacheKey{AuthID: strings.TrimSpace(authID), Model: strings.TrimSpace(model)}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || (c.useIssuedAt && c.expiredLocked(entry, c.nowLocked())) {
		return cacheEntry{}, false
	}
	return entry, true
}

// invalidate drops one cached entry on demand. The upstream refusing the ticket
// it was just handed is a far better expiry signal than the configured TTL, and
// it arrives on the very response that proves the ticket is dead.
func (c *stateCache) invalidate(authID, model string) bool {
	if c == nil {
		return false
	}
	key := makeCacheKey(authID, model)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[key]; !ok {
		return false
	}
	delete(c.entries, key)
	return true
}

func (c *stateCache) snapshot() []cacheEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]cacheEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	return out
}

func (c *stateCache) expiredSnapshot() []cacheEntry {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	out := make([]cacheEntry, 0)
	for _, entry := range c.entries {
		if c.expiredLocked(entry, now) {
			out = append(out, entry)
		}
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
	left := c.ttl - c.nowLocked().Sub(entry.freshnessTime())
	if left < 0 {
		return 0
	}
	return left
}

func (c *stateCache) expiredLocked(entry cacheEntry, now time.Time) bool {
	return c.ttl > 0 && !entry.StoredAt.IsZero() && now.Sub(entry.freshnessTime()) >= c.ttl
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

// Fernet's timestamp is public envelope metadata, not an authenticated claim:
// this parser neither decrypts the state nor verifies its HMAC.
func parseStateIssuedAt(state string) (time.Time, error) {
	issuedAt, _, errParse := parseStateEnvelope(state)
	return issuedAt, errParse
}

func parseStateBlocks(state string) (int, bool) {
	_, blocks, errParse := parseStateEnvelope(state)
	return blocks, errParse == nil
}

func stateBlocksText(state string) string {
	if blocks, okBlocks := parseStateBlocks(state); okBlocks {
		return strconv.Itoa(blocks)
	}
	return "unknown"
}

func parseStateEnvelope(state string) (time.Time, int, error) {
	raw, err := base64.URLEncoding.DecodeString(strings.TrimSpace(state))
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(state))
	}
	// Version + timestamp + IV + at least one AES block + HMAC.
	if err != nil || len(raw) < 73 || raw[0] != 0x80 || (len(raw)-57)%16 != 0 {
		return time.Time{}, 0, fmt.Errorf("invalid Fernet envelope")
	}
	stamp := binary.BigEndian.Uint64(raw[1:9])
	if stamp == 0 || stamp > 1<<63-1 {
		return time.Time{}, 0, fmt.Errorf("invalid Fernet timestamp")
	}
	return time.Unix(int64(stamp), 0).UTC(), (len(raw) - 57) / 16, nil
}

func (e cacheEntry) freshnessTime() time.Time {
	if !e.IssuedAt.IsZero() {
		return e.IssuedAt
	}
	return e.StoredAt
}

func (c *stateCache) configureIssuedAt(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.useIssuedAt = enabled
	for key, entry := range c.entries {
		entry.IssuedAt = time.Time{}
		if enabled {
			issuedAt, err := parseStateIssuedAt(entry.State)
			if err != nil || issuedAt.After(c.nowLocked().Add(time.Minute)) {
				delete(c.entries, key)
				continue
			}
			entry.IssuedAt = issuedAt
		}
		c.entries[key] = entry
	}
}

// Restore the original observation age, including manual entries. Do not route
// restoration through the methods that timestamp a new observation.
func (c *stateCache) restore(entry cacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.nowLocked()
	if entry.AuthID == "" || entry.Model == "" || entry.State == "" || entry.StoredAt.IsZero() || entry.StoredAt.After(now) || c.expiredLocked(entry, now) {
		return
	}
	entry.Length = len(entry.State)
	entry.IssuedAt = time.Time{}
	if c.useIssuedAt {
		issuedAt, err := parseStateIssuedAt(entry.State)
		if err != nil || issuedAt.After(c.nowLocked().Add(time.Minute)) {
			return
		}
		entry.IssuedAt = issuedAt
	}
	c.entries[makeCacheKey(entry.AuthID, entry.Model)] = entry
}
