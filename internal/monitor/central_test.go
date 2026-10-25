package monitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/quorum"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

type events struct{ got []incident.Event }

func (e *events) Notify(_ context.Context, ev incident.Event, _ *incident.Incident, _ *config.Target) {
	e.got = append(e.got, ev)
}

func newCentral(t *testing.T) (*Central, *events) {
	t.Helper()
	cfg, err := config.Parse([]byte("targets:\n  - id: a\n    url: http://x\n"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ev := &events{}
	c := &Central{Cfg: cfg, Store: st, Board: quorum.NewBoard(), Tracker: incident.NewTracker(2, 2),
		Notifier: ev, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Location: "central"}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, ev
}

// One slot: the remote probe reports, then the central's own check triggers the verdict.
func slot(t *testing.T, c *Central, at time.Time, central, remote probe.Status) {
	t.Helper()
	ctx := context.Background()
	tg := &c.Cfg.Targets[0]
	if err := c.Ingest(ctx, []probe.Result{{TargetID: "a", Location: "remote", At: at, Status: remote}}); err != nil {
		t.Fatal(err)
	}
	c.Record(ctx, tg, probe.Result{TargetID: "a", Location: "central", At: at, Status: central})
}

func TestRegionalFailureOpensNothing(t *testing.T) {
	c, ev := newCentral(t)
	start := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 4; i++ {
		slot(t, c, start.Add(time.Duration(i)*5*time.Minute), probe.StatusOK, probe.StatusDown)
	}
	if len(ev.got) != 0 {
		t.Fatalf("one location failing must not open an incident: %v", ev.got)
	}
	open, _ := c.Store.OpenIncidents(context.Background())
	if len(open) != 0 {
		t.Fatalf("open: %+v", open)
	}
}

func TestOutageEverywhereOpensAndCloses(t *testing.T) {
	c, ev := newCentral(t)
	start := time.Now().Add(-40 * time.Minute)
	statuses := []probe.Status{probe.StatusDown, probe.StatusDown, probe.StatusDown, probe.StatusOK, probe.StatusOK}
	for i, s := range statuses {
		slot(t, c, start.Add(time.Duration(i)*5*time.Minute), s, s)
	}
	if len(ev.got) != 2 || ev.got[0] != incident.Opened || ev.got[1] != incident.Closed {
		t.Fatalf("events: %v", ev.got)
	}
	days, err := c.Store.Days(context.Background(), start.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var counted, healthy int
	for _, d := range days["a"] {
		counted += d.Counted
		healthy += d.Healthy
	}
	if counted != 5 || healthy != 2 {
		t.Fatalf("uptime comes from verdicts, one per slot: %d/%d", healthy, counted)
	}
}

func TestIngestRejects(t *testing.T) {
	c, _ := newCentral(t)
	ctx := context.Background()
	if err := c.Ingest(ctx, []probe.Result{{TargetID: "nope", Location: "r", At: time.Now(), Status: probe.StatusOK}}); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("unknown target: %v", err)
	}
	if err := c.Ingest(ctx, []probe.Result{{TargetID: "a", Location: "r", At: time.Now().Add(time.Hour), Status: probe.StatusOK}}); err == nil {
		t.Fatal("future timestamp accepted")
	}
}

func TestFirstSlotAligned(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 3, 0, 0, time.UTC)
	a := firstSlot("x", 5*time.Minute, now)
	b := firstSlot("x", 5*time.Minute, now.Add(17*time.Second))
	if !a.After(now) || a.Sub(now) > 5*time.Minute || !a.Equal(b) {
		t.Fatalf("slots: %v %v", a, b)
	}
}

// An outage inside an announced maintenance window opens nothing, pages nobody and leaves
// uptime alone; the same outage right after the window is an incident as usual.
func TestMaintenanceWindow(t *testing.T) {
	c, ev := newCentral(t)
	start := time.Now().Add(-40 * time.Minute).Truncate(time.Minute)
	c.Cfg.Maintenance = []config.Maintenance{{Match: []string{"a"}, Start: start.Add(-time.Minute), End: start.Add(16 * time.Minute), Note: "정기 점검"}}
	for i := 0; i < 4; i++ { // 0, 5, 10, 15 min: inside
		slot(t, c, start.Add(time.Duration(i)*5*time.Minute), probe.StatusDown, probe.StatusDown)
	}
	if len(ev.got) != 0 {
		t.Fatalf("maintenance must not page: %v", ev.got)
	}
	days, _ := c.Store.Days(context.Background(), start.Add(-time.Hour))
	if len(days["a"]) != 0 && days["a"][0].Counted != 0 {
		t.Fatalf("maintenance verdicts must not count: %+v", days["a"])
	}
	vs, _ := c.Store.VerdictsBetween(context.Background(), "a", start.Add(-time.Minute), start.Add(time.Minute), 10)
	if len(vs) != 1 || vs[0].Status != probe.StatusMaint || vs[0].Reason != "점검 중: 정기 점검 — 실제 down (central, remote)" {
		t.Fatalf("kept with what was seen: %+v", vs)
	}
	for i := 4; i < 6; i++ { // 20, 25 min: after
		slot(t, c, start.Add(time.Duration(i)*5*time.Minute), probe.StatusDown, probe.StatusDown)
	}
	if len(ev.got) != 1 || ev.got[0] != incident.Opened {
		t.Fatalf("after the window, an outage pages: %v", ev.got)
	}
}

// A silenced API still gets its incident; nobody is paged.
func TestSilencedPagesNobody(t *testing.T) {
	c, ev := newCentral(t)
	now := time.Now()
	c.Store.AddSilence(context.Background(), &store.Silence{Target: "a", Created: now.Add(-time.Minute), Until: now.Add(time.Hour), Author: "x"})
	start := now.Add(-20 * time.Minute)
	for i := 0; i < 2; i++ {
		slot(t, c, start.Add(time.Duration(i)*5*time.Minute), probe.StatusDown, probe.StatusDown)
	}
	if len(ev.got) != 0 {
		t.Fatalf("silenced: paged %v", ev.got)
	}
	if open, _ := c.Store.OpenIncidents(context.Background()); len(open) != 1 {
		t.Fatalf("the incident is still recorded: %+v", open)
	}
}

// A price that turns from a number into a string after the warm-up is a schema event;
// a field that is sometimes null is not.
func TestValueKindChange(t *testing.T) {
	c, _ := newCentral(t)
	c.Cfg.SchemaWarmup.Duration = time.Hour
	ctx := context.Background()
	tg := &c.Cfg.Targets[0]
	at := time.Now().Add(-3 * time.Hour)
	answer := func(at time.Time, body string) {
		c.Record(ctx, tg, probe.Result{TargetID: "a", Location: "central", At: at, Status: probe.StatusOK,
			Shape: probe.Shape([]byte(body)), Types: probe.ShapeTypes([]byte(body))})
	}
	answer(at, `{"price":42,"memo":"x"}`)
	answer(at.Add(2*time.Hour), `{"price":42,"memo":null}`)
	answer(at.Add(2*time.Hour+5*time.Minute), `{"price":"42","memo":"y"}`)
	ev, _ := c.Store.SchemaEvents(ctx, "a", at, 10)
	if len(ev) != 1 || ev[0].Kind != "type_changed" || len(ev[0].Paths) != 1 || ev[0].Paths[0] != "price: 숫자 → 문자열" {
		t.Fatalf("events: %+v", ev)
	}
}
