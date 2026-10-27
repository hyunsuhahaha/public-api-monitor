package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// Incident pages (/i/{date}-{target}-{id}.html, and /i/ listing them): one dated page per
// real outage, worded the way people search for one afterwards ("2026년 10월 27일 카카오
// 로그인 장애"). A search engine (and the AI search built on it) has nothing to rank for
// that question until a page says exactly that, with the times, where it broke and what
// the provider said. Short blips do not get a page: a page per two-check flap would be
// thin, and noise for the reader.

// IncidentPageDays: how far back the listing and the export go.
const IncidentPageDays = 90

// incidentWorthAPage: an outage, or a degradation that lasted 15 minutes. Monitor-side
// problems (our key or quota) are never the provider's incident.
func incidentWorthAPage(in *incident.Incident, now time.Time) bool {
	if in.Status == probe.StatusClient || in.Status == probe.StatusMaint {
		return false
	}
	end := in.Ended
	if in.Open() {
		end = now
	}
	return in.Severity >= 2 || end.Sub(in.Started) >= 15*time.Minute
}

// IncidentSlug is the page's file name: 2026-10-27-kakao-login-412.html.
func IncidentSlug(in *incident.Incident) string {
	return in.Started.In(config.KST).Format("2006-01-02") + "-" + in.TargetID + "-" + strconv.FormatInt(in.ID, 10) + ".html"
}

// IncidentPages lists the incidents that get a page, newest first.
func (s *Server) IncidentPages(ctx context.Context, now time.Time) ([]*incident.Incident, error) {
	list, err := s.Store.Incidents(ctx, "", now.AddDate(0, 0, -IncidentPageDays), 2000)
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, in := range list {
		if s.target(in.TargetID) != nil && incidentWorthAPage(in, now) {
			out = append(out, in)
		}
	}
	return out, nil
}

type incidentPage struct {
	In          *incident.Incident
	T           *config.Target
	Slug        string
	Title       string // 2026년 10월 27일 카카오 로그인 (OpenID 설정) 장애
	Summary     string // what happened, in two sentences (also the meta description)
	Date        string // 2026년 10월 27일
	Start, End  string // KST
	Duration    string
	Open        bool
	Kind        string // 장애 / 저하
	Where       string // where it broke, in plain words
	Locations   []incidentLoc
	Lead        *leadView
	Others      []incidentOther // other services failing at the same time
	Site        string
	Generated   string
	GeneratedAt time.Time
	Published   time.Time
}

type incidentLoc struct {
	Name         string
	Failed, Seen int
}

type leadView struct{ Tone, Text, Official, URL string }

type incidentOther struct{ Name, Provider, Href, Start, Duration string }

type incidentIndex struct {
	Site, Generated string
	GeneratedAt     time.Time
	Months          []incidentMonth
	Count           int
}

type incidentMonth struct {
	Name  string
	Items []incidentItem
}

type incidentItem struct{ Href, Date, Time, Name, Provider, Kind, Duration, Where string }

func koDate(t time.Time) string { return t.In(config.KST).Format("2006년 1월 2일") }

