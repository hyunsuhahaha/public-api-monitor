package pamonclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Game day for callers: an upstream API goes down and its rate limiter bans a client
// after banAfter requests during the outage (the ban outlasts the outage, as real ones
// do). Ten workers keep calling it. Without pamon they retry their way into the ban;
// with a Breaker they stop at once (pamon sees the outage) or after a handful of calls
// (pamon unreachable), and work again when it recovers.

const banAfter = 30

type upstream struct {
	srv      *httptest.Server
	down     atomic.Bool
	calls    atomic.Int64
	failed   atomic.Int64 // requests during the outage
	banned   atomic.Bool
	callsAll atomic.Int64
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.callsAll.Add(1)
		if u.banned.Load() {
			http.Error(w, "IP blocked: too many requests", http.StatusForbidden)
			return
		}
		if u.down.Load() {
			if u.failed.Add(1) >= banAfter {
				u.banned.Store(true)
			}
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}
		u.calls.Add(1)
		fmt.Fprint(w, `{"ok":true}`)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *upstream) call(ctx context.Context) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.srv.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return Permanent(fmt.Errorf("HTTP %d", resp.StatusCode))
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// fakePamon serves ai/targets/upstream.json and maintenance.json.
type fakePamon struct {
	srv        *httptest.Server
	mu         sync.Mutex
	assessment string
	checkedAt  time.Time
	windows    []map[string]any
	reads      atomic.Int64
}

