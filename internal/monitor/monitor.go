// Package monitor schedules the checks and hands each result to a Sink: the Central
// (store, combine locations, incidents) or a remote probe's pusher.
package monitor

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/telemetry"
)

var (
	mChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_checks_total", Help: "Checks by target, location and status.",
	}, []string{"target", "location", "status"})
	mLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pamon_check_latency_seconds",
		Help:    "Response time of checks that got an answer.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 13},
	}, []string{"target", "location"})
	// where the time went (probe.Phases): the network's share vs the provider's server
	mPhase = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pamon_check_phase_seconds",
		Help:    "Time per request phase: dns, connect, tls (new connections only) and wait (for the first byte).",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	}, []string{"target", "phase"})
	mTimeouts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_check_timeouts_total", Help: "Timed-out checks by the phase they were stuck in (wait = the provider's server).",
	}, []string{"target", "stalled"})
	mLastCheck = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_last_check_timestamp_seconds", Help: "When each target was last checked from each location.",
	}, []string{"target", "location"})
	// Our own SLI: a check that ran more than one interval after its slot is late (the
	// monitor fell behind or stopped). deploy/alerts.yml burns an error budget on these.
	mLate = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_late_checks_total", Help: "Checks that ran more than one interval late.",
	})
	mScheduled = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_scheduled_checks_total", Help: "Checks run by this process's scheduler.",
	})
)

// observe records the raw-check metrics (used for local and ingested results).
func observe(r probe.Result) {
	mChecks.WithLabelValues(r.TargetID, r.Location, string(r.Status)).Inc()
	mLastCheck.WithLabelValues(r.TargetID, r.Location).Set(float64(r.At.Unix()))
	if r.HTTPStatus > 0 {
		mLatency.WithLabelValues(r.TargetID, r.Location).Observe(float64(r.LatencyMS) / 1000)
	}
	p := r.Phases
	for _, x := range []struct {
		name string
		ms   int64
	}{{"redirect", p.RedirectMs}, {"dns", p.DNSMs}, {"connect", p.ConnectMs}, {"tls", p.TLSMs}, {"wait", p.WaitMs}} {
		if x.ms > 0 {
			mPhase.WithLabelValues(r.TargetID, x.name).Observe(float64(x.ms) / 1000)
		}
	}
	if code := probe.StalledPhase(r.Reason); code != "" {
		mTimeouts.WithLabelValues(r.TargetID, code).Inc()
	}
}

// Sink receives every finished check of this process.
type Sink interface {
	Record(ctx context.Context, t *config.Target, r probe.Result)
}

type Monitor struct {
	Cfg    *config.Config
	Prober *probe.Prober
	Sink   Sink
	Log    *slog.Logger
	// MaxParallel limits concurrent requests (data.go.kr shares one gateway).
	MaxParallel int

	lastCheck atomic.Int64 // unix seconds of the last finished check
	sem       chan struct{}
}

// LastCheck is when this process last finished a check (readiness).
func (m *Monitor) LastCheck() time.Time { return time.Unix(m.lastCheck.Load(), 0) }

// Run checks every target on its interval until ctx ends. Each target gets a fixed
// offset inside its interval (hash of its id), so the gateway sees an even trickle, not a
// burst every five minutes, and every location checks a target at about the same moment
// (their verdicts are compared).
func (m *Monitor) Run(ctx context.Context) {
	if m.MaxParallel <= 0 {
		m.MaxParallel = 4
	}
	m.sem = make(chan struct{}, m.MaxParallel)
	m.lastCheck.Store(time.Now().Unix())
	var wg sync.WaitGroup
	for i := range m.Cfg.Targets {
		t := &m.Cfg.Targets[i]
		if !t.CheckedFrom(m.Prober.Location) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.loop(ctx, t)
		}()
	}
	wg.Wait()
}

// Round checks every target once and returns when all are done: for a scheduler outside
// the process (a cron job) instead of Run's own slots.
func (m *Monitor) Round(ctx context.Context) {
	if m.MaxParallel <= 0 {
		m.MaxParallel = 4
	}
	m.sem = make(chan struct{}, m.MaxParallel)
	now := time.Now()
	var wg sync.WaitGroup
	for i := range m.Cfg.Targets {
		t := &m.Cfg.Targets[i]
		if !t.CheckedFrom(m.Prober.Location) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.checkOnce(ctx, t, now, t.Interval.Duration)
		}()
	}
	wg.Wait()
}

func offset(id string, every time.Duration) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(id))
	return time.Duration(uint64(h.Sum32()) % uint64(every))
}

// firstSlot: the next wall-clock slot for the target, aligned so all locations agree.
func firstSlot(id string, every time.Duration, now time.Time) time.Time {
	base := now.Truncate(every).Add(offset(id, every))
	for !base.After(now) {
		base = base.Add(every)
	}
	return base
}

func (m *Monitor) loop(ctx context.Context, t *config.Target) {
	every := t.Interval.Duration
	// one check right away (spread over 20 s) so a restart shows data, then on the slot
	timer := time.NewTimer(offset(t.ID, 20*time.Second))
	defer timer.Stop()
	slot := time.Now()
	next := firstSlot(t.ID, every, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.checkOnce(ctx, t, slot, every)
		slot = next
		next = next.Add(every)
		if now := time.Now(); slot.Before(now.Add(-every)) {
			// fell behind (laptop sleep, long stall): skip the missed slots
			slot = firstSlot(t.ID, every, now)
			next = slot.Add(every)
		}
		timer.Reset(time.Until(slot))
	}
}

func (m *Monitor) checkOnce(ctx context.Context, t *config.Target, slot time.Time, every time.Duration) {
	// one trace per check: queue wait, the HTTP call (probe), and the central's decision
	ctx, span := telemetry.Tracer().Start(ctx, "check "+t.ID, trace.WithAttributes(
		attribute.String("pamon.target", t.ID), attribute.String("pamon.location", m.Prober.Location)))
	defer span.End()
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	span.AddEvent("slot acquired")
	r := m.Prober.Check(ctx, t)
	<-m.sem
	if ctx.Err() != nil {
		return
	}
	mScheduled.Inc()
	late := time.Since(slot) > every
	if late {
		mLate.Inc()
	}
	span.SetAttributes(attribute.String("pamon.status", string(r.Status)), attribute.Int64("pamon.latency_ms", r.LatencyMS),
		attribute.String("pamon.code", r.Code), attribute.Bool("pamon.late", late))
	if r.Status.Severity() == 2 {
		span.SetStatus(codes.Error, r.Reason)
	}
	m.lastCheck.Store(time.Now().Unix())
	observe(r)
	log := m.Log.With("target", t.ID, "status", r.Status, "latency_ms", r.LatencyMS)
	if sc := span.SpanContext(); sc.IsValid() {
		log = log.With("trace_id", sc.TraceID().String()) // logs join to traces
	}
	if r.Status != probe.StatusOK {
		log = log.With("code", r.Code, "reason", r.Reason)
	}
	log.Info("check")
	m.Sink.Record(ctx, t, r)
}