func (s *Server) incidentPageData(ctx context.Context, in *incident.Incident, now time.Time) (*incidentPage, error) {
	t := s.target(in.TargetID)
	end := in.Ended
	if in.Open() {
		end = now
	}
	kind := map[bool]string{true: "장애", false: "저하"}[in.Severity >= 2]
	p := &incidentPage{In: in, T: t, Slug: IncidentSlug(in), Date: koDate(in.Started), Start: kst(in.Started), Open: in.Open(),
		Duration: koDuration(end.Sub(in.Started)), Kind: kind, Where: plainReason(in.Reason), Site: s.siteBase(),
		Generated: kst(now), GeneratedAt: now, Published: in.Started}
	if p.Where == "" {
		p.Where = "실패 사유가 기록되지 않았습니다."
	}
	p.Title = p.Date + " " + t.Name + " " + kind
	if in.Open() {
		p.End = "진행 중"
		p.Summary = fmt.Sprintf("%s %s(KST)부터 %s(%s)이 외부에서 실패하고 있습니다. %s 마지막 확인 %s 기준 %s째입니다.",
			p.Date, in.Started.In(config.KST).Format("15:04"), t.Name, t.Provider, p.Where, kst(now), p.Duration)
	} else {
		p.End = kst(in.Ended)
		p.Summary = fmt.Sprintf("%s %s(KST)부터 %s 동안 %s(%s)이 외부에서 실패했습니다. %s %s에 복구됐습니다.",
			p.Date, in.Started.In(config.KST).Format("15:04"), p.Duration, t.Name, t.Provider, p.Where, in.Ended.In(config.KST).Format("15:04"))
	}
	// how each location saw it: a failure from one region only points at that region
	checks, err := s.Store.ChecksBetween(ctx, in.TargetID, in.Started, end, 2000)
	if err != nil {
		return nil, err
	}
	byLoc := map[string]*incidentLoc{}
	var order []string
	for _, c := range checks {
		l := byLoc[c.Location]
		if l == nil {
			l = &incidentLoc{Name: s.Cfg.LocationName(c.Location)}
			byLoc[c.Location] = l
			order = append(order, c.Location)
		}
		l.Seen++
		if !c.Status.Healthy() && c.Status != probe.StatusClient {
			l.Failed++
		}
	}
	for _, id := range order {
		p.Locations = append(p.Locations, *byLoc[id])
	}
	if r, err := s.leadReport(ctx); err == nil {
		for _, res := range r.Results {
			for _, id := range res.Incidents {
				if id == in.ID {
					tone, text := leadText(res)
					p.Lead = &leadView{Tone: tone, Text: text, Official: res.Official, URL: res.OfficialURL}
				}
			}
		}
	}
	others, err := s.Store.IncidentsOverlapping(ctx, in.Started.Add(-15*time.Minute), end.Add(15*time.Minute))
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		ot := s.target(o.TargetID)
		if o.ID == in.ID || ot == nil || o.Status == probe.StatusClient {
			continue
		}
		href := "../s/" + ot.ID + ".html"
		if incidentWorthAPage(o, now) {
			href = IncidentSlug(o)
		}
		p.Others = append(p.Others, incidentOther{Name: ot.Name, Provider: ot.Provider, Href: href, Start: kst(o.Started), Duration: incidentLength(o, now)})
		if len(p.Others) == 20 {
			break
		}
	}
	return p, nil
}

// LD: a schema.org Report about the service, dated when the outage began.
func (p *incidentPage) LD() map[string]any {
	mod := p.GeneratedAt
	if !p.Open {
		mod = p.In.Ended
	}
	return map[string]any{"@context": "https://schema.org", "@type": "Report", "inLanguage": "ko",
		"headline": p.Title, "name": p.Title, "description": p.Summary, "url": p.Site + "/i/" + p.Slug,
		"datePublished": p.Published.UTC().Format(time.RFC3339), "dateModified": mod.UTC().Format(time.RFC3339),
		"author":    map[string]any{"@type": "Organization", "name": "pamon", "url": p.Site + "/"},
		"publisher": map[string]any{"@type": "Organization", "name": "pamon", "url": p.Site + "/"},
		"about": map[string]any{"@type": "Service", "name": p.T.Name, "category": p.T.Category,
			"provider": map[string]any{"@type": "Organization", "name": p.T.Provider}}}
}

func (p *incidentIndex) LD() map[string]any {
	return map[string]any{"@context": "https://schema.org", "@type": "CollectionPage", "inLanguage": "ko",
		"name": "최근 서비스 장애 기록", "url": p.Site + "/i/", "dateModified": p.GeneratedAt.UTC().Format(time.RFC3339),
		"description": fmt.Sprintf("최근 %d일 동안 pamon이 외부에서 확인한 서비스 장애 %d건의 날짜·시간·길이·실패 단계.", IncidentPageDays, p.Count)}
}

