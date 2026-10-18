package probe

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// Phases splits one request's time into where it went: name lookup, TCP connect, TLS,
// and the wait for the first byte after the request was sent (the server's own time).
// A timeout says little by itself; "timed out waiting for the first byte, connected in
// 30 ms" points at the provider's server, "timed out connecting" at the network.
// Milliseconds; 0 for a phase that did not happen (a reused connection skips the first three).
type Phases struct {
	DNSMs     int64 `json:"dnsMs,omitempty"`
	ConnectMs int64 `json:"connectMs,omitempty"`
	TLSMs     int64 `json:"tlsMs,omitempty"`
	WaitMs    int64 `json:"waitMs,omitempty"`
	Reused    bool  `json:"reused,omitempty"`
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
		DNSStart:             func(httptrace.DNSStartInfo) { c.mark(&c.dnsStart) },
		DNSDone:              func(httptrace.DNSDoneInfo) { c.mark(&c.dnsDone) },
		ConnectStart:         func(string, string) { c.mark(&c.connStart) },
		ConnectDone:          func(string, string, error) { c.mark(&c.connDone) },
		TLSHandshakeStart:    func() { c.mark(&c.tlsStart) },
		TLSHandshakeDone:     func(tls.ConnectionState, error) { c.mark(&c.tlsDone) },
		GotConn:              func(i httptrace.GotConnInfo) { c.mu.Lock(); c.reused = i.Reused; c.mu.Unlock(); c.mark(&c.gotConn) },
		WroteRequest:         func(httptrace.WroteRequestInfo) { c.mark(&c.wrote) },
		GotFirstResponseByte: func() { c.mark(&c.firstByte) },
	}
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
		return Phases{WaitMs: span(c.wrote, c.firstByte), Reused: true}
	}
	return Phases{DNSMs: span(c.dnsStart, c.dnsDone), ConnectMs: span(c.connStart, c.connDone),
		TLSMs: span(c.tlsStart, c.tlsDone), WaitMs: span(c.wrote, c.firstByte), Reused: c.reused}
}

// Stalled phases, as metric labels, and how a timeout's reason names them.
var stalledText = map[string]string{
	"body": "본문 받는 중", "wait": "서버 응답 대기 중 (연결은 됨)", "send": "요청 보내는 중",
	"tls": "TLS 연결 중", "connect": "TCP 연결 중", "dns": "DNS 조회 중", "pool": "연결 대기 중",
}

// StalledPhase is the phase code in a timeout reason ("timeout: <text>"), or "".
func StalledPhase(reason string) string {
	for code, text := range stalledText {
		if reason == "timeout: "+text {
			return code
		}
	}
	return ""
}

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
	case !c.tlsStart.IsZero() && c.tlsDone.IsZero():
		return "tls"
	case !c.connStart.IsZero() && c.connDone.IsZero():
		return "connect"
	case !c.dnsStart.IsZero() && c.dnsDone.IsZero():
		return "dns"
	}
	return "pool"
}
