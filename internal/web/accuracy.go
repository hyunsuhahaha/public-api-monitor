package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/insight"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Was retryAdvice right? (/retry-advice): every check's advice compared with the next
// check, over AccuracyDays (internal/insight Accuracy).

// AccuracyDays is the period the check covers.
const AccuracyDays = 30

type accuracyCache struct {
	mu sync.Mutex
	m  map[string]store.Transition
	at time.Time
}

func (s *Server) transitions(ctx context.Context) map[string]store.Transition {
	s.acc.mu.Lock()
	defer s.acc.mu.Unlock()
	if s.acc.m == nil || time.Since(s.acc.at) > CacheFor {
		m, err := s.Store.Transitions(ctx, time.Now().AddDate(0, 0, -AccuracyDays))
		if err != nil {
			return map[string]store.Transition{}
		}
		s.acc.m, s.acc.at = m, time.Now()
	}
	return s.acc.m
}

// accuracyFor is one target's record, nil without enough samples to say anything.
func (s *Server) accuracyFor(ctx context.Context, id string) *insight.Accuracy {
	a := insight.AccuracyOf(s.transitions(ctx)[id], AccuracyDays)
	if a.Wait+a.Retry == 0 {
		return nil
	}
	return &a
}

// AccuracyView is /api/v1/retry-advice.
type AccuracyView struct {
	GeneratedAt time.Time                   `json:"generatedAt"`
	Total       insight.Accuracy            `json:"total"`
	Targets     map[string]insight.Accuracy `json:"targets"`
	Method      string                      `json:"method"`
}

const accuracyMethod = "Each check's retryAdvice (wait after a failing check, retry after a working one, backoff after a slow one) is compared with the next check about one interval later: wait is right when that check failed too, retry and backoff when it did not. client and maintenance checks are left out; pairs further apart than 15 minutes are not consecutive."

func (s *Server) accuracyView(ctx context.Context) AccuracyView {
	all := s.transitions(ctx)
	v := AccuracyView{GeneratedAt: time.Now(), Total: insight.AccuracyOf(insight.Merge(all), AccuracyDays), Targets: map[string]insight.Accuracy{}, Method: accuracyMethod}
	for _, t := range s.Cfg.Targets {
		if tr, ok := all[t.ID]; ok {
			v.Targets[t.ID] = insight.AccuracyOf(tr, AccuracyDays)
		}
	}
	return v
}

func (s *Server) accuracyJSON(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.accuracyView(r.Context()))
}

type accuracyRow struct {
	ID, Name, Provider, Wait, Retry, Note string
	Tone                                  string
	n                                     int
}

type accuracyPage struct {
	Total             insight.Accuracy
	Wait, Retry       string
	Rows              []accuracyRow
	Days              int
	Generated, Every  string
	BlipAPIs, Targets int
}

func pctText(p float64, n int) string {
	if p < 0 {
		return "–"
	}
	return fmt.Sprintf("%.1f%% (%d회)", p, n)
}

