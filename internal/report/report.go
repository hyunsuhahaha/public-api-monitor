// Package report builds the monthly availability report: which public APIs were down the
// most, the longest outages, providers ranked. Served at /report/YYYY-MM and printed as
// Markdown by `pamon -report YYYY-MM` (ready to post).
package report

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

type Row struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Provider     string  `json:"provider"`
	Category     string  `json:"category"`
	Availability float64 `json:"availability"` // -1: no data
	Checks       int     `json:"checks"`
	Degraded     int     `json:"degraded"`
	Incidents    int     `json:"incidents"`
	DowntimeMin  int     `json:"downtimeMinutes"` // outage incidents, clipped to the month
	LongestMin   int     `json:"longestMinutes"`
	P50Ms        int64   `json:"p50Ms"`
	P95Ms        int64   `json:"p95Ms"`
}

type Provider struct {
	Name         string  `json:"name"`
	APIs         int     `json:"apis"`
	Availability float64 `json:"availability"` // mean over its APIs with data
	DowntimeMin  int     `json:"downtimeMinutes"`
}

type Outage struct {
	TargetID string    `json:"target"`
	Name     string    `json:"name"`
	Started  time.Time `json:"started"`
	Minutes  int       `json:"minutes"`
	Ongoing  bool      `json:"ongoing"`
	Reason   string    `json:"reason"`
}

// Timeouts: what the follow-ups found for one API's timeouts (probe.FollowUps).
type Timeouts struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Checked  int    `json:"checked"`
	Retried  int    `json:"retried"` // answered at once when re-sent: that request got stuck
	Late     int    `json:"late"`    // answered after the timeout: the server was slow
	Hung     int    `json:"hung"`    // nothing within the follow-up's budget
	MedianMs int64  `json:"medianMs"`
}

type Report struct {
	Month        string     `json:"month"` // YYYY-MM (KST)
	From         time.Time  `json:"from"`
	To           time.Time  `json:"to"`
	Partial      bool       `json:"partial"` // the month isn't over yet
	APIs         int        `json:"apis"`
	WithData     int        `json:"apisWithData"`
	Availability float64    `json:"availability"` // mean over APIs with data
	Incidents    int        `json:"incidents"`
	DowntimeMin  int        `json:"downtimeMinutes"`
	Rows         []Row      `json:"rows"`      // worst availability first
	Providers    []Provider `json:"providers"` // worst first
	Longest      []Outage   `json:"longest"`
	Timeouts     []Timeouts `json:"timeouts"` // most followed-up first
}

// MonthRange parses YYYY-MM as a KST calendar month.
func MonthRange(month string) (time.Time, time.Time, error) {
	from, err := time.ParseInLocation("2006-01", month, config.KST)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("month must be YYYY-MM: %w", err)
	}
	return from, from.AddDate(0, 1, 0), nil
}

