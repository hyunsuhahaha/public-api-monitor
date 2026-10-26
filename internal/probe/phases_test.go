package probe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// The server's think time lands in WaitMs; every check opens its own connection, so
// none is reused and each one has its connect phase.
func TestPhases(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
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
	if r2 := p.Check(context.Background(), &cfg.Targets[0]); r2.Phases.Reused || conns.Load() != 2 {
		t.Fatalf("second call: %+v, %d connections", r2.Phases, conns.Load())
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

// The dialer and the TLS handshake give up after 5 s on their own: the trace then reports
// that step done, with an error. The timeout is in that step, not "waiting for a
// connection" (which every such timeout was reported as before).
func TestStalledInAStepThatGaveUp(t *testing.T) {
	t0 := time.Now()
	for _, c := range []struct {
		name string
		set  func(c *phaseClock)
		want string
	}{
		{"TCP gave up", func(c *phaseClock) {
			c.dnsStart, c.dnsDone, c.connStart, c.connDone, c.connErr = t0, t0, t0, t0.Add(5*time.Second), true
		}, "connect"},
		{"TLS gave up", func(c *phaseClock) {
			c.connStart, c.connDone, c.tlsStart, c.tlsDone, c.tlsErr = t0, t0, t0, t0.Add(5*time.Second), true
		}, "tls"},
		{"DNS failed", func(c *phaseClock) { c.dnsStart, c.dnsDone, c.dnsErr = t0, t0.Add(time.Second), true }, "dns"},
		{"TCP still dialing", func(c *phaseClock) { c.connStart = t0 }, "connect"},
		{"nothing started", func(c *phaseClock) {}, "pool"},
	} {
		pc := &phaseClock{now: time.Now}
		c.set(pc)
		if got := pc.stalled(); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// End to end: a server that accepts TCP and never speaks TLS, so the handshake runs out.
func TestTLSHandshakeTimeoutIsTLS(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // hold it open, say nothing
		}
	}()
	cfg, err := config.Parse([]byte("targets:\n  - id: a\n    auth: none\n    classifier: http\n    timeout: 3s\n    url: https://" + ln.Addr().String() + "/\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("test")
	p.Client.Transport.(*http.Transport).TLSHandshakeTimeout = 500 * time.Millisecond
	r := p.Check(context.Background(), &cfg.Targets[0])
	if r.Reason != "timeout: TLS 연결 중" || r.Phases.TLSMs < 400 {
		t.Fatalf("%s %q %+v", r.Status, r.Reason, r.Phases)
	}
}