func (s *Server) accuracyPage(w http.ResponseWriter, r *http.Request) {
	v := s.accuracyView(r.Context())
	p := accuracyPage{Total: v.Total, Wait: pctText(v.Total.WaitPct, v.Total.Wait), Retry: pctText(v.Total.RetryPct, v.Total.Retry),
		Days: AccuracyDays, Generated: kst(time.Now()), Every: koDuration(s.Cfg.Interval.Duration), Targets: len(v.Targets)}
	for _, t := range s.Cfg.Targets {
		a, ok := v.Targets[t.ID]
		if !ok || a.Wait < 3 {
			continue // too few failing checks to judge the advice
		}
		row := accuracyRow{ID: t.ID, Name: t.Name, Provider: t.Provider, Wait: pctText(a.WaitPct, a.Wait), Retry: pctText(a.RetryPct, a.Retry), n: a.Wait, Tone: "ok"}
		switch {
		case a.WaitPct < 50:
			row.Tone, row.Note = "warn", "실패가 대부분 다음 확인 전에 풀립니다. 짧게 몇 번 재시도하는 편이 낫습니다."
			p.BlipAPIs++
		case a.WaitPct >= 80:
			row.Note = "한 번 실패하면 이어지는 편입니다. 기다리거나 대체 경로로 넘기세요."
		}
		p.Rows = append(p.Rows, row)
	}
	sort.SliceStable(p.Rows, func(i, j int) bool { return p.Rows[i].n > p.Rows[j].n })
	if len(p.Rows) > 60 {
		p.Rows = p.Rows[:60]
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	answerTmpl.ExecuteTemplate(w, "accuracy", p)
}

func init() {
	template.Must(answerTmpl.Parse(accuracyTemplates))
}

const accuracyTemplates = `
{{define "accuracy"}}{{template "head" .}}
<title>pamon의 재시도 조언은 맞았나 | pamon</title>
<meta name="description" content="pamon이 '기다리세요'라고 했을 때 다음 확인에서도 실패했는지, '재시도해도 됩니다'라고 했을 때 정상이었는지 최근 {{.Days}}일 기록으로 검증합니다.">
<link rel="alternate" type="application/json" href="data/api/v1/retry-advice.json">
</head><body><main>
<nav><a href="s/index.html">서비스별 상태</a> › 재시도 조언 검증</nav>
<h1>pamon의 재시도 조언은 맞았나</h1>
<p class="sub">최근 {{.Days}}일, {{.Targets}}개 서비스의 모든 확인 기준 · 데이터 생성 {{.Generated}} (KST)</p>
<p>각 서비스의 <code>ai/targets/{id}.json</code>은 지금 재시도해도 되는지(<code>retryAdvice</code>)를 알려 줍니다. 실패를 봤으면 <b>기다리세요(wait)</b>, 정상이면 <b>재시도해도 됩니다(retry)</b>. 그 조언이 맞았는지 바로 다음 확인({{.Every}} 뒤) 결과와 비교했습니다.</p>
<div class="facts">
  <div><small>'기다리세요' 뒤 다음 확인도 실패</small><b>{{.Wait}}</b></div>
  <div><small>'재시도해도 됩니다' 뒤 다음 확인도 정상</small><b>{{.Retry}}</b></div>
  <div><small>짧은 실패가 많은 서비스</small><b>{{.BlipAPIs}}개</b></div>
</div>
<section class="you" style="margin-top:12px"><p style="margin:0">'기다리세요' 적중률이 높을수록 실패가 이어졌다는 뜻이라, 그동안 재시도를 멈춘 것이 옳았습니다. 낮은 서비스는 실패가 금방 풀려서, 기다리기보다 짧게 몇 번 재시도하는 편이 낫습니다. 아래 표에서 서비스별로 확인하세요.</p></section>

{{if .Rows}}<h2>서비스별 (실패 확인이 많은 순)</h2>
<table><tr><th>서비스</th><th>'기다리세요' 적중</th><th>'재시도' 적중</th></tr>
{{range .Rows}}<tr><td><a href="s/{{.ID}}.html">{{.Name}}</a><br><span class="sub">{{.Provider}}</span></td><td class="n"><span class="h {{.Tone}}">{{.Wait}}</span></td><td class="n">{{.Retry}}</td></tr>
{{if .Note}}<tr><td colspan="3" class="sub" style="border-top:0;padding-top:0">{{.Note}}</td></tr>{{end}}{{end}}</table>
<p class="sub">실패 확인이 3회 미만인 서비스는 판단할 수 없어 뺐습니다.</p>
{{else}}<p>아직 비교할 실패 기록이 없습니다.</p>{{end}}

<h2>어떻게 재나</h2>
<ul>
<li>모든 확인마다 그때의 조언(실패·데이터 이상 → 기다리세요, 정상 → 재시도해도 됨, 느림 → 간격을 늘려 재시도)을 정하고, 다음 확인 결과와 비교합니다.</li>
<li>다음 확인은 {{.Every}} 뒤입니다. 몇 초 뒤 재시도의 결과와는 다를 수 있습니다. 이 수치는 "{{.Every}} 안에 풀리는 실패였나"를 말합니다.</li>
<li>키·호출 한도 같은 감시 쪽 문제와 공지된 점검 시간은 뺍니다. 15분 넘게 떨어진 확인(감시가 멈췄던 때)은 이어진 확인으로 보지 않습니다.</li>
</ul>
<p><a href="data/api/v1/retry-advice.json">이 표의 JSON</a> · <a href="lead-time.html">공식 발표보다 얼마나 빨랐나</a></p>
<footer>pamon: 개발자와 사용자가 의존하는 서비스의 외부 관측 기록. 공식 발표가 아닙니다.</footer>
</main></body></html>{{end}}

{{define "accuracyLine"}}{{with .}}{{if ge .WaitPct 0.0}}<p class="sub">이 API에서 pamon이 '기다리세요'라고 한 뒤 다음 확인도 실패한 비율: {{printf "%.1f" .WaitPct}}% ({{.Wait}}회){{if lt .WaitPct 50.0}}. 실패가 대부분 금방 풀리므로 짧게 몇 번 재시도하는 편이 낫습니다{{end}}. <a href="../retry-advice.html">재시도 조언 검증</a></p>{{end}}{{end}}{{end}}
`
