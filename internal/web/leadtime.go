package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/leadtime"
	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
	"github.com/hyunsuhahaha/public-api-monitor/internal/report"
)

// Lead time (/lead-time): how many minutes pamon confirmed an outage before (or after)
// the provider's own status page admitted it, over the last 90 days, with every case
// that could not be compared shown as such (internal/leadtime).

// LeadDays is the period the lead-time page covers.
const LeadDays = 90

type leadCache struct {
	mu sync.Mutex
	r  *leadtime.Report
	at time.Time
}

// leadReport is the last 90 days' comparison, rebuilt at most every CacheFor (every
// answer page of an export asks for it).
func (s *Server) leadReport(ctx context.Context) (*leadtime.Report, error) {
	s.lead.mu.Lock()
	defer s.lead.mu.Unlock()
	if s.lead.r == nil || time.Since(s.lead.at) > CacheFor {
		now := time.Now()
		r, err := leadtime.Load(ctx, s.Store, s.Cfg, now.AddDate(0, 0, -LeadDays), now, now)
		if err != nil {
			return nil, err
		}
		s.lead.r, s.lead.at = r, now
	}
	return s.lead.r, nil
}

// leadText says one comparison in words.
func leadText(r leadtime.Result) (tone, text string) {
	switch r.Kind {
	case leadtime.Announced:
		switch m := r.LeadMinutes; {
		case m >= 5:
			return "ok", fmt.Sprintf("pamon이 %s 먼저 확인", koMinutes(m))
		case m <= -5:
			return "warn", fmt.Sprintf("공식 발표가 %s 먼저", koMinutes(-m))
		default:
			return "", "거의 동시 (5분 이내)"
		}
	case leadtime.Never:
		return "bad", "공식 발표 없음"
	case leadtime.Pending:
		return "", "아직 판단 전 (진행 중이거나 끝난 지 1시간 미만)"
	case leadtime.AlreadyOn:
		return "", "비교 제외: 공식 페이지가 그 전부터 다른 문제를 알리던 중"
	}
	return "", "비교 제외: 그 시간에 공식 페이지 기록이 비어 있음"
}

var (
	koMinutes     = report.Dur
	signedMinutes = report.SignedMinutes
)

type leadCase struct {
	Start, Provider, Tone, Text, Official string
	OfficialURL, Source                   string
	Targets                               []answerItem
}

type leadProviderRow struct {
	leadtime.Stats
	Median, Mean, Since string
	Comparable          bool
}

type leadPage struct {
	Total                    leadtime.Stats
	Median, Mean, FirstShare string
	Providers                []leadProviderRow
	Quiet                    []leadProviderRow // watched, no outage in the period
	Cases                    []leadCase
	Days                     int
	Since, Generated         string
	Every                    string
}