func newPamon(t *testing.T, now time.Time) *fakePamon {
	p := &fakePamon{assessment: "no_failure_observed", checkedAt: now}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.reads.Add(1)
		p.mu.Lock()
		defer p.mu.Unlock()
		switch r.URL.Path {
		case "/ai/targets/upstream.json":
			status := map[string]string{"failure_observed": "down", "no_failure_observed": "ok"}[p.assessment]
			json.NewEncoder(w).Encode(map[string]any{"targets": []map[string]any{{
				"id": "upstream", "observedStatus": status, "assessment": p.assessment, "reason": "http 503",
				"checkedAt": p.checkedAt, "freshUntil": p.checkedAt.Add(10 * time.Minute), "intervalSeconds": 300}}})
		case "/maintenance.json":
			json.NewEncoder(w).Encode(map[string]any{"windows": p.windows})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakePamon) set(assessment string, at time.Time) {
	p.mu.Lock()
	p.assessment, p.checkedAt = assessment, at
	p.mu.Unlock()
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var noWait = BreakerOptions{Sleep: func(context.Context, time.Duration) error { return nil }}

// workers runs n workers making calls each through call, concurrently.
func workers(n, calls int, call func() error) (errs []error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				err := call()
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errs
}

func TestRetryStormGetsBanned(t *testing.T) {
	u := newUpstream(t)
	u.down.Store(true)
	ctx := context.Background()
	// the naive client: retry until it works (capped here so the test ends)
	workers(10, 1, func() error {
		for i := 0; i < 100; i++ {
			if u.call(ctx) == nil {
				return nil
			}
		}
		return errors.New("gave up")
	})
	u.down.Store(false) // the outage ends...
	if !u.banned.Load() || u.call(ctx) == nil {
		t.Fatal("expected the retry storm to get the client banned")
	}
	t.Logf("naive retries: %d requests, banned after the outage ended", u.callsAll.Load())
}

func TestBreakerStaysOutOfTheBanWhenPamonSeesTheOutage(t *testing.T) {
	clk := &clock{t: time.Now()}
	u, p := newUpstream(t), newPamon(t, clk.now())
	u.down.Store(true)
	p.set("failure_observed", clk.now())
	c := New(p.srv.URL)
	c.Now = clk.now
	b := c.Breaker("upstream", noWait)
	ctx := context.Background()
	errs := workers(10, 20, func() error { return b.Do(ctx, u.call) })
	for _, err := range errs {
		if !errors.Is(err, ErrProviderDown) {
			t.Fatalf("got %v, want ErrProviderDown", err)
		}
	}
	if n := u.callsAll.Load(); n != 0 || u.banned.Load() {
		t.Fatalf("%d requests reached the API during the outage", n)
	}
	if r := p.reads.Load(); r > 2 { // one observation, one maintenance read, then the cache
		t.Fatalf("pamon read %d times for 200 calls", r)
	}
	// recovery: pamon's next check (5 minutes later) is ok, and so is the API
	u.down.Store(false)
	clk.add(5 * time.Minute)
	p.set("no_failure_observed", clk.now())
	if err := b.Do(ctx, u.call); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestBreakerWithoutPamonStillBacksOff(t *testing.T) {
	clk := &clock{t: time.Now()}
	u := newUpstream(t)
	u.down.Store(true)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close() // pamon unreachable: the breaker must not depend on it
	c := New(dead.URL)
	c.Now = clk.now
	b := c.Breaker("upstream", noWait)
	ctx := context.Background()
	workers(10, 20, func() error { return b.Do(ctx, u.call) })
	if u.banned.Load() || u.callsAll.Load() >= banAfter {
		t.Fatalf("%d requests during the outage (ban at %d)", u.callsAll.Load(), banAfter)
	}
	if !errors.Is(b.Do(ctx, u.call), ErrOpen) {
		t.Fatal("breaker should be open")
	}
	t.Logf("own breaker only: %d requests for 200 calls", u.callsAll.Load())
	// after the cooldown one trial call goes through and closes it
	u.down.Store(false)
	clk.add(31 * time.Second)
	if err := b.Do(ctx, u.call); err != nil || b.Open() {
		t.Fatalf("trial call: %v", err)
	}
}

func TestStaleObservationIsNotTrusted(t *testing.T) {
	clk := &clock{t: time.Now()}
	u, p := newUpstream(t), newPamon(t, clk.now().Add(-time.Hour)) // an export an hour old
	p.set("failure_observed", clk.now().Add(-time.Hour))
	c := New(p.srv.URL)
	c.Now = clk.now
	if err := c.Breaker("upstream", noWait).Do(context.Background(), u.call); err != nil || u.calls.Load() != 1 {
		t.Fatalf("an expired failure_observed blocked the call: %v", err)
	}
}

func TestMaintenanceSkipsTheCall(t *testing.T) {
	clk := &clock{t: time.Now()}
	u, p := newUpstream(t), newPamon(t, clk.now())
	p.windows = []map[string]any{{"id": "w1", "start": clk.now().Add(-time.Minute), "end": clk.now().Add(time.Hour),
		"note": "정기 점검", "targets": []map[string]string{{"id": "upstream"}}}}
	c := New(p.srv.URL)
	c.Now = clk.now
	b := c.Breaker("upstream", noWait)
	if err := b.Do(context.Background(), u.call); !errors.Is(err, ErrMaintenance) || u.callsAll.Load() != 0 {
		t.Fatalf("got %v with %d calls", err, u.callsAll.Load())
	}
	clk.add(2 * time.Hour) // the window is over (the cached list says so too)
	if err := b.Do(context.Background(), u.call); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesStopWhenPamonConfirms(t *testing.T) {
	clk := &clock{t: time.Now()}
	u, p := newUpstream(t), newPamon(t, clk.now())
	u.down.Store(true)
	c := New(p.srv.URL)
	c.Now = clk.now
	c.MinRefresh = time.Nanosecond
	opts := noWait
	opts.MaxAttempts = 10
	opts.Sleep = func(context.Context, time.Duration) error {
		// while we wait, pamon's next check sees the outage
		clk.add(5 * time.Minute)
		p.set("failure_observed", clk.now())
		return nil
	}
	err := c.Breaker("upstream", opts).Do(context.Background(), u.call)
	if !errors.Is(err, ErrProviderDown) || u.callsAll.Load() != 1 {
		t.Fatalf("got %v after %d calls, want ErrProviderDown after 1", err, u.callsAll.Load())
	}
}
