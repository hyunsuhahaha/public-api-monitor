// Package insight turns an API's history into what a developer calling it decides on:
// whether to wait out its failures or fall back (Pattern), which hours to avoid or cache
// through (Peaks), and whether to retry right now (Advise).
package insight

import (
	"fmt"
	"sort"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Pattern sums up an API's incidents over a period.
type Pattern struct {
	Days        int    `json:"days"`
	Incidents   int    `json:"incidents"` // outages and degradations
	Outages     int    `json:"outages"`
	MedianMin   int    `json:"medianMinutes"`  // closed incidents
	LongestMin  int    `json:"longestMinutes"` // open ones count up to now
	ShortShare  int    `json:"shortSharePct"`  // closed within 15 minutes, % of closed
	Night       int    `json:"nightSharePct"`  // started 00-06 KST, % of all (maintenance hours)
	Kind        string `json:"kind"`           // none, blips, long, mixed
	Summary     string `json:"summary"`        // one Korean sentence
	Recommended string `json:"recommended"`    // what to build for it
}

// Patterns of incidents (any order) over days, at now.
func Patterns(incs []*incident.Incident, days int, now time.Time) Pattern {
	p := Pattern{Days: days, Kind: "none"}
	var closed []int
	night := 0
	for _, in := range incs {
		p.Incidents++
		if in.Severity >= 2 {
			p.Outages++
		}
		end := in.Ended
		if in.Open() {
			end = now
		}
		m := int(end.Sub(in.Started).Round(time.Minute).Minutes())
		p.LongestMin = max(p.LongestMin, m)
		if !in.Open() {
			closed = append(closed, m)
		}
		if h := in.Started.In(config.KST).Hour(); h < 6 {
			night++
		}
	}
	if p.Incidents == 0 {
		p.Summary = fmt.Sprintf("최근 %d일 동안 기록된 장애가 없습니다.", days)
		p.Recommended = "평소처럼 시간 제한과 몇 번의 재시도면 충분합니다."
		return p
	}
	p.Night = night * 100 / p.Incidents
	if len(closed) > 0 {
		sort.Ints(closed)
		p.MedianMin = closed[(len(closed)-1)/2]
		short := 0
		for _, m := range closed {
			if m <= 15 {
				short++
			}
		}
		p.ShortShare = short * 100 / len(closed)
	}
	switch {
	case len(closed) > 0 && p.ShortShare >= 70 && p.LongestMin <= 60:
		p.Kind = "blips"
		p.Recommended = "대부분 짧게 끝나므로, 간격을 늘려 가는 재시도와 짧은 캐시로 버틸 수 있습니다. 대체 수단까지는 필요 없을 가능성이 큽니다."
	case p.MedianMin >= 60 || (len(closed) == 0 && p.LongestMin >= 60):
		p.Kind = "long"
		p.Recommended = "한 번 멈추면 오래 갑니다. 재시도로는 안 되니 마지막 정상 데이터를 보여 주거나 다른 제공자로 넘기는 대체 경로를 준비하세요."
	default:
		p.Kind = "mixed"
		p.Recommended = "짧은 장애와 긴 장애가 섞여 있습니다. 재시도는 몇 번까지만 하고, 그래도 안 되면 서킷 브레이커로 멈춘 뒤 캐시나 대체 경로로 넘기세요."
	}
	p.Summary = fmt.Sprintf("최근 %d일 장애·저하 %d건", days, p.Incidents)
	if p.MedianMin > 0 || len(closed) > 0 {
		p.Summary += fmt.Sprintf(", 보통 %s 안에 복구", dur(p.MedianMin))
	}
	p.Summary += fmt.Sprintf(", 가장 길었던 것 %s", dur(p.LongestMin))
	if p.Night >= 50 && p.Incidents >= 2 {
		p.Summary += fmt.Sprintf(", %d%%가 새벽(0~6시)에 시작 (점검 시간대일 수 있음)", p.Night)
	}
	p.Summary += "."
	return p
}

func dur(m int) string {
	switch {
	case m < 1:
		return "1분"
	case m < 60:
		return fmt.Sprintf("%d분", m)
	case m%60 == 0:
		return fmt.Sprintf("%d시간", m/60)
	}
	return fmt.Sprintf("%d시간 %d분", m/60, m%60)
}

// Hour is one hour of the day (KST) over a period: verdicts and answer times.
type Hour struct {
	Hour        int     `json:"hour"` // 0..23 KST
	Checks      int     `json:"checks"`
	FailPct     float64 `json:"failPct"`     // outage verdicts, % of counted
	DegradedPct float64 `json:"degradedPct"` // slow or stale
	P95Ms       int64   `json:"p95Ms"`
}

// Peaks is the hour-of-day profile and the hours that stand out.
type Peaks struct {
	Days    int     `json:"days"`
	Hours   []Hour  `json:"hours"` // 24, by hour
	FailPct float64 `json:"failPct"`
	P95Ms   int64   `json:"p95Ms"`
	Worst   []Hour  `json:"worst"` // at most 3, worst first
	Summary string  `json:"summary"`
}

// minChecks: an hour needs this many verdicts to be compared (about a week at 5 minutes).
const minChecks = 60

// PeakHours profiles hourly rollups (verdict rows "*" and merged-latency rows "ALL").
func PeakHours(rows []store.HourRow, days int) Peaks {
	p := Peaks{Days: days, Hours: make([]Hour, 24), Worst: []Hour{}}
	var counted, outage, degraded [24]int
	hists := make([]store.Hist, 24)
	all := store.NewHist()
	var tc, to int
	for _, r := range rows {
		h := time.UnixMilli(r.Hour).In(config.KST).Hour()
		switch r.Location {
		case "*":
			counted[h] += r.Counted
			outage[h] += r.Outage
			degraded[h] += r.Degraded
			tc += r.Counted
			to += r.Outage
		case store.AllLocations:
			if r.Hist != nil {
				if hists[h] == nil {
					hists[h] = store.NewHist()
				}
				hists[h].Merge(r.Hist)
				all.Merge(r.Hist)
			}
		}
	}
	for h := 0; h < 24; h++ {
		x := Hour{Hour: h, Checks: counted[h]}
		if counted[h] > 0 {
			x.FailPct = pct(outage[h], counted[h])
			x.DegradedPct = pct(degraded[h], counted[h])
		}
		if hists[h] != nil {
			x.P95Ms = hists[h].Quantile(0.95)
		}
		p.Hours[h] = x
	}
	if tc == 0 {
		p.Summary = "시간대별로 비교할 기록이 아직 없습니다."
		return p
	}
	p.FailPct, p.P95Ms = pct(to, tc), all.Quantile(0.95)
	var cands []Hour
	for _, x := range p.Hours {
		if x.Checks < minChecks {
			continue
		}
		// stands out: at least double the usual failure rate and 2 points more, or a p95
		// at least double the usual one
		failHot := x.FailPct >= 2*p.FailPct && x.FailPct-p.FailPct >= 2
		slowHot := p.P95Ms > 0 && x.P95Ms >= 2*p.P95Ms
		if failHot || slowHot {
			cands = append(cands, x)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].FailPct != cands[j].FailPct {
			return cands[i].FailPct > cands[j].FailPct
		}
		return cands[i].P95Ms > cands[j].P95Ms
	})
	if len(cands) > 3 {
		cands = cands[:3]
	}
	p.Worst = cands
	if len(cands) == 0 {
		p.Summary = "특별히 나쁜 시간대가 없습니다. 하루 종일 비슷합니다."
		return p
	}
	w := cands[0]
	p.Summary = fmt.Sprintf("%d시대가 가장 나쁩니다: 실패 %.1f%% (평소 %.1f%%), 응답 p95 %s (평소 %s).", w.Hour, w.FailPct, p.FailPct, ms(w.P95Ms), ms(p.P95Ms))
	return p
}