func (s *Server) incidentPage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	slug := r.PathValue("slug")
	if slug == "" || slug == "index.html" {
		list, err := s.IncidentPages(r.Context(), now)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		idx := &incidentIndex{Site: s.siteBase(), Generated: kst(now), GeneratedAt: now, Count: len(list)}
		for _, in := range list {
			t := s.target(in.TargetID)
			month := in.Started.In(config.KST).Format("2006년 1월")
			if len(idx.Months) == 0 || idx.Months[len(idx.Months)-1].Name != month {
				idx.Months = append(idx.Months, incidentMonth{Name: month})
			}
			m := &idx.Months[len(idx.Months)-1]
			m.Items = append(m.Items, incidentItem{Href: IncidentSlug(in), Date: in.Started.In(config.KST).Format("1월 2일"),
				Time: in.Started.In(config.KST).Format("15:04"), Name: t.Name, Provider: t.Provider,
				Kind: map[bool]string{true: "장애", false: "저하"}[in.Severity >= 2], Duration: incidentLength(in, now), Where: plainReason(in.Reason)})
		}
		writeIncidentHTML(w, "incidents", idx)
		return
	}
	i := strings.LastIndex(strings.TrimSuffix(slug, ".html"), "-")
	id, err := strconv.ParseInt(strings.TrimSuffix(slug, ".html")[i+1:], 10, 64)
	if i < 0 || err != nil {
		http.NotFound(w, r)
		return
	}
	in, err := s.Store.Incident(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if in == nil || s.target(in.TargetID) == nil || !incidentWorthAPage(in, now) {
		http.NotFound(w, r)
		return
	}
	if want := IncidentSlug(in); slug != want {
		http.Redirect(w, r, want, http.StatusMovedPermanently)
		return
	}
	p, err := s.incidentPageData(r.Context(), in, now)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeIncidentHTML(w, "incident", p)
}

func writeIncidentHTML(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	incidentTmpl.ExecuteTemplate(w, name, data)
}

// incidentHref: the incident's page from an answer page (s/), "" when it has none.
func incidentHref(in *incident.Incident, now time.Time) string {
	if !incidentWorthAPage(in, now) {
		return ""
	}
	return "../i/" + IncidentSlug(in)
}

