// Package monitor schedules the checks and wires results into storage, incidents,
// metrics and notifications.
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

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

var (
	mChecks = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_checks_total", Help: "Checks by target and status.",
	}, []string{"target", "status"})
	mLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "pamon_check_latency_seconds",
		Help:    "Response time of checks that got an answer.",
		Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 8, 13},
	}, []string{"target"})
	mUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_target_up", Help: "1 healthy, 0.5 degraded, 0 outage, -1 unknown (our side).",
	}, []string{"target"})
	mLastCheck = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_last_check_timestamp_seconds", Help: "When each target was last checked.",
	}, []string{"target"})
	mOpen = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pamon_open_incidents", Help: "Incidents currently open.",
	})
	// Our own SLI: a check that finished later than its slot plus one interval is a late
	// check (the monitor fell behind or stopped). Alert on the rate of these.
	mLate = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_late_checks_total", Help: "Checks that ran more than one interval late.",
	})
	mStoreErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_store_errors_total", Help: "Failed writes to the database.",
	})
)

// Notifier is told about incident changes (webhooks etc.).
type Notifier interface {
	Notify(ctx context.Context, ev incident.Event, in *incident.Incident, t *config.Target)
}

type Monitor struct {
	Cfg      *config.Config
	Prober   *probe.Prober
	Store    *store.Store
	Tracker  *incident.Tracker
	Notifier Notifier
	Log      *slog.Logger
	// MaxParallel limits concurrent requests (data.go.kr shares one gateway).
	MaxParallel int

	lastCycle atomic.Int64 // unix seconds of the last finished check
	sem       chan struct{}
}

// LastCheck is when any check last finished (readiness).
func (m *Monitor) LastCheck() time.Time { return time.Unix(m.lastCycle.Load(), 0) }

// Run checks every target on its interval until ctx ends. Each target gets a fixed
// offset inside its interval (hash of its id), so the gateway sees an even trickle, not a
// burst every five minutes.
func (m *Monitor) Run(ctx context.Context) {
	if m.MaxParallel <= 0 {
		m.MaxParallel = 4
	}
	m.sem = make(chan struct{}, m.MaxParallel)
	m.lastCycle.Store(time.Now().Unix())
	if open, err := m.Store.OpenIncidents(ctx); err == nil {
		for _, in := range open {
			m.Tracker.Restore(in)
		}
		mOpen.Set(float64(len(open)))
	}
	var wg sync.WaitGroup
	for i := range m.Cfg.Targets {
		t := &m.Cfg.Targets[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.loop(ctx, t)
		}()
	}
	wg.Wait()
}

func offset(id string, every time.Duration) time.Duration {
	h := fnv.New32a()
	h.Write([]byte(id))
	return time.Duration(uint64(h.Sum32()) % uint64(every))
}

func (m *Monitor) loop(ctx context.Context, t *config.Target) {
	every := t.Interval.Duration
	// first check soon after start (spread over 30 s), then on the target's slot
	first := offset(t.ID, 30*time.Second)
	timer := time.NewTimer(first)
	defer timer.Stop()
	next := time.Now().Add(first)
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.checkOnce(ctx, t, next, every)
		next = next.Add(every)
		if now := time.Now(); next.Before(now) {
			next = now.Add(every) // fell behind (sleep, long outage): skip missed slots
		}
		timer.Reset(time.Until(next))
	}
}

func (m *Monitor) checkOnce(ctx context.Context, t *config.Target, slot time.Time, every time.Duration) {
	select {
	case m.sem <- struct{}{}:
	case <-ctx.Done():
		return
	}
	r := m.Prober.Check(ctx, t)
	<-m.sem
	if ctx.Err() != nil {
		return
	}
	if time.Since(slot) > every {
		mLate.Inc()
	}
	m.record(ctx, t, r)
}

func (m *Monitor) record(ctx context.Context, t *config.Target, r probe.Result) {
	m.lastCycle.Store(time.Now().Unix())
	mChecks.WithLabelValues(t.ID, string(r.Status)).Inc()
	mLastCheck.WithLabelValues(t.ID).Set(float64(r.At.Unix()))
	if r.HTTPStatus > 0 {
		mLatency.WithLabelValues(t.ID).Observe(float64(r.LatencyMS) / 1000)
	}
	switch {
	case !r.Status.Counts():
		mUp.WithLabelValues(t.ID).Set(-1)
	case r.Status.Severity() == 0:
		mUp.WithLabelValues(t.ID).Set(1)
	case r.Status.Severity() == 1:
		mUp.WithLabelValues(t.ID).Set(0.5)
	default:
		mUp.WithLabelValues(t.ID).Set(0)
	}
	log := m.Log.With("target", t.ID, "status", r.Status, "latency_ms", r.LatencyMS)
	if r.Status != probe.StatusOK {
		log = log.With("code", r.Code, "reason", r.Reason)
	}
	log.Info("check")

	if err := m.Store.SaveCheck(ctx, r); err != nil {
		mStoreErrors.Inc()
		m.Log.Error("save check", "err", err)
	}
	ev, in := m.Tracker.Observe(r)
	if ev == incident.None {
		return
	}
	if err := m.Store.SaveIncident(ctx, in); err != nil {
		mStoreErrors.Inc()
		m.Log.Error("save incident", "err", err)
	}
	switch ev {
	case incident.Opened:
		mOpen.Inc()
		m.Log.Warn("incident opened", "target", t.ID, "id", in.ID, "reason", in.Reason)
	case incident.Closed:
		mOpen.Dec()
		m.Log.Info("incident closed", "target", t.ID, "id", in.ID, "duration", in.Ended.Sub(in.Started).Round(time.Second))
	}
	if m.Notifier != nil {
		m.Notifier.Notify(ctx, ev, in, t)
	}
}

// Prune deletes old checks once a day.
func (m *Monitor) Prune(ctx context.Context, keep time.Duration) {
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		if n, err := m.Store.Prune(ctx, time.Now().Add(-keep)); err != nil {
			m.Log.Error("prune", "err", err)
		} else if n > 0 {
			m.Log.Info("pruned old checks", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