func pct(a, b int) float64 { return float64(int(float64(a)*1000/float64(b)+0.5)) / 10 }

func ms(v int64) string {
	if v == 0 {
		return "–"
	}
	if v >= 1000 {
		return fmt.Sprintf("%.1f초", float64(v)/1000)
	}
	return fmt.Sprintf("%dms", v)
}

// Advice is "should I retry now?", for code: a caller can switch on Action alone.
type Advice struct {
	// Action: retry (no failure observed: a failure on your side is likely transient or
	// yours, retry with backoff), backoff (degraded: retry sparingly with longer waits),
	// wait (failure observed: do not retry until RetryAfter), skip (announced maintenance:
	// queue until Until), unknown (no fresh observation: rely on your own breaker).
	Action string `json:"action"`
	// RetryAfter: when pamon's next check may tell something new (wait, backoff).
	RetryAfter *time.Time `json:"retryAfter,omitempty"`
	// Until: end of the announced maintenance window (skip).
	Until  *time.Time `json:"until,omitempty"`
	Reason string     `json:"reason"`
}

// Advise from pamon's assessment of a target, its next expected check, an announced
// maintenance end (nil if none) and its incident pattern.
func Advise(assessment string, nextCheck time.Time, maintEnd *time.Time, pattern Pattern) Advice {
	if maintEnd != nil {
		return Advice{Action: "skip", Until: maintEnd, Reason: "Announced maintenance: queue or skip calls until it ends; do not retry."}
	}
	next := nextCheck
	switch assessment {
	case "failure_observed":
		a := Advice{Action: "wait", RetryAfter: &next, Reason: "pamon observes this API failing; retries will not help before its next check."}
		if pattern.Kind == "long" {
			a.Reason += " Its outages usually last long: switch to a fallback."
		}
		return a
	case "degraded_observed":
		return Advice{Action: "backoff", RetryAfter: &next, Reason: "Slow or degraded: retry sparingly with growing, jittered waits and a longer timeout."}
	case "no_failure_observed":
		return Advice{Action: "retry", Reason: "No failure observed from outside: a failure you see is likely transient or specific to your request, key or network. Retry a few times with backoff."}
	}
	return Advice{Action: "unknown", Reason: "No fresh observation: decide with your own timeouts and circuit breaker."}
}