var incidentTmpl = template.Must(template.Must(answerTmpl.Clone()).Parse(`
{{define "incident"}}{{template "head" .}}
<title>{{.Title}} ({{.Start}}{{if not .Open}}~{{.End}}{{end}}, {{.Duration}}) | pamon</title>
<meta name="description" content="{{.Summary}}">
<script type="application/ld+json">{{.LD}}</script>
</head><body><main>
<nav><a href="./">장애 기록</a> › <a href="../s/{{.T.ID}}.html">{{.T.Name}}</a></nav>
<h1>{{.Title}}</h1>
<div class="sub">{{.T.Provider}} · {{.T.Category}} · 외부 감시 기록 (공식 발표가 아닙니다)</div>

<section class="verdict {{if .Open}}bad{{else}}warn{{end}}">
  <b class="h">{{if .Open}}진행 중인 {{.Kind}}{{else}}복구된 {{.Kind}}{{end}} · {{.Duration}}</b>
  <p>{{.Summary}}</p>
  <p class="sub">갱신 시각 <time datetime="{{.GeneratedAt.UTC.Format "2006-01-02T15:04:05Z"}}">{{.Generated}}</time> (KST) · 지금 상태는 <a href="../s/{{.T.ID}}.html">{{.T.Name}} 지금 되나요?</a></p>
</section>

<table>
<tr><th style="width:30%">시작</th><td><time datetime="{{.In.Started.UTC.Format "2006-01-02T15:04:05Z"}}">{{.Start}}</time> (KST)</td></tr>
<tr><th>복구</th><td>{{if .Open}}아직 복구되지 않았습니다{{else}}<time datetime="{{.In.Ended.UTC.Format "2006-01-02T15:04:05Z"}}">{{.End}}</time> (KST){{end}}</td></tr>
<tr><th>길이</th><td>{{.Duration}}{{if .Open}} (진행 중){{end}}</td></tr>
<tr><th>어디서 실패했나</th><td>{{.Where}}</td></tr>
{{if .In.Flaps}}<tr><th>오락가락</th><td>중간에 {{.In.Flaps}}번 잠깐 회복했다가 다시 실패했습니다.</td></tr>{{end}}
</table>

{{if .Locations}}<h2>지점별로 본 실패</h2>
<table><tr><th>지점</th><th class="n">실패 / 확인</th></tr>
{{range .Locations}}<tr><td>{{.Name}}</td><td class="n">{{.Failed}} / {{.Seen}}</td></tr>{{end}}</table>
<p class="sub">한 지점에서만 실패했다면 그 지역의 네트워크나 접속 제한 문제일 수 있습니다.</p>{{end}}

<h2>{{.T.Provider}} 공식 발표</h2>
{{with .Lead}}<p class="{{.Tone}}"><b class="h">{{.Text}}</b>{{if .Official}}<br>공식 발표: {{.Official}}{{end}}{{if .URL}} · <a href="{{.URL}}" rel="nofollow">원문</a>{{end}}</p>
{{else}}<p>pamon이 읽는 공식 상태 페이지가 없거나, 이 장애와 비교할 기록이 없습니다.</p>{{end}}

{{if .Others}}<h2>같은 시간에 함께 실패한 서비스</h2>
<table>{{range .Others}}<tr><td><a href="{{.Href}}">{{.Name}}</a> <span class="sub">{{.Provider}}</span></td><td>{{.Start}}</td><td class="n">{{.Duration}}</td></tr>{{end}}</table>
<p class="sub">여러 서비스가 함께 실패하면 공통 기반(클라우드·CDN·DNS)의 문제일 수 있습니다. 시간이 겹친다는 것만으로 원인을 단정할 수는 없습니다.</p>{{end}}

<h2>무엇을 확인했나</h2>
<p>pamon은 {{.T.Name}}의 고정된 읽기 기능 하나에 5분마다 직접 요청을 보내 응답 코드와 내용까지 확인합니다. 연속 두 번 실패하면 장애로 기록하고, 두 번 연속 성공하면 복구로 봅니다. 로그인·결제를 실제로 하지는 않으므로 계정에 따른 문제는 보이지 않습니다.</p>
<p><a href="../s/{{.T.ID}}.html">{{.T.Name}} 지금 상태와 최근 30일</a> · <a href="./">다른 장애 기록</a> · <a href="../ai/targets/{{.T.ID}}.json">AI·개발자용 JSON</a></p>
<footer>pamon: 개발자와 사용자가 의존하는 서비스의 외부 관측 기록.</footer>
</main></body></html>{{end}}

{{define "incidents"}}{{template "head" .}}
<title>최근 서비스 장애 기록: 카카오·네이버·홈택스·은행·결제·AI·클라우드 | pamon</title>
<meta name="description" content="최근 {{len .Months}}개월 동안 외부에서 확인한 서비스 장애 {{.Count}}건. 날짜, 시간, 길이, 어디서 실패했는지.">
<script type="application/ld+json">{{.LD}}</script>
</head><body><main>
<nav><a href="../s/">서비스별 상태</a></nav>
<h1>최근 서비스 장애 기록</h1>
<p class="sub">5분마다 외부에서 직접 확인해 기록한 장애입니다(짧은 단발 실패는 빼고, 장애이거나 15분 넘게 이어진 저하만). 갱신 시각 <time datetime="{{.GeneratedAt.UTC.Format "2006-01-02T15:04:05Z"}}">{{.Generated}}</time> (KST) · 공식 발표가 아닙니다.</p>
{{range .Months}}<section class="cat"><h2>{{.Name}}</h2>
{{range .Items}}<div class="svc"><a href="{{.Href}}">{{.Date}} {{.Time}} {{.Name}} {{.Kind}} <span class="sub">{{.Provider}}</span></a><span class="sub">{{.Duration}}</span></div>{{end}}
</section>{{else}}<p>최근 {{len .Months}}개월 동안 기록된 장애가 없습니다.</p>{{end}}
<footer><a href="../s/">서비스별 지금 상태</a> · <a href="../lead-time.html">공식 발표보다 얼마나 빨랐나</a> · <a href="../llms.txt">AI용 안내</a></footer>
</main></body></html>{{end}}
`))