func Build(ctx context.Context, st *store.Store, cfg *config.Config, month string, now time.Time) (*Report, error) {
	from, to, err := MonthRange(month)
	if err != nil {
		return nil, err
	}
	end := to
	r := &Report{Month: month, From: from, To: to, APIs: len(cfg.Targets)}
	if now.Before(to) {
		r.Partial, end = true, now
	}
	counts, err := st.VerdictCounts(ctx, from, to)
	if err != nil {
		return nil, err
	}
	incs, err := st.IncidentsOverlapping(ctx, from, to)
	if err != nil {
		return nil, err
	}
	lat, err := st.LatenciesBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	names := map[string]*config.Target{}
	for i := range cfg.Targets {
		names[cfg.Targets[i].ID] = &cfg.Targets[i]
	}
	byTarget := map[string][]*incident.Incident{}
	for _, in := range incs {
		byTarget[in.TargetID] = append(byTarget[in.TargetID], in)
	}
	var sum float64
	for _, t := range cfg.Targets {
		c := counts[t.ID]
		row := Row{ID: t.ID, Name: t.Name, Provider: t.Provider, Category: t.Category, Availability: -1,
			Checks: c.Counted, Degraded: c.Degraded}
		if c.Counted > 0 {
			row.Availability = float64(c.Healthy) / float64(c.Counted)
			sum += row.Availability
			r.WithData++
		}
		for _, in := range byTarget[t.ID] {
			row.Incidents++
			if in.Severity < 2 {
				continue // degradations count as incidents, not as downtime
			}
			m := minutes(in, from, end)
			row.DowntimeMin += m
			row.LongestMin = max(row.LongestMin, m)
			r.Longest = append(r.Longest, Outage{TargetID: t.ID, Name: t.Name, Started: in.Started, Minutes: m, Ongoing: in.Open(), Reason: in.Reason})
		}
		row.P50Ms, row.P95Ms = pct(lat[t.ID], 0.5), pct(lat[t.ID], 0.95)
		r.Incidents += row.Incidents
		r.DowntimeMin += row.DowntimeMin
		r.Rows = append(r.Rows, row)
	}
	if r.WithData > 0 {
		r.Availability = sum / float64(r.WithData)
	} else {
		r.Availability = -1
	}
	sort.SliceStable(r.Rows, func(i, j int) bool {
		a, b := r.Rows[i], r.Rows[j]
		if (a.Availability < 0) != (b.Availability < 0) {
			return b.Availability < 0 // no data last
		}
		if a.Availability != b.Availability {
			return a.Availability < b.Availability
		}
		return a.DowntimeMin > b.DowntimeMin
	})
	sort.Slice(r.Longest, func(i, j int) bool { return r.Longest[i].Minutes > r.Longest[j].Minutes })
	if len(r.Longest) > 10 {
		r.Longest = r.Longest[:10]
	}
	r.Providers = providers(r.Rows)
	fus, err := st.FollowUpsBetween(ctx, from, to)
	if err != nil {
		return nil, err
	}
	for id, list := range fus {
		x := Timeouts{ID: id, Name: id, Checked: len(list)}
		if t := names[id]; t != nil {
			x.Name = t.Name
		}
		var after []int64
		for _, f := range list {
			switch f.Outcome() {
			case "retried":
				x.Retried++
			case "late":
				x.Late++
			default:
				x.Hung++
			}
			if f.Answered {
				after = append(after, f.AfterMs)
			}
		}
		x.MedianMs = lowMedian(after)
		r.Timeouts = append(r.Timeouts, x)
	}
	sort.Slice(r.Timeouts, func(i, j int) bool {
		if r.Timeouts[i].Checked != r.Timeouts[j].Checked {
			return r.Timeouts[i].Checked > r.Timeouts[j].Checked
		}
		return r.Timeouts[i].ID < r.Timeouts[j].ID
	})
	return r, nil
}

func minutes(in *incident.Incident, from, end time.Time) int {
	s, e := in.Started, in.Ended
	if e.IsZero() || e.After(end) {
		e = end
	}
	if s.Before(from) {
		s = from
	}
	if !e.After(s) {
		return 0
	}
	return int(e.Sub(s).Round(time.Minute).Minutes())
}

// lowMedian: the lower middle value, so two follow-ups of 0.1 s and 57 s read 0.1 s, not 57 s.
func lowMedian(v []int64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[(len(s)-1)/2]
}

func pct(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(p*float64(len(s)-1)+0.5)]
}

func providers(rows []Row) []Provider {
	m := map[string]*Provider{}
	n := map[string]int{}
	var order []string
	for _, r := range rows {
		p := m[r.Provider]
		if p == nil {
			p = &Provider{Name: r.Provider}
			m[r.Provider] = p
			order = append(order, r.Provider)
		}
		p.APIs++
		p.DowntimeMin += r.DowntimeMin
		if r.Availability >= 0 {
			p.Availability += r.Availability
			n[r.Provider]++
		}
	}
	out := make([]Provider, 0, len(order))
	for _, name := range order {
		p := m[name]
		if n[name] > 0 {
			p.Availability /= float64(n[name])
		} else {
			p.Availability = -1
		}
		out = append(out, *p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Availability < 0) != (out[j].Availability < 0) {
			return out[j].Availability < 0
		}
		return out[i].Availability < out[j].Availability
	})
	return out
}

// Pct formats an availability (or "–" without data).
func Pct(v float64) string {
	if v < 0 {
		return "–"
	}
	if v == 1 {
		return "100%"
	}
	return fmt.Sprintf("%.3f%%", v*100)
}

// Dur formats minutes as "3시간 12분".
func Dur(m int) string {
	switch {
	case m == 0:
		return "0분"
	case m < 60:
		return fmt.Sprintf("%d분", m)
	case m%60 == 0:
		return fmt.Sprintf("%d시간", m/60)
	}
	return fmt.Sprintf("%d시간 %d분", m/60, m%60)
}

