package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// respCache keeps whole JSON responses for a short time per URL. Long ranges change
// little between refreshes and every open dashboard polls every 30 s, so one build serves
// them all. TTL by range: longer ranges, longer TTL.
type respCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	body    []byte
	expires time.Time
}

var cacheTTL = map[string]time.Duration{"1h": 15 * time.Second, "6h": 30 * time.Second, "24h": 30 * time.Second, "7d": 2 * time.Minute, "30d": 5 * time.Minute}

func (c *respCache) wrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ttl, ok := cacheTTL[r.URL.Query().Get("range")]
		if !ok {
			ttl = 30 * time.Second
		}
		key := r.URL.RawQuery
		c.mu.Lock()
		if c.entries == nil {
			c.entries = map[string]cacheEntry{}
		}
		e, hit := c.entries[key]
		c.mu.Unlock()
		if hit && time.Now().Before(e.expires) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Cache", "hit")
			w.Write(e.body)
			return
		}
		rec := httptest.NewRecorder()
		h(rec, r)
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.Header().Set("X-Cache", "miss")
		w.WriteHeader(rec.Code)
		body := rec.Body.Bytes()
		w.Write(body)
		if rec.Code == http.StatusOK {
			c.mu.Lock()
			for k, old := range c.entries { // drop expired entries as we go
				if time.Now().After(old.expires) {
					delete(c.entries, k)
				}
			}
			c.entries[key] = cacheEntry{bytes.Clone(body), time.Now().Add(ttl)}
			c.mu.Unlock()
		}
	}
}
