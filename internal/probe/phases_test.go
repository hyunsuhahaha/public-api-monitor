package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// The server's think time lands in WaitMs; a fresh connection has a connect time.
func TestPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DATA_GO_KR_KEY", "k")
	cfg, err := config.Parse([]byte("targets:\n  - id: a\n    url: " + srv.URL + "/?k={{.Key}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("test")
	r := p.Check(context.Background(), &cfg.Targets[0])
	if r.Phases.WaitMs < 60 || r.Phases.WaitMs > r.LatencyMS || r.Phases.Reused {
		t.Fatalf("first call: %+v (latency %d)", r.Phases, r.LatencyMS)
	}
	if r2 := p.Check(context.Background(), &cfg.Targets[0]); !r2.Phases.Reused || r2.Phases.ConnectMs != 0 {
		t.Fatalf("second call reuses the connection: %+v", r2.Phases)
	}
}

// A reused connection reports no DNS/connect/TLS even if the transport dialed a spare.
func TestPhasesReusedDropsSpareDial(t *testing.T) {
	t0 := time.Unix(0, 0)
	c := &phaseClock{now: func() time.Time { return t0 }}
	c.dnsStart, c.dnsDone = t0, t0.Add(5*time.Millisecond)
	c.connStart, c.connDone = t0, t0.Add(30*time.Millisecond)
	c.reused, c.wrote, c.firstByte = true, t0, t0.Add(100*time.Millisecond)
	if p := c.phases(); p != (Phases{WaitMs: 100, Reused: true}) {
		t.Fatalf("%+v", p)
	}
}

func TestStalledPhaseRoundTrip(t *testing.T) {
	for code, text := range stalledText {
		if got := StalledPhase("timeout: " + text); got != code {
			t.Fatalf("%q -> %q, want %q", text, got, code)
		}
	}
	if StalledPhase("timeout") != "" || StalledPhase("http 503") != "" {
		t.Fatal("not a phase")
	}
}