func (s *Server) leadPageData(ctx context.Context) (*leadPage, error) {
	r, err := s.leadReport(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, t := range s.Cfg.Targets {
		names[t.ID] = t.Name
	}
	p := &leadPage{Total: r.Total, Days: LeadDays, Generated: kst(time.Now()), Every: koDuration(s.Cfg.Interval.Duration)}
	if r.Total.Announced > 0 {
		p.Median, p.Mean = signedMinutes(r.Total.MedianMinutes), signedMinutes(r.Total.MeanMinutes)
		p.FirstShare = fmt.Sprintf("%d / %d", r.Total.PamonFirst, r.Total.Announced)
	}
	var first time.Time
	for _, at := range r.Watched {
		if first.IsZero() || at.Before(first) {
			first = at
		}
	}
	if !first.IsZero() {
		p.Since = kst(first)
	}
	seen := map[string]bool{}
	for _, st := range r.Providers {
		row := leadProviderRow{Stats: st, Comparable: st.Announced > 0}
		if st.Announced > 0 {
			row.Median, row.Mean = signedMinutes(st.MedianMinutes), signedMinutes(st.MeanMinutes)
		}
		if at, ok := r.Watched[st.Provider]; ok {
			row.Since = kst(at)
		}
		seen[st.Provider] = true
		p.Providers = append(p.Providers, row)
	}
	for _, name := range official.Providers() {
		if !seen[name] {
			row := leadProviderRow{Stats: leadtime.Stats{Provider: name}}
			if at, ok := r.Watched[name]; ok {
				row.Since = kst(at)
			} else {
				row.Since = "기록 전"
			}
			p.Quiet = append(p.Quiet, row)
		}
	}
	for i, x := range r.Results {
		if i == 100 {
			break
		}
		c := leadCase{Start: kst(x.Confirmed), Provider: x.Provider, Official: x.Official, OfficialURL: x.OfficialURL, Source: x.Source}
		c.Tone, c.Text = leadText(x)
		for _, id := range x.Targets {
			c.Targets = append(c.Targets, answerItem{ID: id, Name: names[id]})
		}
		sort.Slice(c.Targets, func(i, j int) bool { return c.Targets[i].Name < c.Targets[j].Name })
		p.Cases = append(p.Cases, c)
	}
	return p, nil
}

func (s *Server) leadPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.leadPageData(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	answerTmpl.ExecuteTemplate(w, "leadtime", p)
}

func (s *Server) leadJSON(w http.ResponseWriter, r *http.Request) {
	rep, err := s.leadReport(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, rep)
}

// answerLead is the lead-time block of one answer page: this API's outages compared with
// its provider's page, and the provider's totals.
type answerLead struct {
	Provider string
	Cases    []leadCase
	Stats    leadtime.Stats
	Median   string
	Since    string // when pamon started reading the page ("" before the first read)
	Days     int
}

func (s *Server) answerLead(ctx context.Context, t TargetView) *answerLead {
	if !official.Has(t.Provider) {
		return nil
	}
	r, err := s.leadReport(ctx)
	if err != nil {
		return nil
	}
	a := &answerLead{Provider: t.Provider, Stats: r.Provider(t.Provider), Days: LeadDays}
	if at, ok := r.Watched[t.Provider]; ok {
		a.Since = kst(at)
	}
	if a.Stats.Announced > 0 {
		a.Median = signedMinutes(a.Stats.MedianMinutes)
	}
	for i, x := range r.For(t.ID) {
		if i == 10 {
			break
		}
		c := leadCase{Start: kst(x.Confirmed), Official: x.Official, OfficialURL: x.OfficialURL, Source: x.Source}
		c.Tone, c.Text = leadText(x)
		a.Cases = append(a.Cases, c)
	}
	return a
}

func init() {
	template.Must(answerTmpl.Parse(strings.TrimSpace(leadTemplates)))
}

const leadTemplates = `
{{define "leadtime"}}{{template "head" .}}
<title>공식 상태 페이지보다 얼마나 빨리 알았나 | pamon</title>
<meta name="description" content="GitHub·Slack·OpenAI·Cloudflare 등 공식 상태 페이지가 장애를 발표한 시각과, pamon이 외부에서 장애를 확인한 시각을 비교한 최근 {{.Days}}일 기록입니다.">
<link rel="alternate" type="application/json" href="data/api/v1/lead-time.json">
</head><body><main>
<nav><a href="s/index.html">서비스별 상태</a> › 공식 발표와 비교</nav>
<h1>공식 상태 페이지보다 얼마나 빨리 알았나</h1>
<p class="sub">최근 {{.Days}}일, 공식 상태 페이지를 함께 읽는 서비스의 장애 기준 · 데이터 생성 {{.Generated}} (KST){{if .Since}} · 공식 페이지 기록 시작 {{.Since}}{{end}}</p>

{{if .Total.Counted}}<div class="facts">
  <div><small>비교한 장애</small><b>{{.Total.Counted}}건</b></div>
  <div><small>pamon이 먼저 (5분 이상)</small><b>{{if .FirstShare}}{{.FirstShare}}{{else}}–{{end}}</b></div>
  <div><small>공식 발표 없이 끝남</small><b>{{.Total.Never}}건</b></div>
</div>
<section class="verdict {{if gt .Total.PamonFirst .Total.OfficialFirst}}ok{{else}}warn{{end}}">
{{if .Median}}<b class="h">발표가 있던 {{.Total.Announced}}건: 중앙값 pamon이 {{.Median}}</b>
<p>평균으로는 {{.Mean}}입니다. 비슷(5분 이내) {{.Total.Tie}}건, 공식 발표가 먼저 {{.Total.OfficialFirst}}건.</p>
{{else}}<b class="h">비교한 장애 {{.Total.Counted}}건 모두 공식 발표가 없었습니다</b>{{end}}
{{if .Total.Excluded}}<p class="sub">이 밖의 {{.Total.Excluded}}건은 비교에서 뺐습니다 (진행 중, 공식 페이지 기록 공백, 이미 다른 문제를 알리던 중).</p>{{end}}
</section>
{{else}}<section class="verdict unknown"><b class="h">아직 비교할 장애가 없습니다</b>
<p>공식 상태 페이지를 읽기 시작한 뒤로, 비교 조건을 채운 장애가 아직 없습니다.{{if .Total.Excluded}} (조건이 안 맞아 뺀 장애 {{.Total.Excluded}}건){{end}} 표본이 쌓이면 이 페이지가 채워집니다.</p></section>{{end}}

<h2>서비스 제공자별</h2>
{{if .Providers}}<table><tr><th>제공자</th><th>비교</th><th>중앙값</th><th>평균</th><th title="5분 이상 먼저 / 5분 이내 / 5분 이상 늦게">먼저·비슷·늦게</th><th>발표 없음</th><th>제외</th></tr>
{{range .Providers}}<tr><td>{{.Provider}}</td><td class="n">{{.Counted}}건</td><td class="n">{{if .Comparable}}{{.Median}}{{else}}–{{end}}</td><td class="n">{{if .Comparable}}{{.Mean}}{{else}}–{{end}}</td>
<td class="n">{{.PamonFirst}} · {{.Tie}} · {{.OfficialFirst}}</td><td class="n">{{.Never}}</td><td class="n">{{.Excluded}}</td></tr>{{end}}</table>
<p class="sub">표본이 적은 제공자의 평균은 한두 건에 크게 흔들립니다. 건수를 함께 보세요.</p>
{{else}}<p>최근 {{.Days}}일 동안 공식 상태 페이지가 있는 서비스에 기록된 장애가 없습니다.</p>{{end}}
{{if .Quiet}}<p class="sub">장애 없이 공식 페이지만 읽는 중: {{range $i, $q := .Quiet}}{{if $i}}, {{end}}{{$q.Provider}}{{end}}</p>{{end}}

{{if .Cases}}<h2>장애별 기록</h2>
<table><tr><th>pamon 확인 (KST)</th><th>서비스</th><th>공식 발표와 비교</th></tr>
{{range .Cases}}<tr><td>{{.Start}}</td><td>{{.Provider}}<br><span class="sub">{{range $i, $t := .Targets}}{{if $i}}, {{end}}<a href="s/{{$t.ID}}.html">{{$t.Name}}</a>{{end}}</span></td>
<td><span class="h {{.Tone}}">{{.Text}}</span>{{if .Official}}<br><span class="sub">공식: {{if .OfficialURL}}<a href="{{.OfficialURL}}" rel="nofollow">{{.Official}}</a>{{else}}{{.Official}}{{end}}{{if eq .Source "indicator"}} (상태 표시 기준, 5분 단위){{end}}</span>{{end}}</td></tr>{{end}}</table>{{end}}

<h2>어떻게 재나</h2>
<ul>
<li>공식 발표 시각은 제공자가 상태 페이지에 <b>장애 공지를 처음 올린 시각</b>입니다(Statuspage <code>incidents.json</code>의 <code>created_at</code>, Slack은 자체 API). pamon은 {{.Every}}마다 이 목록을 읽어 저장하므로 과거 공지도 비교에 씁니다. 목록이 그 시간까지 거슬러 올라가지 않으면, pamon이 {{.Every}}마다 읽은 상태 표시(<code>status.json</code>)가 바뀐 시각으로 대신 재고 "상태 표시 기준"이라고 적습니다.</li>
<li>pamon 쪽 시각은 첫 실패가 아니라 <b>장애로 확정한 시각</b>(연속 2회 실패, 보통 첫 실패 {{.Every}} 뒤)입니다. 실제로 이 페이지를 본 사람이 알 수 있었던 시각입니다.</li>
<li>'장애'(응답 없음·오류)만 셉니다. '느림·데이터 이상'은 공식 페이지가 다루지 않는 경우가 많아 뺐습니다. 한 제공자의 여러 API가 함께 실패하면 한 건으로 셉니다.</li>
<li>장애 확정 1시간 전부터 장애가 끝나고 1시간 뒤 사이에 올라온 공지(또는 '정상'이 아닌 표시)를 같은 일로 봅니다. 같은 제공자의 다른 기능에 대한 공지일 수도 있어서, 장애마다 공지 제목과 링크를 함께 보여 줍니다.</li>
<li>'공식 발표 없음'은 그 시간을 덮는 공지 목록을 읽었는데 공지가 없었던 경우(목록이 없으면 그 시간 내내 15분 넘는 공백 없이 상태 표시가 '정상'이었던 경우)만입니다. 기록이 비었거나, 바뀐 시각을 특정할 수 없거나, 그 전부터 올라와 있던 공지가 아직 열려 있었으면 <b>비교에서 뺍니다</b>.</li>
<li>pamon은 서비스의 기능 하나를 미국 서부에서 확인합니다. 공식 페이지가 '정상'이라도 pamon이 보는 기능만의 문제이거나, 반대로 pamon이 보지 않는 기능의 장애를 공식 페이지가 알릴 수 있습니다.</li>
</ul>
<p><a href="data/api/v1/lead-time.json">이 표의 JSON</a> · <a href="s/index.html">서비스별 상태</a></p>
<footer>pamon: 개발자와 사용자가 의존하는 서비스의 외부 관측 기록. 공식 발표가 아닙니다.</footer>
</main></body></html>{{end}}

{{define "lead"}}{{with .}}<h2>공식 발표보다 얼마나 빨랐나</h2>
{{if .Cases}}<table><tr><th>pamon 확인 (KST)</th><th>{{.Provider}} 공식 발표와 비교</th></tr>
{{range .Cases}}<tr><td>{{.Start}}</td><td><span class="h {{.Tone}}">{{.Text}}</span>{{if .Official}}<br><span class="sub">공식: {{if .OfficialURL}}<a href="{{.OfficialURL}}" rel="nofollow">{{.Official}}</a>{{else}}{{.Official}}{{end}}{{if eq .Source "indicator"}} (상태 표시 기준, 5분 단위){{end}}</span>{{end}}</td></tr>{{end}}</table>
{{else}}<p>최근 {{.Days}}일 동안 이 API에 공식 발표와 비교할 장애가 없었습니다.</p>{{end}}
<p class="sub">{{.Provider}} 전체: 비교 {{.Stats.Counted}}건{{if .Median}}, 공식 발표가 있던 {{.Stats.Announced}}건의 중앙값은 pamon이 {{.Median}}{{end}}{{if .Stats.Never}}, 발표 없이 끝난 장애 {{.Stats.Never}}건{{end}}.{{if .Since}} 공식 페이지 기록 시작 {{.Since}}.{{else}} 공식 페이지 기록이 아직 없습니다.{{end}} <a href="../lead-time.html">재는 방법과 전체 표</a></p>{{end}}{{end}}
`
