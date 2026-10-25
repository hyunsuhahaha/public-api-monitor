package insight

import (
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func TestPatterns(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, config.KST)
	in := func(startH, mins int, sev int) *incident.Incident {
		s := time.Date(2026, 10, 20, startH, 0, 0, 0, config.KST)
		return &incident.Incident{Started: s, Ended: s.Add(time.Duration(mins) * time.Minute), Severity: sev}
	}
	if p := Patterns(nil, 30, now); p.Kind != "none" || !strings.Contains(p.Summary, "없습니다") {
		t.Fatalf("%+v", p)
	}
	blips := Patterns([]*incident.Incident{in(2, 5, 2), in(3, 10, 2), in(14, 10, 1)}, 30, now)
	if blips.Kind != "blips" || blips.MedianMin != 10 || blips.ShortShare != 100 || blips.Night != 66 || !strings.Contains(blips.Summary, "새벽") {
		t.Fatalf("%+v", blips)
	}
	long := Patterns([]*incident.Incident{in(9, 180, 2), in(10, 90, 2)}, 30, now)
	if long.Kind != "long" || long.LongestMin != 180 {
		t.Fatalf("%+v", long)
	}
}

func TestPeakHours(t *testing.T) {
	var rows []store.HourRow
	day0 := time.Date(2026, 10, 1, 0, 0, 0, 0, config.KST)
	for d := 0; d < 10; d++ {
		for h := 0; h < 24; h++ {
			hour := day0.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour).UnixMilli()
			r := store.HourRow{Target: "x", Location: "*", Hour: hour}
			r.Counted, r.Healthy = 12, 12
			if h == 18 {
				r.Healthy, r.Outage = 9, 3 // 25% at 18:00
			}
			hist := store.NewHist()
			hist[5] = 12 // ~150ms
			if h == 18 {
				hist[5], hist[14] = 6, 6 // ~3s
			}
			rows = append(rows, r, store.HourRow{Target: "x", Location: store.AllLocations, Hour: hour, Hist: hist})
		}
	}
	p := PeakHours(rows, 30)
	if len(p.Worst) != 1 || p.Worst[0].Hour != 18 || p.Worst[0].FailPct != 25 || !strings.Contains(p.Summary, "18시대") {
		t.Fatalf("%+v", p.Worst)
	}
	if q := PeakHours(nil, 30); len(q.Worst) != 0 || !strings.Contains(q.Summary, "아직") {
		t.Fatal(q.Summary)
	}
}

func TestAdvise(t *testing.T) {
	next := time.Now().Add(3 * time.Minute)
	end := time.Now().Add(time.Hour)
	cases := map[string]Advice{
		"skip":    Advise("failure_observed", next, &end, Pattern{}),
		"wait":    Advise("failure_observed", next, nil, Pattern{}),
		"backoff": Advise("degraded_observed", next, nil, Pattern{}),
		"retry":   Advise("no_failure_observed", next, nil, Pattern{}),
		"unknown": Advise("unknown", next, nil, Pattern{}),
	}
	for want, a := range cases {
		if a.Action != want {
			t.Errorf("want %s, got %+v", want, a)
		}
	}
	if a := Advise("failure_observed", next, nil, Pattern{Kind: "long"}); !strings.Contains(a.Reason, "fallback") || a.RetryAfter == nil {
		t.Fatalf("%+v", a)
	}
}

func TestAccuracy(t *testing.T) {
	tr := store.Transition{
		"ok":     {"ok": 96, "down": 4, "client": 9},
		"down":   {"down": 7, "ok": 3},
		"slow":   {"ok": 1, "slow": 1},
		"client": {"ok": 5},
	}
	a := AccuracyOf(tr, 30)
	if a.Retry != 100 || a.RetryPct != 96 || a.Wait != 10 || a.WaitPct != 70 || a.Recoveries != 3 || a.Backoff != 2 || a.BackoffPct != 100 {
		t.Fatalf("%+v", a)
	}
	if e := AccuracyOf(nil, 30); e.WaitPct != -1 || e.RetryPct != -1 {
		t.Fatalf("%+v", e)
	}
	m := Merge(map[string]store.Transition{"a": {"ok": {"ok": 1}}, "b": {"ok": {"ok": 2, "down": 1}}})
	if m["ok"]["ok"] != 3 || m["ok"]["down"] != 1 {
		t.Fatal(m)
	}
}
