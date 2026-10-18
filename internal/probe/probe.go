package probe

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/telemetry"
)

// MaxBody: enough to find the header and a timestamp; the rest is not read.
const MaxBody = 256 << 10

// Result is one finished check of one target.
type Result struct {
	TargetID   string
	Location   string
	At         time.Time
	Status     Status
	LatencyMS  int64
	HTTPStatus int
	Code       string
	Reason     string
	Shape      []string // field paths of a healthy answer (schema-change detection), not sent to the central
	Phases     Phases   // where the time went (DNS, connect, TLS, server wait)
}

// Prober runs checks.
type Prober struct {
	Client   *http.Client
	Location string
	Now      func() time.Time
	Baseline *Baseline // nil: only the fixed slow_after limit
}

// New returns a prober whose client never follows the key into another host's logs:
// redirects are allowed only on the same host.
func New(location string) *Prober {
	return &Prober{
		Location: location,
		Now:      time.Now,
		Baseline: NewBaseline(),
		Client: &http.Client{
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
				TLSHandshakeTimeout: 5 * time.Second,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 || req.URL.Host != via[0].URL.Host {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// Check calls one target once.
func (p *Prober) Check(ctx context.Context, t *config.Target) Result {
	start := p.Now()
	r := Result{TargetID: t.ID, Location: p.Location, At: start}
	if !t.HasKey() {
		r.Status, r.Reason = StatusClient, "service key not set ("+t.KeyEnv+")"
		return r
	}
	u, err := t.Render(start)
	if err != nil {
		r.Status, r.Reason = StatusClient, "url template: "+err.Error()
		return r
	}
	ctx, cancel := context.WithTimeout(ctx, t.Timeout.Duration)
	defer cancel()
	clock := &phaseClock{now: p.Now}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, clock.trace()), http.MethodGet, u, nil)
	if err != nil {
		r.Status, r.Reason = StatusClient, "bad url"
		return r
	}
	req.Header.Set("User-Agent", "public-api-monitor/0.1 (+https://github.com/hyunsuhahaha/public-api-monitor)")
	req.Header.Set("Accept", "application/json, application/xml;q=0.9, */*;q=0.5")

	// The HTTP span is made by hand, not by otelhttp: that records the full URL, and the
	// query string carries the service key. Host and path only, and the path redacted too:
	// Seoul's open API puts the key in the path (/{key}/json/...).
	path := t.Redact(req.URL.Path)
	_, span := telemetry.Tracer().Start(ctx, "GET "+req.URL.Host+path, trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attribute.String("http.request.method", "GET"), attribute.String("server.address", req.URL.Hostname()),
			attribute.String("url.path", path)))
	var o Observation
	resp, err := p.Client.Do(req)
	if err == nil {
		o.HTTPStatus = resp.StatusCode
		o.Body, err = io.ReadAll(io.LimitReader(resp.Body, MaxBody))
		resp.Body.Close()
		span.SetAttributes(attribute.Int("http.response.status_code", o.HTTPStatus), attribute.Int("http.response.body.size", len(o.Body)))
	}
	if err != nil {
		span.SetStatus(codes.Error, t.Redact(err.Error()))
	}
	span.End()
	o.Latency = p.Now().Sub(start)
	if err != nil {
		o.Err = errors.New(t.Redact(err.Error()))
		o.Timeout = errors.Is(err, context.DeadlineExceeded) || isTimeout(err)
	}
	v := Classify(o, t.SlowAfter.Duration)
	r.Phases = clock.phases()
	if o.Timeout {
		v.Reason = "timeout: " + stalledText[clock.stalled()]
	}
	if v.Status == StatusOK && p.Baseline != nil {
		// slower than this API usually is (learned per target), though under slow_after
		if limit, usual, ok := p.Baseline.Limit(t.ID, t.SlowAfter.Duration); ok && o.Latency > limit {
			v.Status = StatusSlow
			v.Reason = "slow: " + o.Latency.Round(time.Millisecond).String() + " (usually " + usual.Round(time.Millisecond).String() + ", limit " + limit.Round(time.Millisecond).String() + ")"
		}
		// (answers past the fixed limit or timeouts are not learned: an outage must not
		// become the normal; a lasting moderate change does, over half the window)
		p.Baseline.Observe(t.ID, o.Latency, v.Status == StatusSlow)
	}
	if v.Status.Healthy() && t.Freshness != nil {
		if stale, why := staleness(o.Body, t.Freshness, p.Now()); stale {
			v.Status, v.Reason = StatusStale, why
		}
	}
	r.Status, r.Code, r.Reason = v.Status, v.Code, v.Reason
	if v.Status.Healthy() {
		r.Shape = Shape(o.Body)
	}
	r.HTTPStatus = o.HTTPStatus
	r.LatencyMS = o.Latency.Milliseconds()
	return r
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// staleness finds the newest timestamp the pattern matches and compares it with now.
func staleness(body []byte, f *config.Freshness, now time.Time) (bool, string) {
	var newest time.Time
	for _, m := range f.Re.FindAllSubmatch(body, 200) {
		if len(m) < 2 {
			continue
		}
		ts, err := time.ParseInLocation(f.Layout, string(m[1]), config.KST)
		if err == nil && ts.After(newest) {
			newest = ts
		}
	}
	if newest.IsZero() {
		return true, "stale: no timestamp in response"
	}
	if age := now.Sub(newest); age > f.MaxAge.Duration {
		return true, "stale: newest data " + age.Round(time.Minute).String() + " old"
	}
	return false, ""
}
