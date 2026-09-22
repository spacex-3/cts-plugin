package main

import (
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// The Codex backend hands out two account-level routing cookies next to the
// turn state: __cflb picks the Cloudflare data centre and __oailb keeps the
// session pinned. They are what makes a 292 state keep working, so they travel
// with the state. Every other cookie (session tokens, analytics, consent) is
// deliberately ignored: the jar must never hold credentials the plugin has no
// business forwarding.
const (
	cookieNameCFLB  = "__cflb"
	cookieNameOAILB = "__oailb"
)

var trackedCookieNames = []string{cookieNameCFLB, cookieNameOAILB}

type cookieEntry struct {
	AuthID     string    `json:"auth_id"`
	Name       string    `json:"name"`
	Value      string    `json:"value,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	Source     string    `json:"source,omitempty"`
	Route      string    `json:"route,omitempty"`
}

// capturedCookie is one whitelisted Set-Cookie value read from an upstream
// response, before it reaches the jar.
type capturedCookie struct {
	Name    string
	Value   string
	Expires time.Time
}

func isTrackedCookie(name string) bool {
	name = strings.TrimSpace(name)
	for _, tracked := range trackedCookieNames {
		if strings.EqualFold(name, tracked) {
			return true
		}
	}
	return false
}

// parseTrackedCookies keeps only the routing cookies this plugin understands. A
// response may carry several Set-Cookie lines; anything outside the whitelist is
// dropped before it can reach the jar, the status page or the logs.
func parseTrackedCookies(header http.Header, now time.Time) []capturedCookie {
	if len(header) == 0 {
		return nil
	}
	response := &http.Response{Header: header}
	var out []capturedCookie
	for _, cookie := range response.Cookies() {
		if cookie == nil || !isTrackedCookie(cookie.Name) {
			continue
		}
		value := strings.TrimSpace(cookie.Value)
		if value == "" {
			continue
		}
		item := capturedCookie{Name: strings.ToLower(strings.TrimSpace(cookie.Name)), Value: value}
		switch {
		case cookie.MaxAge < 0:
			// Upstream is clearing the cookie; a tombstone is not a credential.
			continue
		case cookie.MaxAge > 0:
			item.Expires = now.Add(time.Duration(cookie.MaxAge) * time.Second)
		case !cookie.Expires.IsZero():
			item.Expires = cookie.Expires
		}
		out = append(out, item)
	}
	return out
}

// cookieJar stores one live value per (account, cookie name). The routing
// cookies are account-level, so the jar is keyed by account only: every model on
// the same account shares the same pool.
type cookieJar struct {
	mu      sync.Mutex
	entries map[string]map[string]cookieEntry
	ttl     time.Duration
	nowFunc func() time.Time
}

func newCookieJar(ttl time.Duration, nowFunc func() time.Time) *cookieJar {
	if nowFunc == nil {
		nowFunc = time.Now
	}
	return &cookieJar{
		entries: make(map[string]map[string]cookieEntry),
		ttl:     ttl,
		nowFunc: nowFunc,
	}
}

func (j *cookieJar) nowLocked() time.Time {
	if j == nil || j.nowFunc == nil {
		return time.Now()
	}
	return j.nowFunc()
}

func (j *cookieJar) configure(ttl time.Duration, nowFunc func() time.Time) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if ttl > 0 {
		j.ttl = ttl
	}
	if nowFunc != nil {
		j.nowFunc = nowFunc
	}
	j.pruneLocked()
}

// observe stores freshly captured cookies and returns how many values actually
// changed. Re-sending an identical value does not extend the age: the age of an
// entry measures how long we have been relying on this exact value, which is the
// number that tells us whether the cookie or the ticket died first.
func (j *cookieJar) observe(authID, source, route string, cookies []capturedCookie) int {
	if j == nil || len(cookies) == 0 {
		return 0
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.nowLocked()
	bucket := j.entries[authID]
	if bucket == nil {
		bucket = make(map[string]cookieEntry)
		j.entries[authID] = bucket
	}
	updated := 0
	for _, cookie := range cookies {
		if !isTrackedCookie(cookie.Name) || strings.TrimSpace(cookie.Value) == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(cookie.Name))
		if !cookie.Expires.IsZero() && !now.Before(cookie.Expires) {
			continue
		}
		current, exists := bucket[name]
		if exists && current.Value == cookie.Value {
			current.Source = source
			current.Route = route
			if !cookie.Expires.IsZero() {
				current.ExpiresAt = cookie.Expires
			}
			bucket[name] = current
			continue
		}
		bucket[name] = cookieEntry{
			AuthID:     authID,
			Name:       name,
			Value:      cookie.Value,
			CapturedAt: now,
			ExpiresAt:  cookie.Expires,
			Source:     source,
			Route:      route,
		}
		updated++
	}
	j.pruneLocked()
	return updated
}

func (j *cookieJar) expiredLocked(entry cookieEntry, now time.Time) bool {
	if j == nil || j.ttl <= 0 || entry.CapturedAt.IsZero() {
		return true
	}
	deadline := entry.CapturedAt.Add(j.ttl)
	if !entry.ExpiresAt.IsZero() && entry.ExpiresAt.Before(deadline) {
		deadline = entry.ExpiresAt
	}
	return !now.Before(deadline)
}

func (j *cookieJar) pruneLocked() {
	now := j.nowLocked()
	for authID, bucket := range j.entries {
		for name, entry := range bucket {
			if j.expiredLocked(entry, now) {
				delete(bucket, name)
			}
		}
		if len(bucket) == 0 {
			delete(j.entries, authID)
		}
	}
}

// live returns the unexpired cookies of one account, ordered by name.
func (j *cookieJar) live(authID string) []cookieEntry {
	if j == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.nowLocked()
	bucket := j.entries[authID]
	out := make([]cookieEntry, 0, len(bucket))
	for _, entry := range bucket {
		if j.expiredLocked(entry, now) {
			continue
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

func (j *cookieJar) snapshot() []cookieEntry {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]cookieEntry, 0)
	for _, bucket := range j.entries {
		for _, entry := range bucket {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		if out[i].AuthID == out[k].AuthID {
			return out[i].Name < out[k].Name
		}
		return out[i].AuthID < out[k].AuthID
	})
	return out
}

func (j *cookieJar) restore(entries []cookieEntry) {
	if j == nil || len(entries) == 0 {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := j.nowLocked()
	for _, entry := range entries {
		if strings.TrimSpace(entry.AuthID) == "" || !isTrackedCookie(entry.Name) || strings.TrimSpace(entry.Value) == "" {
			continue
		}
		if entry.CapturedAt.IsZero() || entry.CapturedAt.After(now) || j.expiredLocked(entry, now) {
			continue
		}
		bucket := j.entries[entry.AuthID]
		if bucket == nil {
			bucket = make(map[string]cookieEntry)
			j.entries[entry.AuthID] = bucket
		}
		name := strings.ToLower(strings.TrimSpace(entry.Name))
		if current, exists := bucket[name]; exists && current.CapturedAt.After(entry.CapturedAt) {
			continue
		}
		bucket[name] = entry
	}
}

// mergeCookieHeader keeps every cookie the host already sends and replaces only
// the tracked routing cookies: the host applies a returned header as a replace,
// so returning a bare value would drop the rest of the Cookie header.
func mergeCookieHeader(existing string, injected []cookieEntry) string {
	if len(injected) == 0 {
		return strings.TrimSpace(existing)
	}
	kept := make([]string, 0, 4)
	for _, part := range strings.Split(existing, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name := part
		if index := strings.Index(part, "="); index >= 0 {
			name = part[:index]
		}
		if isTrackedCookie(name) {
			continue
		}
		kept = append(kept, part)
	}
	for _, entry := range injected {
		kept = append(kept, entry.Name+"="+entry.Value)
	}
	return strings.Join(kept, "; ")
}

// comboStats measures how long one cached ticket actually survived once it was
// in use, so the TTL stops being a guess and "the cookie died first" becomes a
// number instead of an opinion.
type comboStats struct {
	AuthID            string    `json:"auth_id"`
	Model             string    `json:"model"`
	StartedAt         time.Time `json:"started_at,omitempty"`
	LifetimeSamples   int64     `json:"lifetime_samples,omitempty"`
	TotalLifetime     float64   `json:"total_lifetime_seconds,omitempty"`
	LastLifetime      float64   `json:"last_lifetime_seconds,omitempty"`
	MinLifetime       float64   `json:"min_lifetime_seconds,omitempty"`
	Invalidations     int64     `json:"invalidations,omitempty"`
	LastInvalidatedAt time.Time `json:"last_invalidated_at,omitempty"`
	LastInvalidatedBy string    `json:"last_invalidated_by,omitempty"`
}

func (c comboStats) averageLifetime() float64 {
	if c.LifetimeSamples == 0 {
		return 0
	}
	return c.TotalLifetime / float64(c.LifetimeSamples)
}

func (c *comboStats) noteLifetime(lifetime time.Duration) {
	if c == nil || lifetime <= 0 {
		return
	}
	seconds := lifetime.Seconds()
	c.LifetimeSamples++
	c.TotalLifetime += seconds
	c.LastLifetime = seconds
	if c.MinLifetime == 0 || seconds < c.MinLifetime {
		c.MinLifetime = seconds
	}
}
