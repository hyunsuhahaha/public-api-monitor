package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBaselineLimit(t *testing.T) {
	b := NewBaseline()
	if _, _, ok := b.Limit("a", 3*time.Second); ok {
		t.Fatal("no limit before MinSamples")
	}
	for i := 0; i < 30; i++ {
		b.Add("a", 50*time.Millisecond)
	}
	limit, usual, ok := b.Limit("a", 3*time.Second)
	if !ok || usual != 50*time.Millisecond || limit != 450*time.Millisecond {
		t.Fatalf("fast API: limit %v usual %v", limit, usual)
	}
	for i := 0; i < 30; i++ {
		b.Add("slowapi", 1200*time.Millisecond)
	}
	if limit, _, _ := b.Limit("slowapi", 3*time.Second); limit != 3*time.Second {
		t.Fatalf("capped at slow_after: %v", limit)
	}
}

func TestBaselineWindowForgets(t *testing.T) {
	b := NewBaseline()
	b.Window = 10
	b.MinSamples = 5
	for i := 0; i < 10; i++ {
		b.Add("a", 50*time.Millisecond)
	}
	for i := 0; i < 6; i++ { // more than half the window: the new normal
		b.Add("a", 900*time.Millisecond)
	}
	if _, usual, _ := b.Limit("a", 0); usual != 900*time.Millisecond {
		t.Fatalf("usual after a lasting change: %v", usual)
	}
}

// End to end: an API that normally answers fast is marked slow when it gets 10x slower,
// well under the fixed 3 s limit.
func TestCheckAdaptiveSlow(t *testing.T) {
	var delay atomic.Int64
	delay.Store(int64(5 * time.Millisecond))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(delay.Load()))
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DATA_GO_KR_KEY", "k")
	p := New("test")
	tg := target(t, "targets:\n  - id: a\n    url: "+srv.URL+"/?k={{.Key}}\n")
	for i := 0; i < p.Baseline.MinSamples; i++ {
		if r := p.Check(context.Background(), tg); r.Status != StatusOK {
			t.Fatalf("warm-up: %+v", r)
		}
	}
	delay.Store(int64(700 * time.Millisecond))
	r := p.Check(context.Background(), tg)
	if r.Status != StatusSlow || !strings.Contains(r.Reason, "usually") {
		t.Fatalf("want adaptive slow, got %+v", r)
	}
}