// Markdown renders the report for a blog post or GeekNews.
func (r *Report) Markdown(publicURL string) string {
	var b strings.Builder
	y, m := r.From.Year(), int(r.From.Month())
	fmt.Fprintf(&b, "# %d년 %d월 공공 API 가용성 리포트\n\n", y, m)
	if r.Partial {
		fmt.Fprintf(&b, "> %s까지의 중간 집계입니다.\n\n", time.Now().In(config.KST).Format("1월 2일 15:04"))
	}
	fmt.Fprintf(&b, "- 감시 API: %d개 (기록 있는 API %d개), 5분마다 응답 본문의 결과 코드까지 확인\n", r.APIs, r.WithData)
	fmt.Fprintf(&b, "- 평균 가용률: **%s**\n", Pct(r.Availability))
	fmt.Fprintf(&b, "- 장애·성능 저하: %d건, 장애 시간 합계 %s\n\n", r.Incidents, Dur(r.DowntimeMin))

	b.WriteString("## 가용률 낮은 API\n\n| 순위 | API | 기관 | 가용률 | 장애 시간 | 최장 장애 | p95 |\n|---|---|---|---|---|---|---|\n")
	n := 0
	for _, row := range r.Rows {
		if row.Availability < 0 || n == 10 {
			break
		}
		n++
		fmt.Fprintf(&b, "| %d | %s | %s | %s | %s | %s | %s |\n", n, row.Name, row.Provider, Pct(row.Availability),
			Dur(row.DowntimeMin), Dur(row.LongestMin), msOrDash(row.P95Ms))
	}
	if n == 0 {
		b.WriteString("| – | 기록 없음 | | | | | |\n")
	}

	if len(r.Longest) > 0 {
		b.WriteString("\n## 가장 길었던 장애\n\n| API | 시작 (KST) | 길이 | 원인 |\n|---|---|---|---|\n")
		for _, o := range r.Longest {
			length := Dur(o.Minutes)
			if o.Ongoing {
				length += " (진행 중)"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", o.Name, o.Started.In(config.KST).Format("1/2 15:04"), length, strings.ReplaceAll(o.Reason, "|", "/"))
		}
	}

	if len(r.Timeouts) > 0 {
		b.WriteString("\n## 타임아웃 뒤 재확인\n\n타임아웃이 나면 같은 요청을 한 번 더 길게 기다려 봤습니다. 바로 응답하면 그 요청만 걸린 것(재시도하면 됨), 늦게 응답하면 서버가 느린 것, 끝까지 응답이 없으면 서버가 멈춘 것입니다.\n\n| API | 재확인 | 재시도로 바로 됨 | 늦게 응답 | 응답 없음 | 응답까지 (중앙값) |\n|---|---|---|---|---|---|\n")
		for _, x := range r.Timeouts {
			fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %s |\n", x.Name, x.Checked, x.Retried, x.Late, x.Hung, msOrDash(x.MedianMs))
		}
	}

	b.WriteString("\n## 기관별\n\n| 기관 | API 수 | 평균 가용률 | 장애 시간 |\n|---|---|---|---|\n")
	for _, p := range r.Providers {
		fmt.Fprintf(&b, "| %s | %d | %s | %s |\n", p.Name, p.APIs, Pct(p.Availability), Dur(p.DowntimeMin))
	}

	b.WriteString("\n## 측정 방법\n\n")
	b.WriteString("- 한국 공공 API는 실패해도 HTTP 200을 주는 경우가 많아, 응답 본문의 결과 코드(resultCode 등)까지 읽어 판정합니다.\n")
	b.WriteString("- 서비스 키·호출 한도처럼 감시하는 쪽 문제는 가용률에서 뺍니다. 여러 지역에서 확인할 때는 과반수가 실패해야 장애입니다.\n")
	b.WriteString("- 장애는 연속 2회 실패에 시작, 연속 2회 정상에 끝납니다. 장애 시간은 '장애' 등급만 합산하고 '성능 저하'는 건수에만 셉니다.\n")
	if publicURL != "" {
		fmt.Fprintf(&b, "\n실시간 상태: %s\n", strings.TrimRight(publicURL, "/"))
	}
	return b.String()
}

func msOrDash(v int64) string {
	if v == 0 {
		return "–"
	}
	return fmt.Sprintf("%dms", v)
}
