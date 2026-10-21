package web

import (
	"context"
	"net/http"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Long ranges (bucket a whole number of hours: 7d, 30d) are served from the hourly
// rollups (store/rollup.go) instead of raw rows: the 30-day overview took 2 s scanning
// ~780k checks. Latency percentiles here are histogram estimates.

func useRollups(step time.Duration) bool { return step >= time.Hour && step%time.Hour == 0 }

// bucketsFromHours folds hourly rows into the range's buckets: verdict counts ("*"),
// latency of all locations merged ("ALL"), and per location (other rows, series only).
// allCounts are the raw checks of all locations (checks per minute).
func bucketsFromHours(rows []store.HourRow, from time.Time, step time.Duration, n int) (verdicts map[string][]store.Bucket, hists map[string][]store.Hist,
	locHists map[string]map[string][]store.Hist, locCounts map[string]map[string][]store.Bucket, totals map[string]store.Hist, allCounts map[string][]store.Bucket) {
	verdicts, hists, allCounts = map[string][]store.Bucket{}, map[string][]store.Hist{}, map[string][]store.Bucket{}
	locHists, locCounts, totals = map[string]map[string][]store.Hist{}, map[string]map[string][]store.Bucket{}, map[string]store.Hist{}
	for _, r := range rows {
		i := int((r.Hour - from.UnixMilli()) / step.Milliseconds())
		if i < 0 || i >= n {
			continue
		}
		if r.Location == "*" {
			if verdicts[r.Target] == nil {
				verdicts[r.Target] = make([]store.Bucket, n)
			}
			b := &verdicts[r.Target][i]
			b.Counted += r.Counted
			b.Healthy += r.Healthy
			b.Degraded += r.Degraded
			b.Outage += r.Outage
			continue
		}
		if hists[r.Target] == nil {
			hists[r.Target] = make([]store.Hist, n)
			locHists[r.Target] = map[string][]store.Hist{}
			locCounts[r.Target] = map[string][]store.Bucket{}
			totals[r.Target] = store.NewHist()
		}
		if r.Location == store.AllLocations {
			if r.Hist != nil {
				if hists[r.Target][i] == nil {
					hists[r.Target][i] = store.NewHist()
				}
				hists[r.Target][i].Merge(r.Hist)
				totals[r.Target].Merge(r.Hist)
			}
			if allCounts[r.Target] == nil {
				allCounts[r.Target] = make([]store.Bucket, n)
			}
			b := &allCounts[r.Target][i]
			b.Counted += r.Counted
			continue
		}
		if locHists[r.Target][r.Location] == nil {
			locHists[r.Target][r.Location] = make([]store.Hist, n)
			locCounts[r.Target][r.Location] = make([]store.Bucket, n)
		}
		lb := &locCounts[r.Target][r.Location][i]
		lb.Counted += r.Counted
		lb.Healthy += r.Healthy
		lb.Degraded += r.Degraded
		lb.Outage += r.Outage
		if r.Hist != nil {
			if locHists[r.Target][r.Location][i] == nil {
				locHists[r.Target][r.Location][i] = store.NewHist()
			}
			locHists[r.Target][r.Location][i].Merge(r.Hist)
		}
	}
	return
}

func q(h store.Hist, p float64) *int64 {
	if h == nil || h.Count() == 0 {
		return nil
	}
	v := h.Quantile(p)
	return &v
}

func (s *Server) overviewRollup(ctx context.Context, w http.ResponseWriter, name string, from, to time.Time, step time.Duration) {
	rows, err := s.Store.Hourly(ctx, "", from, to, true)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	clients, err := s.Store.ClientCounts(ctx, from, to)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	verdicts, _ := s.Store.LatestVerdicts(ctx)
	byLoc, _ := s.Store.LatestByLocation(ctx, to.Add(-time.Hour))
	burns := s.burnRates(ctx, time.Now())
	open, _ := s.Store.OpenIncidents(ctx)
	n := int(to.Sub(from)/step) + 1
	vb, hists, _, _, totals, allCounts := bucketsFromHours(rows, from, step, n)

	out := overviewResponse{Range: name, From: from.Unix(), To: to.Unix(), Step: int64(step.Seconds()), Counts: map[string]int{}, Maintenance: s.maintenance(from, time.Now()), Silences: s.silences(ctx), GeneratedAt: time.Now()}
	fleet := store.NewHist()
	var sumAvail, sloSum float64
	var withData, verdictN, clientChecks int
	var checks int64
	for _, t := range s.Cfg.Targets {
		row := overviewTarget{ID: t.ID, Name: t.Name, Provider: t.Provider, Docs: t.Docs, Category: t.Category, SLO: t.SLO,
			Status: "unknown", Availability: -1, Spark: make([]*int64, n), States: make([]int, n)}
		for i := range row.States {
			row.States[i] = -1
		}
		if v, ok := verdicts[t.ID]; ok {
			row.Status, row.Reason = string(v.Status), v.Reason
		}
		var counted, healthy int
		for i, b := range vb[t.ID] {
			row.States[i] = state(b)
			counted += b.Counted
			healthy += b.Healthy
		}
		if counted > 0 {
			row.Availability = float64(healthy) / float64(counted)
			sumAvail += row.Availability
			sloSum += t.SLO
			withData++
		}
		row.Checks = counted
		row.ClientChecks = clients[t.ID]
		row.Coverage = coverage(counted, row.ClientChecks)
		clientChecks += row.ClientChecks
		verdictN += counted
		row.BudgetLeft = budgetLeft(row.Availability, t.SLO)
		for i, h := range hists[t.ID] {
			row.Spark[i] = q(h, 0.95)
		}
		if tot := totals[t.ID]; tot != nil {
			row.P50, row.P95 = tot.Quantile(0.5), tot.Quantile(0.95)
			fleet.Merge(tot)
		}
		for _, b := range allCounts[t.ID] {
			checks += int64(b.Counted)
		}
		row.Locations = []locState{}
		for _, x := range byLoc[t.ID] {
			row.Locations = append(row.Locations, locState{x.Location, string(x.Status), x.LatencyMS})
		}
		countStatus(out.Counts, row.Status)
		b := burns[t.ID]
		row.Burn1h, row.Burn6h, row.Burn5m, row.Burn30m = b[0], b[1], b[2], b[3]
		out.Rows = append(out.Rows, row)
	}
	out.Fleet = fleetSummary(sumAvail, sloSum, withData, fleet.Quantile(0.5), fleet.Quantile(0.95), fleet.Quantile(0.99),
		float64(checks)/to.Sub(from).Minutes(), len(open), verdictN)
	out.Fleet["coverage"], out.Fleet["clientChecks"] = coverage(verdictN, clientChecks), clientChecks
	writeJSON(w, out)
}

func (s *Server) seriesRollup(ctx context.Context, w http.ResponseWriter, id, name string, from, to time.Time, step time.Duration) {
	rows, err := s.Store.Hourly(ctx, id, from, to, false)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	recent, _ := s.Store.RecentChecks(ctx, id, 40)
	n := int(to.Sub(from)/step) + 1
	vb, hists, locHists, locCounts, _, _ := bucketsFromHours(rows, from, step, n)
	ts := make([]int64, n)
	for i := range ts {
		ts[i] = from.Add(time.Duration(i) * step).Unix()
	}
	p50, p95, mx := make([]*int64, n), make([]*int64, n), make([]*int64, n)
	for i, h := range hists[id] {
		p50[i], p95[i], mx[i] = q(h, 0.5), q(h, 0.95), q(h, 1)
	}
	byLoc := map[string][]*int64{}
	for loc, hs := range locHists[id] {
		byLoc[loc] = make([]*int64, n)
		for i, h := range hs {
			byLoc[loc][i] = q(h, 0.95)
		}
	}
	locStates := map[string][]int{}
	for loc, bs := range locCounts[id] {
		locStates[loc] = make([]int, n)
		for i, b := range bs {
			locStates[loc][i] = state(b)
		}
	}
	states := make([]int, n)
	for i := range states {
		states[i] = -1
	}
	counted, bad := make([]int, n), make([]int, n)
	for i, b := range vb[id] {
		states[i] = state(b)
		counted[i], bad[i] = b.Counted, b.Counted-b.Healthy
	}
	slowAfter, limit, usual := s.slowLines(id)
	writeJSON(w, map[string]any{
		"target": id, "range": name, "stepSec": int64(step.Seconds()), "estimated": true, "slo": s.slo(id),
		"slowAfterMs": slowAfter, "slowLimitMs": limit, "usualMs": usual, "followUp": s.followUp(id),
		"t": ts, "p50": p50, "p95": p95, "max": mx, "states": states, "counted": counted, "bad": bad,
		"byLocation": byLoc, "locationStates": locStates, "recent": recentChecks(recent),
	})
}

func countStatus(c map[string]int, status string) {
	switch probe.Status(status).Severity() {
	case 2:
		c["down"]++
	case 1:
		c["degraded"]++
	default:
		if status == "ok" {
			c["up"]++
		} else if status == "maint" {
			c["maint"]++
		} else {
			c["unknown"]++
		}
	}
}
