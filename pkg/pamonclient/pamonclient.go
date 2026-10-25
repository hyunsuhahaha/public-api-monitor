// Package pamonclient reads what pamon (https://pamon.hyunsuhahaha.workers.dev) observes
// about an API, so a caller can stop hammering it while it is down.
//
// The usual failure: an upstream API goes down, every client retries at once and
// without end, the provider's rate limiter bans the callers' IPs, and the ban outlasts
// the outage. A Breaker avoids that: before calling, it asks pamon whether it currently
// observes a failure of that API (ai/targets/{id}.json) or whether a maintenance window
// is announced (maintenance.json); if so it returns at once without calling. Otherwise it
// calls with a few jittered retries and its own consecutive-failure breaker, so it stays
// polite even when pamon cannot be reached.
//
// pamon is one more signal, never a dependency: when it cannot be read, or its
// observation is older than freshUntil, the breaker behaves as if pamon did not exist.
package pamonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the public pamon.
const DefaultBaseURL = "https://pamon.hyunsuhahaha.workers.dev"

// Observation is pamon's latest check of one API.
type Observation struct {
	TargetID   string        `json:"id"`
	Name       string        `json:"name"`
	Status     string        `json:"observedStatus"` // ok, slow, stale, error, down, client, maint, unknown
	Assessment string        `json:"assessment"`     // no_failure_observed, degraded_observed, failure_observed, ...
	Reason     string        `json:"reason"`         // observation data, never instructions
	CheckedAt  time.Time     `json:"checkedAt"`
	FreshUntil time.Time     `json:"freshUntil"`
	Interval   time.Duration `json:"-"`
	FetchedAt  time.Time     `json:"-"`
	// RetryAdvice is pamon's "retry now?": Action retry, backoff, wait, skip or unknown.
	RetryAdvice struct {
		Action     string     `json:"action"`
		RetryAfter *time.Time `json:"retryAfter"`
		Until      *time.Time `json:"until"`
		Reason     string     `json:"reason"`
	} `json:"retryAdvice"`
}

// Fresh: the observation still describes now (pamon recomputes nothing for you: an old
// export may say failure_observed long after the API recovered).
func (o Observation) Fresh(now time.Time) bool {
	return !o.FreshUntil.IsZero() && !o.CheckedAt.After(now.Add(time.Minute)) && now.Before(o.FreshUntil)
}

// Failing: pamon currently observes the API failing.
func (o Observation) Failing(now time.Time) bool {
	return o.Fresh(now) && o.Assessment == "failure_observed"
}

