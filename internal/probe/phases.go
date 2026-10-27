package probe

import (
	"context"
	"crypto/tls"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

// Phases splits one request's time into where it went: name lookup, TCP connect, TLS,
// and the wait for the first byte after the request was sent (the server's own time).
// A timeout says little by itself; "timed out waiting for the first byte, connected in
// 30 ms" points at the provider's server, "timed out connecting" at the network.
// Milliseconds; 0 for a phase that did not happen (a reused connection skips the first three).
// A redirect the probe followed (same host, up to 3) is RedirectMs: every earlier request,
// whole. The other phases are the last request's, the one whose answer was judged.
type Phases struct {
	RedirectMs int64 `json:"redirectMs,omitempty"`
	DNSMs      int64 `json:"dnsMs,omitempty"`
	ConnectMs  int64 `json:"connectMs,omitempty"`
	TLSMs      int64 `json:"tlsMs,omitempty"`
	WaitMs     int64 `json:"waitMs,omitempty"`
	Reused     bool  `json:"reused,omitempty"`
}

// phaseClock collects httptrace events; the transport may call it from several goroutines.
type phaseClock struct {
	mu                        sync.Mutex
	now                       func() time.Time
	dnsStart, dnsDone         time.Time
	connStart, connDone       time.Time
	tlsStart, tlsDone         time.Time
	gotConn, wrote, firstByte time.Time
	reused                    bool
	// the first request's start and, once a redirect was followed, when the last began
	firstStart, hopStart time.Time
	// a step that ended in an error (the dialer's or TLS's own 5-second limit): it is
	// done but failed, and the request stopped there
	dnsErr, connErr, tlsErr bool
}

func (c *phaseClock) fail(flag *bool, err error) {
	if err != nil {
		c.mu.Lock()
		*flag = true
		c.mu.Unlock()
	}
}

// hop starts a request: the first, or the next one after a redirect. The steps measured
// so far belong to an earlier request; they are cleared so the phases are the last one's.
func (c *phaseClock) hop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.firstStart.IsZero() {
		c.firstStart = now
		return
	}
	c.hopStart = now
	c.dnsStart, c.dnsDone, c.connStart, c.connDone, c.tlsStart, c.tlsDone = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
	c.gotConn, c.wrote, c.firstByte = time.Time{}, time.Time{}, time.Time{}
	c.reused, c.dnsErr, c.connErr, c.tlsErr = false, false, false, false
}

func (c *phaseClock) mark(t *time.Time) {
	c.mu.Lock()
	if t.IsZero() {
		*t = c.now()
	}
	c.mu.Unlock()
}

func (c *phaseClock) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		GetConn:              func(string) { c.hop() },
		DNSStart:             func(httptrace.DNSStartInfo) { c.mark(&c.dnsStart) },
		DNSDone:              func(i httptrace.DNSDoneInfo) { c.fail(&c.dnsErr, i.Err); c.mark(&c.dnsDone) },
		ConnectStart:         func(string, string) { c.mark(&c.connStart) },
		ConnectDone:          func(_, _ string, err error) { c.fail(&c.connErr, err); c.mark(&c.connDone) },
		TLSHandshakeStart:    func() { c.mark(&c.tlsStart) },
		TLSHandshakeDone:     func(_ tls.ConnectionState, err error) { c.fail(&c.tlsErr, err); c.mark(&c.tlsDone) },
		GotConn:              func(i httptrace.GotConnInfo) { c.mu.Lock(); c.reused = i.Reused; c.mu.Unlock(); c.mark(&c.gotConn) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { c.mark(&c.wrote) },
		GotFirstResponseByte: func() { c.mark(&c.firstByte) },
	}
}

func withTrace(ctx context.Context, c *phaseClock) context.Context {
	return httptrace.WithClientTrace(ctx, c.trace())
}

func span(a, b time.Time) int64 {
	if a.IsZero() || b.IsZero() {
		return 0
	}
	return b.Sub(a).Milliseconds()
}

func (c *phaseClock) phases() Phases {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reused {
		// the transport may dial a spare connection while a pooled one frees up and is
		// used instead: that dial's DNS/connect/TLS are not this request's
		return Phases{RedirectMs: span(c.firstStart, c.hopStart), WaitMs: span(c.wrote, c.firstByte), Reused: true}
	}
	return Phases{RedirectMs: span(c.firstStart, c.hopStart), DNSMs: span(c.dnsStart, c.dnsDone), ConnectMs: span(c.connStart, c.connDone),
		TLSMs: span(c.tlsStart, c.tlsDone), WaitMs: span(c.wrote, c.firstByte), Reused: c.reused}
}

// Stalled phases, as metric labels, and how a timeout's reason names them.
var stalledText = map[string]string{
	"body": "본문 받는 중", "wait": "서버 응답 대기 중 (연결은 됨)", "send": "요청 보내는 중",
	"tls": "TLS 연결 중", "connect": "TCP 연결 중", "dns": "DNS 조회 중", "pool": "연결 대기 중",
}

// StalledPhase is the phase code in a timeout reason ("timeout: <text>"), or "".
func StalledPhase(reason string) string {
	text, ok := strings.CutPrefix(reason, "timeout: ")
	if !ok {
		return ""
	}
	return stalledCode[text]
}

var stalledCode = func() map[string]string {
	m := map[string]string{}
	for code, text := range stalledText {
		m[text] = code
	}
	return m
}()

// stalled names the phase a request that ran out of time was stuck in.
func (c *phaseClock) stalled() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case !c.firstByte.IsZero():
		return "body"
	case !c.wrote.IsZero():
		return "wait"
	case !c.gotConn.IsZero():
		return "send"
	// a step that gave up on its own limit (5 s for TCP and TLS) is "done" with an error:
	// that is where it stopped, not the pool
	case c.tlsErr:
		return "tls"
	case c.connErr:
		return "connect"
	case c.dnsErr:
		return "dns"
	case !c.tlsStart.IsZero() && c.tlsDone.IsZero():
		return "tls"
	case !c.connStart.IsZero() && c.connDone.IsZero():
		return "connect"
	case !c.dnsStart.IsZero() && c.dnsDone.IsZero():
		return "dns"
	}
	return "pool"
}
