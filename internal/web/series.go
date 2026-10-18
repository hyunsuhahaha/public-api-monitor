package web

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Ranges the dashboard offers, and the bucket size for each (about 60-90 buckets).
var ranges = map[string]struct{ span, step time.Duration }{
	"1h":  {time.Hour, time.Minute},
	"6h":  {6 * time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, time.Hour}, // hour buckets: served from the rollups
	"7d":  {7 * 24 * time.Hour, 2 * time.Hour},
	"30d": {30 * 24 * time.Hour, 8 * time.Hour},
}

func (s *Server) parseRange(r *http.Request) (string, time.Time, time.Time, time.Duration) {
	name := r.URL.Query().Get("range")
	rg, ok := ranges[name]
	if !ok {
		name, rg = "24h", ranges["24h"]
	}
	// a bucket is never shorter than the check interval (else every other one is empty)
	if iv := s.Cfg.Interval.Duration; rg.step < iv {
		rg.step = iv
	}
	to := time.Now()
	// buckets aligned to the step, so a refresh doesn't shift them
	from := to.Add(-rg.span).Truncate(rg.step)
	return name, from, to, rg.step
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}

// Bucket states for heatmaps: -1 no data, 0 healthy, 1 degraded, 2 outage.
func state(b store.Bucket) int {
	switch {
	case b.Counted == 0:
		return -1
	case b.Outage > 0:
		return 2
	case b.Degraded > 0:
		return 1
	}
	return 0
}

// budgetLeft: share of the error budget not yet spent over the range (can go negative).
func budgetLeft(avail, slo float64) float64 {
	if avail < 0 {
		return 1
	}
	return 1 - (1-avail)/(1-slo)
}

func pctl(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	return v[int(p*float64(len(v)-1)+0.5)]
}

type overviewTarget struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Provider     string     `json:"provider"`
	Category     string     `json:"category"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"`
	SLO          float64    `json:"slo"`
	Availability float64    `json:"availability"` // over the range, -1 without data
	BudgetLeft   float64    `json:"budgetLeft"`
	P50          int64      `json:"p50"`
	P95          int64      `json:"p95"`
	Spark        []*int64   `json:"spark"`  // p95 per bucket (null = no answer)
	States       []int      `json:"states"` // per bucket
	Checks       int        `json:"checks"`
	Locations    []locState `json:"locations"`
}

type overviewResponse struct {
	Range  string           `json:"range"`
	From   int64            `json:"from"`
	To     int64            `json:"to"`
	Step   int64            `json:"stepSec"`
	Fleet  map[string]any   `json:"fleet"`
	Counts map[string]int   `json:"counts"`
	Rows   []overviewTarget `json:"targets"`
}

func fleetSummary(sumAvail, sloSum float64, withData int, p50, p95, p99 int64, perMin float64, open, verdicts int) map[string]any {
	avail, slo := -1.0, 0.99
	if withData > 0 {
		avail, slo = sumAvail/float64(withData), sloSum/float64(withData)
	}
	return map[string]any{
		"availability": avail, "slo": slo, "budgetLeft": budgetLeft(avail, slo),
		"p50": p50, "p95": p95, "p99": p99, "checksPerMin": perMin, "openIncidents": open, "verdicts": verdicts,
	}
}