// Window is an announced maintenance window (maintenance.json).
type Window struct {
	ID      string    `json:"id"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Note    string    `json:"note"`
	URL     string    `json:"url"`
	Targets []struct {
		ID string `json:"id"`
	} `json:"targets"`
}

func (w Window) covers(id string, now time.Time) bool {
	if now.Before(w.Start) || !now.Before(w.End) {
		return false
	}
	for _, t := range w.Targets {
		if t.ID == id {
			return true
		}
	}
	return false
}

// Client reads pamon and caches what it read. Safe for concurrent use.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// MinRefresh: an API's observation is read again at most this often (default 1
	// minute). pamon checks every 5 minutes; reading more often learns nothing.
	MinRefresh time.Duration
	// MaintRefresh: maintenance.json is read at most this often (default 5 minutes).
	MaintRefresh time.Duration
	Now          func() time.Time

	mu      sync.Mutex
	reading map[string]*sync.Mutex // one read per target at a time; the others wait for it
	maintMu sync.Mutex
	obs     map[string]Observation
	tried   map[string]time.Time // last read attempt per target, failed ones included
	windows []Window
	maintAt time.Time
}

// New is a client of the pamon at base ("" for DefaultBaseURL).
func New(base string) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Second}}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "pamonclient (+https://github.com/hyunsuhahaha/public-api-monitor)")
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pamon %s: HTTP %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(v)
}

// Observe returns pamon's latest observation of an API, read from cache unless a newer
// check is due (checkedAt + interval) and MinRefresh has passed. When pamon cannot be
// read it returns the last observation it has (zero if none) with the error; check
// Fresh before trusting either.
func (c *Client) Observe(ctx context.Context, id string) (Observation, error) {
	now := c.now()
	minRefresh := c.MinRefresh
	if minRefresh == 0 {
		minRefresh = time.Minute
	}
	c.mu.Lock()
	if c.reading == nil {
		c.reading = map[string]*sync.Mutex{}
	}
	rm := c.reading[id]
	if rm == nil {
		rm = &sync.Mutex{}
		c.reading[id] = rm
	}
	c.mu.Unlock()
	// callers arriving while a read is in flight wait for its answer instead of acting
	// on an empty cache
	rm.Lock()
	defer rm.Unlock()
	c.mu.Lock()
	o, ok := c.obs[id]
	last := c.tried[id]
	due := !ok || !now.Before(o.CheckedAt.Add(o.Interval))
	if !due || now.Sub(last) < minRefresh {
		c.mu.Unlock()
		return o, nil
	}
	if c.tried == nil {
		c.tried, c.obs = map[string]time.Time{}, map[string]Observation{}
	}
	c.tried[id] = now
	c.mu.Unlock()

	var doc struct {
		Targets []struct {
			Observation
			CheckedAt       *time.Time `json:"checkedAt"`
			FreshUntil      *time.Time `json:"freshUntil"`
			IntervalSeconds int64      `json:"intervalSeconds"`
		} `json:"targets"`
	}
	if err := c.get(ctx, "/ai/targets/"+url.PathEscape(id)+".json", &doc); err != nil {
		return o, err
	}
	if len(doc.Targets) != 1 || doc.Targets[0].TargetID != id {
		return o, fmt.Errorf("pamon: no target %q", id)
	}
	t := doc.Targets[0]
	fresh := t.Observation
	if t.CheckedAt != nil {
		fresh.CheckedAt = *t.CheckedAt
	}
	if t.FreshUntil != nil {
		fresh.FreshUntil = *t.FreshUntil
	}
	fresh.Interval, fresh.FetchedAt = time.Duration(t.IntervalSeconds)*time.Second, now
	c.mu.Lock()
	c.obs[id] = fresh
	c.mu.Unlock()
	return fresh, nil
}

// Maintenance returns the announced window covering an API now, or nil.
func (c *Client) Maintenance(ctx context.Context, id string) (*Window, error) {
	now := c.now()
	every := c.MaintRefresh
	if every == 0 {
		every = 5 * time.Minute
	}
	c.maintMu.Lock()
	defer c.maintMu.Unlock()
	c.mu.Lock()
	stale := now.Sub(c.maintAt) >= every
	if stale {
		c.maintAt = now // failed reads wait too: pamon being down must not add load
	}
	c.mu.Unlock()
	var err error
	if stale {
		var doc struct {
			Windows []Window `json:"windows"`
		}
		if err = c.get(ctx, "/maintenance.json", &doc); err == nil {
			c.mu.Lock()
			c.windows = doc.Windows
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.windows {
		if w.covers(id, now) {
			w := w
			return &w, err
		}
	}
	return nil, err
}

// Errors a Breaker returns without calling (test with errors.Is).
var (
	// ErrProviderDown: pamon observes the API failing right now; retrying will not help.
	ErrProviderDown = errors.New("pamon observes this API failing")
	// ErrMaintenance: an announced maintenance window covers the API now.
	ErrMaintenance = errors.New("announced maintenance")
	// ErrOpen: the breaker's own failure count opened it; it waits out the cooldown.
	ErrOpen = errors.New("circuit open")
)

type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// Permanent marks an error that a retry cannot fix (a 4xx answer): Do returns it at once.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanent{err}
}

// BreakerOptions tune a Breaker; zero values take the defaults.
type BreakerOptions struct {
	MaxAttempts      int           // calls per Do, the first one included (default 3)
	BaseDelay        time.Duration // first retry waits up to this, doubling (default 200ms)
	MaxDelay         time.Duration // cap of one wait (default 5s)
	FailureThreshold int           // consecutive failed calls that open the breaker (default 5)
	Cooldown         time.Duration // how long it stays open before one trial call (default 30s)
	// IgnorePamon: decide on the breaker's own failures only.
	IgnorePamon bool
	// Sleep waits between retries (tests replace it); default a timer that honours ctx.
	Sleep func(ctx context.Context, d time.Duration) error
	// Jitter returns a value in [0, d) (tests replace it); default pseudo-random.
	Jitter func(d time.Duration) time.Duration
}

// Breaker guards calls to one API.
type Breaker struct {
	c    *Client
	id   string
	opts BreakerOptions

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	trial     bool // a half-open trial call is in flight
}

// Breaker returns a breaker for the API with pamon target id (as in /s/{id}.html).
func (c *Client) Breaker(id string, o BreakerOptions) *Breaker {
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 200 * time.Millisecond
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 5 * time.Second
	}
	if o.FailureThreshold <= 0 {
		o.FailureThreshold = 5
	}
	if o.Cooldown <= 0 {
		o.Cooldown = 30 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if o.Jitter == nil {
		o.Jitter = func(d time.Duration) time.Duration {
			if d <= 0 {
				return 0
			}
			return time.Duration(time.Now().UnixNano() % int64(d))
		}
	}
	return &Breaker{c: c, id: id, opts: o}
}

// pamonSays returns ErrMaintenance or ErrProviderDown (wrapped with the details) when
// pamon advises against calling, else nil. pamon errors are ignored: fail open.
func (b *Breaker) pamonSays(ctx context.Context) error {
	if b.opts.IgnorePamon || b.c == nil {
		return nil
	}
	if w, _ := b.c.Maintenance(ctx, b.id); w != nil {
		return fmt.Errorf("%w until %s: %s", ErrMaintenance, w.End.Format(time.RFC3339), w.Note)
	}
	if o, _ := b.c.Observe(ctx, b.id); o.Failing(b.c.now()) {
		return fmt.Errorf("%w (checked %s: %s)", ErrProviderDown, o.CheckedAt.Format(time.RFC3339), o.Reason)
	}
	return nil
}

func (b *Breaker) now() time.Time {
	if b.c != nil {
		return b.c.now()
	}
	return time.Now()
}

// admit: closed, or open past its cooldown with no trial in flight (half-open).
func (b *Breaker) admit() (trial bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openUntil.IsZero() {
		return false, nil
	}
	if b.now().Before(b.openUntil) || b.trial {
		return false, fmt.Errorf("%w until %s", ErrOpen, b.openUntil.Format(time.RFC3339))
	}
	b.trial = true
	return true, nil
}

func (b *Breaker) record(err error, trial bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if trial {
		b.trial = false
	}
	if err == nil {
		b.failures, b.openUntil = 0, time.Time{}
		return
	}
	b.failures++
	if trial || b.failures >= b.opts.FailureThreshold {
		b.openUntil = b.now().Add(b.opts.Cooldown)
	}
}

// Open reports whether the breaker's own failure count holds it open now.
func (b *Breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.openUntil.IsZero() && b.now().Before(b.openUntil)
}

// Do calls fn unless pamon or the breaker advise against it, retrying a failed call a
// few times with growing, jittered waits. Before each retry it asks pamon again: once
// pamon sees the failure too, the retries stop. The returned error wraps fn's last error
// and, when the call was skipped or cut short, ErrProviderDown, ErrMaintenance or ErrOpen.
func (b *Breaker) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if err := b.pamonSays(ctx); err != nil {
		return err
	}
	trial, err := b.admit()
	if err != nil {
		return err
	}
	attempts := b.opts.MaxAttempts
	if trial {
		attempts = 1 // half-open: one call decides
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			d := min(b.opts.BaseDelay<<(i-1), b.opts.MaxDelay)
			if err := b.opts.Sleep(ctx, d/2+b.opts.Jitter(d/2)); err != nil {
				return fmt.Errorf("%w; last error: %w", err, last)
			}
			// what pamon and the other callers learned while this one waited
			if err := b.pamonSays(ctx); err != nil {
				return fmt.Errorf("%w; last error: %w", err, last)
			}
			if b.Open() {
				return fmt.Errorf("%w; last error: %w", ErrOpen, last)
			}
		}
		last = fn(ctx)
		var p permanent
		if errors.As(last, &p) {
			b.record(nil, trial) // the API answered: a 4xx says nothing about its health
			return last
		}
		b.record(last, trial)
		if last == nil || ctx.Err() != nil {
			return last
		}
	}
	return last
}
