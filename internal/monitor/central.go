package monitor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/quorum"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
	"github.com/hyunsuhahaha/public-api-monitor/internal/telemetry"
)

var (
	mUp = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_target_up", Help: "Combined verdict: 1 healthy, 0.5 degraded, 0 outage, -1 unknown (our side).",
	}, []string{"target"})
	// verdicts per target and status: the SLI the per-API burn-rate rules are built on
	mVerdicts = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_verdicts_total", Help: "Combined verdicts by target and status.",
	}, []string{"target", "status"})
	mSLO = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_target_slo", Help: "Availability objective of each target (targets.yaml slo).",
	}, []string{"target"})
	mVoters = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "pamon_target_voters", Help: "Locations that counted in the last verdict.",
	}, []string{"target"})
	mOpen = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pamon_open_incidents", Help: "Incidents currently open.",
	})
	mSchemaChanges = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_schema_changes_total", Help: "Response fields added to or removed from an API.",
	}, []string{"target", "kind"})
	mIngested = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "pamon_ingested_results_total", Help: "Results received from remote probes.",
	}, []string{"location"})
	mStoreErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_store_errors_total", Help: "Failed writes to the database.",
	})
)

// Notifier is told about incident changes (webhooks etc.).
type Notifier interface {
	Notify(ctx context.Context, ev incident.Event, in *incident.Incident, t *config.Target)
}

// SchemaNotifier, if the notifier also implements it, is told about response-field changes.
type SchemaNotifier interface {
	NotifySchema(ctx context.Context, e store.SchemaEvent, t *config.Target)
}

// Central stores every check (its own and remote probes'), combines locations into a
// verdict after each of its own checks, and runs incidents on the verdicts.
type Central struct {
	Cfg      *config.Config
	Store    *store.Store
	Board    *quorum.Board
	Tracker  *incident.Tracker
	Notifier Notifier
	Log      *slog.Logger
	Location string // this process's own location: its checks trigger the verdicts

	targets map[string]*config.Target
}

// Start restores open incidents and indexes the targets. Call before Run.
func (c *Central) Start(ctx context.Context) error {
	c.targets = map[string]*config.Target{}
	for i := range c.Cfg.Targets {
		t := &c.Cfg.Targets[i]
		c.targets[t.ID] = t
		mSLO.WithLabelValues(t.ID).Set(t.SLO)
		// start every status series at 0 so rate() works from the first verdict
		for _, st := range []probe.Status{probe.StatusOK, probe.StatusSlow, probe.StatusStale, probe.StatusError, probe.StatusDown, probe.StatusClient, probe.StatusMaint} {
			mVerdicts.WithLabelValues(t.ID, string(st))
		}
	}
	open, err := c.Store.OpenIncidents(ctx)
	if err != nil {
		return err
	}
	for _, in := range open {
		c.Tracker.Restore(in)
	}
	mOpen.Set(float64(len(open)))
	return nil
}

// Record handles one of the central's own checks.
func (c *Central) Record(ctx context.Context, t *config.Target, r probe.Result) {
	c.save(ctx, r)
	dctx, span := telemetry.Tracer().Start(ctx, "decide "+t.ID)
	c.decide(dctx, t, r.At)
	span.End()
	if r.Shape != nil {
		c.learnShape(ctx, t, r)
	}
}

// learnShape records the answer's field paths; fields never seen before (after the first
// answer, which is the baseline) are a schema change.
func (c *Central) learnShape(ctx context.Context, t *config.Target, r probe.Result) {
	added, had, err := c.Store.ObservePaths(ctx, t.ID, r.Shape, r.At, c.Cfg.SchemaWarmup.Duration)
	if err != nil {
		mStoreErrors.Inc()
		c.Log.Error("save shape", "err", err)
		return
	}
	if had && len(added) > 0 {
		c.schemaChange(ctx, t, store.SchemaEvent{Target: t.ID, At: r.At, Kind: "added", Paths: added})
	}
}

func (c *Central) schemaChange(ctx context.Context, t *config.Target, e store.SchemaEvent) {
	mSchemaChanges.WithLabelValues(t.ID, e.Kind).Inc()
	if err := c.Store.SaveSchemaEvent(ctx, &e); err != nil {
		mStoreErrors.Inc()
		c.Log.Error("save schema event", "err", err)
	}
	c.Log.Warn("schema change", "target", t.ID, "kind", e.Kind, "paths", e.Paths)
	if n, ok := c.Notifier.(SchemaNotifier); ok {
		n.NotifySchema(ctx, e, t)
	}
}

// Schema sweeps once an hour for fields gone from an API that still answers with data:
// missing for 24 h is a removal (shorter would flag optional fields that are just absent
// from the current data).
func (c *Central) Schema(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		gone, err := c.Store.StalePaths(ctx, time.Now(), 24*time.Hour, time.Hour)
		if err != nil {
			c.Log.Error("schema sweep", "err", err)
			continue
		}
		for id, paths := range gone {
			if t := c.targets[id]; t != nil {
				c.schemaChange(ctx, t, store.SchemaEvent{Target: id, At: time.Now(), Kind: "removed", Paths: paths})
			}
		}
	}
}

