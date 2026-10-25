package web

import (
	"context"
	"fmt"
	"html/template"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/insight"
)

// What an API's history means for a developer calling it (internal/insight): retry now
// or not (AI JSON retryAdvice), how its incidents usually go, and its bad hours.

// PatternDays and PeakDays are the periods the summaries cover.
const (
	PatternDays = 30
	PeakDays    = 30
)

func (s *Server) pattern(ctx context.Context, id string, now time.Time) insight.Pattern {
	incs, err := s.Store.Incidents(ctx, id, now.AddDate(0, 0, -PatternDays), 500)
	if err != nil {
		return insight.Pattern{Days: PatternDays, Kind: "none"}
	}
	return insight.Patterns(incs, PatternDays, now)
}

// retryAdvice for one target of the AI JSON.
func (s *Server) retryAdvice(ctx context.Context, t config.Target, d DiagnosticTarget) (insight.Advice, insight.Pattern) {
	now := time.Now()
	p := s.pattern(ctx, t.ID, now)
	var next time.Time
	if d.CheckedAt != nil {
		next = d.CheckedAt.Add(t.Interval.Duration)
		if next.Before(now) {
			next = now.Add(time.Minute) // a check is due: look again shortly
		}
	}
	var until *time.Time
	if m := s.Cfg.MaintenanceAt(&t, now); m != nil {
		end := m.End
		until = &end
	}
	return insight.Advise(d.Assessment, next, until, p), p
}

type peakView struct {
	insight.Peaks
	Bars []peakBar
}

type peakBar struct {
	Hour        int
	Height, Tip string
	Tone        string
}

func (s *Server) peaks(ctx context.Context, id string, now time.Time) *peakView {
	rows, err := s.Store.Hourly(ctx, id, now.AddDate(0, 0, -PeakDays), now, true)
	if err != nil {
		return nil
	}
	p := insight.PeakHours(rows, PeakDays)
	v := &peakView{Peaks: p}
	worst := 0.0
	for _, h := range p.Hours {
		worst = max(worst, h.FailPct+h.DegradedPct)
	}
	hot := map[int]bool{}
	for _, h := range p.Worst {
		hot[h.Hour] = true
	}
	for _, h := range p.Hours {
		b := peakBar{Hour: h.Hour, Height: "2", Tone: "ok",
			Tip: fmt.Sprintf("%d시: 실패 %.1f%%, 느림·이상 %.1f%%, p95 %dms (%d회)", h.Hour, h.FailPct, h.DegradedPct, h.P95Ms, h.Checks)}
		if worst > 0 {
			b.Height = fmt.Sprintf("%d", 2+int((h.FailPct+h.DegradedPct)/worst*58))
		}
		switch {
		case h.Checks == 0:
			b.Tone = "none"
		case hot[h.Hour]:
			b.Tone = "bad"
		case h.FailPct+h.DegradedPct > 0:
			b.Tone = "warn"
		}
		v.Bars = append(v.Bars, b)
	}
	return v
}

func init() {
	template.Must(answerTmpl.Parse(insightTemplates))
}

const insightTemplates = `
{{define "pattern"}}{{with .}}<h2>이 API의 장애는 보통 어떻게 가나</h2>
<section class="you"><b>{{.Summary}}</b><p style="margin:6px 0 0">{{.Recommended}}</p></section>{{end}}{{end}}

{{define "peaks"}}{{with .}}<h2>시간대별 실패·느림 (최근 {{.Days}}일, KST)</h2>
<div style="display:flex;align-items:flex-end;gap:2px;height:64px;border-bottom:1px solid var(--line)">{{range .Bars}}<span title="{{.Tip}}" style="flex:1;height:{{.Height}}px;border-radius:2px 2px 0 0;background:var(--{{.Tone}})"></span>{{end}}</div>
<div class="sub" style="display:flex;justify-content:space-between"><span>0시</span><span>6시</span><span>12시</span><span>18시</span><span>23시</span></div>
<p>{{.Summary}}{{if .Worst}} 이 시간에는 결과를 캐시해 두고 호출을 줄이거나, 시간 제한을 넉넉히 두세요.{{end}}</p>
<p class="sub">막대 높이는 그 시간대의 실패와 느림·데이터 이상 비율입니다. 빨간 막대는 평소보다 두 배 이상 나쁜 시간대(그 시간 확인이 60회 이상일 때만)입니다. 감시 지점은 미국 서부 한 곳입니다.</p>{{end}}{{end}}
`
