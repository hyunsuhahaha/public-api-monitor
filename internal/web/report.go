package web

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/report"
)

func (s *Server) reportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/report/"+time.Now().In(config.KST).Format("2006-01"), http.StatusFound)
	})
	mux.HandleFunc("GET /report/{month}", s.reportPage)
	mux.HandleFunc("GET /api/v1/report/{month}", s.reportJSON)
	mux.HandleFunc("GET /feed.xml", s.feed)
}

func (s *Server) buildReport(w http.ResponseWriter, r *http.Request) *report.Report {
	rep, err := report.Build(r.Context(), s.Store, s.Cfg, r.PathValue("month"), time.Now())
	if err != nil {
		if strings.Contains(err.Error(), "YYYY-MM") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return nil
	}
	return rep
}

func (s *Server) reportJSON(w http.ResponseWriter, r *http.Request) {
	rep := s.buildReport(w, r)
	if rep == nil {
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(rep)
}

var reportTmpl = template.Must(template.New("report").Funcs(template.FuncMap{
	"pct":  report.Pct,
	"dur":  report.Dur,
	"kst":  func(t time.Time) string { return t.In(config.KST).Format("1/2 15:04") },
	"prev": func(t time.Time) string { return t.AddDate(0, -1, 0).Format("2006-01") },
	"next": func(t time.Time) string { return t.AddDate(0, 1, 0).Format("2006-01") },
	"inc":  func(i int) int { return i + 1 },
	"sub":  func(a, b int) int { return a - b },
	"bad":  func(v float64) bool { return v >= 0 && v < 0.99 },
}).Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.From.Year}}년 {{printf "%d" .From.Month}}월 공공 API 가용성 리포트</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--text:#1b1f24;--muted:#667080;--line:#e3e6ea;--bad:#e5534b}
@media (prefers-color-scheme:dark){:root{--bg:#0e1116;--card:#161b22;--text:#e6edf3;--muted:#8b949e;--line:#2a313c}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:15px/1.55 -apple-system,"Apple SD Gothic Neo","Malgun Gothic","Noto Sans KR",sans-serif}
main{max-width:960px;margin:0 auto;padding:24px 16px 48px}h1{font-size:22px;margin:0 0 6px}h2{font-size:17px;margin:28px 0 10px}
nav{font-size:13px;color:var(--muted);margin-bottom:16px}nav a{color:inherit}
.kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:10px}
.kpi{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:12px 14px}.kpi b{display:block;font-size:22px}.kpi span{color:var(--muted);font-size:13px}
.wrap{overflow-x:auto;background:var(--card);border:1px solid var(--line);border-radius:10px}
table{border-collapse:collapse;width:100%;font-size:14px}th,td{padding:8px 10px;border-bottom:1px solid var(--line);text-align:left;white-space:nowrap}th{color:var(--muted);font-weight:600}
tr:last-child td{border-bottom:0}.bad{color:var(--bad);font-weight:600}.note{color:var(--muted);font-size:13px}
</style></head><body><main>
<nav><a href="/">← 실시간 상태</a> · <a href="/report/{{prev .From}}">이전 달</a> · <a href="/report/{{next .From}}">다음 달</a> · <a href="/api/v1/report/{{.Month}}">JSON</a></nav>
<h1>{{.From.Year}}년 {{printf "%d" .From.Month}}월 공공 API 가용성 리포트</h1>
{{if .Partial}}<p class="note">이번 달 중간 집계입니다.</p>{{end}}
<div class="kpis">
<div class="kpi"><span>평균 가용률</span><b>{{pct .Availability}}</b></div>
<div class="kpi"><span>장애·성능 저하</span><b>{{.Incidents}}건</b></div>
<div class="kpi"><span>장애 시간 합계</span><b>{{dur .DowntimeMin}}</b></div>
<div class="kpi"><span>감시 API</span><b>{{.WithData}} / {{.APIs}}</b></div>
</div>
<h2>API별 (가용률 낮은 순)</h2>
<div class="wrap"><table><tr><th>#</th><th>API</th><th>기관</th><th>가용률</th><th>장애 시간</th><th>최장</th><th>건수</th><th>p95</th></tr>
{{range $i, $r := .Rows}}{{if ge $r.Availability 0.0}}<tr><td>{{inc $i}}</td><td>{{$r.Name}}</td><td>{{$r.Provider}}</td><td{{if bad $r.Availability}} class="bad"{{end}}>{{pct $r.Availability}}</td><td>{{dur $r.DowntimeMin}}</td><td>{{dur $r.LongestMin}}</td><td>{{$r.Incidents}}</td><td>{{if $r.P95Ms}}{{$r.P95Ms}}ms{{else}}–{{end}}</td></tr>
{{end}}{{end}}</table></div>
{{if lt .WithData .APIs}}<p class="note">이 달 기록이 없는 API {{sub .APIs .WithData}}개는 표에서 뺐습니다 (서비스 키 미설정 등).</p>{{end}}
{{if .Longest}}<h2>가장 길었던 장애</h2>
<div class="wrap"><table><tr><th>API</th><th>시작 (KST)</th><th>길이</th><th>원인</th></tr>
{{range .Longest}}<tr><td>{{.Name}}</td><td>{{kst .Started}}</td><td>{{dur .Minutes}}{{if .Ongoing}} (진행 중){{end}}</td><td>{{.Reason}}</td></tr>
{{end}}</table></div>{{end}}
<h2>기관별</h2>
<div class="wrap"><table><tr><th>기관</th><th>API 수</th><th>평균 가용률</th><th>장애 시간</th></tr>
{{range .Providers}}<tr><td>{{.Name}}</td><td>{{.APIs}}</td><td{{if bad .Availability}} class="bad"{{end}}>{{pct .Availability}}</td><td>{{dur .DowntimeMin}}</td></tr>
{{end}}</table></div>
<p class="note">응답 본문의 결과 코드까지 읽어 판정합니다. 서비스 키·호출 한도 같은 감시 쪽 문제는 제외하고, 장애 시간은 '장애' 등급만 합산합니다.</p>
</main></body></html>`))

func (s *Server) reportPage(w http.ResponseWriter, r *http.Request) {
	rep := s.buildReport(w, r)
	if rep == nil {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := reportTmpl.Execute(w, rep); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}

// Atom feed of incidents (newest 50), for feed readers and Slack's RSS app.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}
type atomEntry struct {
	Title   string   `xml:"title"`
	ID      string   `xml:"id"`
	Updated string   `xml:"updated"`
	Link    atomLink `xml:"link"`
	Summary string   `xml:"summary"`
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Incidents(r.Context(), "", time.Now().AddDate(0, 0, -90), 50)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	base := s.PublicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	base = strings.TrimRight(base, "/")
	names := map[string]string{}
	for _, t := range s.Cfg.Targets {
		names[t.ID] = t.Name
	}
	f := atomFeed{Title: "공공 API 상태판: 장애", ID: base + "/feed.xml", Updated: time.Now().UTC().Format(time.RFC3339),
		Link: []atomLink{{Href: base + "/"}, {Href: base + "/feed.xml", Rel: "self"}}}
	for _, in := range list {
		name := names[in.TargetID]
		if name == "" {
			name = in.TargetID
		}
		label := "장애"
		if in.Severity < 2 {
			label = "성능 저하"
		}
		updated := in.Started
		summary := fmt.Sprintf("%s 시작. %s", in.Started.In(config.KST).Format("2006-01-02 15:04 KST"), in.Reason)
		title := fmt.Sprintf("[%s] %s", label, name)
		if in.Open() {
			title += " (진행 중)"
		} else {
			updated = in.Ended
			title += " (복구, " + report.Dur(int(in.Ended.Sub(in.Started).Round(time.Minute).Minutes())) + ")"
			summary += fmt.Sprintf(" %s 복구.", in.Ended.In(config.KST).Format("15:04"))
		}
		f.Entries = append(f.Entries, atomEntry{
			Title: title, ID: fmt.Sprintf("%s/incidents/%d", base, in.ID), Updated: updated.UTC().Format(time.RFC3339),
			Link: atomLink{Href: base + "/#" + in.TargetID}, Summary: summary,
		})
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(f)
}