func (c *Central) save(ctx context.Context, r probe.Result) {
	if err := c.Store.SaveCheck(ctx, r); err != nil {
		mStoreErrors.Inc()
		c.Log.Error("save check", "err", err)
	}
	c.Board.Update(r)
}

// ErrUnknownTarget: an ingested result names a target this config does not have.
var ErrUnknownTarget = errors.New("unknown target")

// Ingest takes results from a remote probe. They count in the next verdict of their
// target (taken when the central's own check of it finishes).
func (c *Central) Ingest(ctx context.Context, results []probe.Result) error {
	ctx, span := telemetry.Tracer().Start(ctx, "ingest", trace.WithAttributes(attribute.Int("pamon.results", len(results))))
	defer span.End()
	now := time.Now()
	for _, r := range results {
		if c.targets[r.TargetID] == nil {
			return fmt.Errorf("%w: %q", ErrUnknownTarget, r.TargetID)
		}
		if r.At.After(now.Add(2*time.Minute)) || r.At.Before(now.Add(-time.Hour)) {
			return fmt.Errorf("result for %q at %s: clock too far off", r.TargetID, r.At.Format(time.RFC3339))
		}
	}
	for _, r := range results {
		observe(r)
		mIngested.WithLabelValues(r.Location).Inc()
		c.save(ctx, r)
	}
	return nil
}

func (c *Central) decide(ctx context.Context, t *config.Target, at time.Time) {
	// a location counts if it checked within 2.5 intervals (one missed slot is fine)
	fresh := c.Board.Fresh(t.ID, at, t.Interval.Duration*5/2)
	v := quorum.Decide(t.ID, at, fresh)
	if m := c.Cfg.MaintenanceAt(t, v.At); m != nil {
		// kept (with what was really seen) but out of uptime, SLOs and incidents
		reason := "점검 중: " + m.Note
		if v.Status != probe.StatusOK {
			reason += " — 실제 " + string(v.Status)
			if r := strings.TrimSpace(v.Reason); r != "" {
				reason += " " + r
			}
		}
		v.Status, v.Reason = probe.StatusMaint, reason
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("pamon.verdict", string(v.Status)),
		attribute.Int("pamon.voters", v.Voters), attribute.Int("pamon.bad", v.Bad), attribute.String("pamon.reason", v.Reason))
	if err := c.Store.SaveVerdict(ctx, store.Verdict{TargetID: t.ID, At: v.At, Status: v.Status, Reason: v.Reason, Voters: v.Voters}); err != nil {
		mStoreErrors.Inc()
		c.Log.Error("save verdict", "err", err)
	}
	mVoters.WithLabelValues(t.ID).Set(float64(v.Voters))
	mVerdicts.WithLabelValues(t.ID, string(v.Status)).Inc()
	switch {
	case !v.Status.Counts():
		mUp.WithLabelValues(t.ID).Set(-1)
	case v.Status.Severity() == 0:
		mUp.WithLabelValues(t.ID).Set(1)
	case v.Status.Severity() == 1:
		mUp.WithLabelValues(t.ID).Set(0.5)
	default:
		mUp.WithLabelValues(t.ID).Set(0)
	}
	ev, in := c.Tracker.Observe(v.Result())
	if ev == incident.None {
		return
	}
	trace.SpanFromContext(ctx).AddEvent("incident", trace.WithAttributes(
		attribute.Int("pamon.event", int(ev)), attribute.String("pamon.reason", in.Reason)))
	if err := c.Store.SaveIncident(ctx, in); err != nil {
		mStoreErrors.Inc()
		c.Log.Error("save incident", "err", err)
	}
	switch ev {
	case incident.Opened:
		mOpen.Inc()
		c.Log.Warn("incident opened", "target", t.ID, "id", in.ID, "reason", in.Reason)
	case incident.Reopened:
		mOpen.Inc()
		c.Log.Warn("incident reopened", "target", t.ID, "id", in.ID, "flaps", in.Flaps, "reason", in.LastReason)
	case incident.Closed:
		mOpen.Dec()
		c.Log.Info("incident closed", "target", t.ID, "id", in.ID, "duration", in.Ended.Sub(in.Started).Round(time.Second))
	}
	if c.Notifier != nil {
		c.Notifier.Notify(ctx, ev, in, t)
	}
}

// Prune deletes old checks and verdicts once a day.
func (c *Central) Prune(ctx context.Context, keep time.Duration) {
	tick := time.NewTicker(24 * time.Hour)
	defer tick.Stop()
	for {
		if n, err := c.Store.Prune(ctx, time.Now().Add(-keep)); err != nil {
			c.Log.Error("prune", "err", err)
		} else if n > 0 {
			c.Log.Info("pruned old rows", "rows", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