type checkView struct {
	At        time.Time `json:"at"`
	Location  string    `json:"location"`
	Status    string    `json:"status"`
	LatencyMs int64     `json:"latencyMs"`
	HTTP      int       `json:"http"`
	Code      string    `json:"code,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

func recentChecks(rs []probe.Result) []checkView {
	out := make([]checkView, 0, len(rs))
	for _, x := range rs {
		out = append(out, checkView{x.At, x.Location, string(x.Status), x.LatencyMS, x.HTTPStatus, x.Code, x.Reason})
	}
	return out
}

type locState struct {
	Location  string `json:"location"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latencyMs"`
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	name, from, to, step := s.parseRange(r)
	ctx := r.Context()
	if useRollups(step) {
		s.overviewRollup(ctx, w, name, from, to, step)
		return
	}
	buckets, err := s.Store.VerdictBuckets(ctx, from, to, step)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	samples, err := s.Store.Samples(ctx, "", from, to)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	verdicts, _ := s.Store.LatestVerdicts(ctx)
	byLoc, _ := s.Store.LatestByLocation(ctx, to.Add(-time.Hour))
	open, _ := s.Store.OpenIncidents(ctx)
	n := int(to.Sub(from)/step) + 1
	idx := func(at int64) int { return int((at - from.UnixMilli()) / step.Milliseconds()) }

	lat := map[string][][]int64{}
	all := map[string][]int64{}
	var fleetLat []int64
	for _, x := range samples {
		if !x.Status.Healthy() {
			continue
		}
		if lat[x.Target] == nil {
			lat[x.Target] = make([][]int64, n)
		}
		if i := idx(x.At); i >= 0 && i < n {
			lat[x.Target][i] = append(lat[x.Target][i], x.LatencyMS)
		}
		all[x.Target] = append(all[x.Target], x.LatencyMS)
		fleetLat = append(fleetLat, x.LatencyMS)
	}

	out := overviewResponse{Range: name, From: from.Unix(), To: to.Unix(), Step: int64(step.Seconds()), Counts: map[string]int{}}

	var sumAvail float64
	var withData, checks int
	var sloSum float64
	for _, t := range s.Cfg.Targets {
		row := overviewTarget{ID: t.ID, Name: t.Name, Provider: t.Provider, Category: t.Category, SLO: t.SLO,
			Status: "unknown", Availability: -1, Spark: make([]*int64, n), States: make([]int, n)}
		for i := range row.States {
			row.States[i] = -1
		}
		if v, ok := verdicts[t.ID]; ok {
			row.Status, row.Reason = string(v.Status), v.Reason
		}
		var counted, healthy int
		for _, b := range buckets[t.ID] {
			if i := idx(b.Start); i >= 0 && i < n {
				row.States[i] = state(b)
			}
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
		checks += counted
		row.BudgetLeft = budgetLeft(row.Availability, t.SLO)
		for i, l := range lat[t.ID] {
			if len(l) > 0 {
				v := pctl(l, 0.95)
				row.Spark[i] = &v
			}
		}
		row.P50, row.P95 = pctl(all[t.ID], 0.5), pctl(all[t.ID], 0.95)
		row.Locations = []locState{}
		for _, x := range byLoc[t.ID] {
			row.Locations = append(row.Locations, locState{x.Location, string(x.Status), x.LatencyMS})
		}
		countStatus(out.Counts, row.Status)
		out.Rows = append(out.Rows, row)
	}
	out.Fleet = fleetSummary(sumAvail, sloSum, withData, pctl(fleetLat, 0.5), pctl(fleetLat, 0.95), pctl(fleetLat, 0.99),
		float64(len(samples))/to.Sub(from).Minutes(), len(open), checks)
	writeJSON(w, out)
}

// series: one target over the range: p50/p95/max per bucket (all locations), p95 per
// location, verdict state per bucket, and the newest raw checks.
func (s *Server) series(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("target")
	found := false
	for _, t := range s.Cfg.Targets {
		if t.ID == id {
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	name, from, to, step := s.parseRange(r)
	ctx := r.Context()
	if useRollups(step) {
		s.seriesRollup(ctx, w, id, name, from, to, step)
		return
	}
	samples, err := s.Store.Samples(ctx, id, from, to)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	buckets, _ := s.Store.VerdictBuckets(ctx, from, to, step)
	recent, _ := s.Store.RecentChecks(ctx, id, 40)
	n := int(to.Sub(from)/step) + 1
	idx := func(at int64) int { return int((at - from.UnixMilli()) / step.Milliseconds()) }

	ts := make([]int64, n)
	for i := range ts {
		ts[i] = from.Add(time.Duration(i) * step).Unix()
	}
	all := make([][]int64, n)
	locs := map[string][][]int64{}
	fails := map[string][]int{}
	for _, x := range samples {
		i := idx(x.At)
		if i < 0 || i >= n {
			continue
		}
		if locs[x.Location] == nil {
			locs[x.Location] = make([][]int64, n)
			fails[x.Location] = make([]int, n)
			for k := range fails[x.Location] {
				fails[x.Location][k] = -1
			}
		}
		sev := 0
		if !x.Status.Counts() {
			sev = -2 // our side
		} else {
			sev = x.Status.Severity()
		}
		if sev > fails[x.Location][i] || fails[x.Location][i] == -1 {
			fails[x.Location][i] = sev
		}
		if x.Status.Healthy() {
			all[i] = append(all[i], x.LatencyMS)
			locs[x.Location][i] = append(locs[x.Location][i], x.LatencyMS)
		}
	}
	nullable := func(l []int64, p float64) *int64 {
		if len(l) == 0 {
			return nil
		}
		v := pctl(l, p)
		return &v
	}
	p50, p95, mx := make([]*int64, n), make([]*int64, n), make([]*int64, n)
	for i, l := range all {
		p50[i], p95[i], mx[i] = nullable(l, 0.5), nullable(l, 0.95), nullable(l, 1)
	}
	byLoc := map[string][]*int64{}
	for loc, ls := range locs {
		byLoc[loc] = make([]*int64, n)
		for i, l := range ls {
			byLoc[loc][i] = nullable(l, 0.95)
		}
	}
	states := make([]int, n)
	for i := range states {
		states[i] = -1
	}
	counted, bad := make([]int, n), make([]int, n)
	for _, b := range buckets[id] {
		if i := idx(b.Start); i >= 0 && i < n {
			states[i] = state(b)
			counted[i], bad[i] = b.Counted, b.Counted-b.Healthy
		}
	}
	writeJSON(w, map[string]any{
		"target": id, "range": name, "stepSec": int64(step.Seconds()), "slo": s.slo(id),
		"t": ts, "p50": p50, "p95": p95, "max": mx, "states": states, "counted": counted, "bad": bad,
		"byLocation": byLoc, "locationStates": fails, "recent": recentChecks(recent),
	})
}

// schemaEvents: response-field changes in the last 90 days, newest first.
func (s *Server) schemaEvents(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.SchemaEvents(r.Context(), r.URL.Query().Get("target"), time.Now().AddDate(0, 0, -90), 200)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	writeJSON(w, list)
}
