package web

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Badges and widgets for other people's pages: a README, a blog, an internal wiki.
//
//	/badge/{id}.svg         30-day availability, coloured by the current status
//	/badge/{id}-status.svg  the current status in words: 정상, 장애, 느림, ...
//	/badge/summary.svg      how many of the monitored services fail right now
//	/w/{id}.html            a small card for an <iframe>: verdict, 30 days, a link
//
// All are static files in the export, regenerated every cycle. A badge says 확인 불가
// when its observation is older than two intervals, never a stale 정상.

// DefaultSiteURL is the public address used in copy-and-paste snippets when the server
// has no PublicURL.
const DefaultSiteURL = "https://pamon.hyunsuhahaha.workers.dev"

const (
	colorOK      = "#3fb950"
	colorWarn    = "#d29922"
	colorBad     = "#f85149"
	colorMaint   = "#5794f2"
	colorUnknown = "#9f9f9f"
)

// textWidth estimates rendered width at 11px Verdana: wide for Hangul and other CJK.
func textWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r >= 0x1100 && r <= 0xFFDC:
			w += 12
		case r == ' ' || r == '.' || r == ',' || r == ':' || r == '(' || r == ')' || r == '·':
			w += 4
		default:
			w += 7
		}
	}
	return w
}

// shorten keeps a label to n characters.
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func badgeSVG(label, value, color string) []byte {
	label, value = template.HTMLEscapeString(label), template.HTMLEscapeString(value)
	lw, vw := textWidth(label)+12, textWidth(value)+12
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s"><title>%s: %s</title>`+
		`<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`+
		`<clipPath id="r"><rect width="%d" height="20" rx="3" fill="#fff"/></clipPath><g clip-path="url(#r)">`+
		`<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/><rect width="%d" height="20" fill="url(#s)"/></g>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,'Apple SD Gothic Neo','Malgun Gothic',sans-serif" font-size="11">`+
		`<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text>`+
		`<text x="%d" y="15" fill="#010101" fill-opacity=".3">%s</text><text x="%d" y="14">%s</text></g></svg>`,
		lw+vw, label, value, label, value, lw+vw, lw, lw, vw, color, lw+vw,
		lw/2, label, lw/2, label, lw+vw/2, value, lw+vw/2, value))
}

// statusWords is the badge's word and colour for a target's current status.
func (s *Server) statusWords(t TargetView, now time.Time) (string, string) {
	if t.CheckedAt == nil || now.Sub(*t.CheckedAt) > 2*s.Cfg.Interval.Duration {
		return "확인 불가", colorUnknown
	}
	switch t.Status {
	case probe.StatusOK:
		return "정상", colorOK
	case probe.StatusSlow:
		return "느림", colorWarn
	case probe.StatusStale:
		return "데이터 이상", colorWarn
	case probe.StatusError, probe.StatusDown:
		return "장애", colorBad
	case probe.StatusMaint:
		return "점검 중", colorMaint
	}
	return "확인 불가", colorUnknown
}

func writeSVG(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(b)
}

func (s *Server) badge(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("id")
	if !strings.HasSuffix(name, ".svg") {
		http.NotFound(w, r)
		return
	}
	name = strings.TrimSuffix(name, ".svg")
	v, _, err := s.view(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	if name == "summary" {
		bad, warn, known := 0, 0, 0
		for _, t := range v.Targets {
			word, _ := s.statusWords(t, now)
			switch word {
			case "장애":
				bad++
			case "느림", "데이터 이상":
				warn++
			}
			if word != "확인 불가" {
				known++
			}
		}
		value, color := fmt.Sprintf("%d개 모두 정상", known), colorOK
		switch {
		case bad > 0:
			value, color = fmt.Sprintf("%d개 중 %d개 장애", known, bad), colorBad
		case warn > 0:
			value, color = fmt.Sprintf("%d개 중 %d개 느림·이상", known, warn), colorWarn
		case known == 0:
			value, color = "확인 불가", colorUnknown
		}
		writeSVG(w, badgeSVG("pamon", value, color))
		return
	}
	id, status := strings.CutSuffix(name, "-status")
	for _, t := range v.Targets {
		if t.ID != id {
			continue
		}
		word, color := s.statusWords(t, now)
		if status {
			label := r.URL.Query().Get("label")
			if label == "" {
				label = t.Name
			}
			writeSVG(w, badgeSVG(shorten(label, 24), word, color))
			return
		}
		value := "기록 없음"
		if t.Uptime30d >= 0 {
			value = fmt.Sprintf("%.2f%%", t.Uptime30d*100)
		}
		writeSVG(w, badgeSVG("30일 가용률", value, color))
		return
	}
	http.NotFound(w, r)
}

type widgetPage struct {
	T                 TargetView
	V                 Verdict
	Days              []dayCell
	Uptime30, Checked string
	Link, Base        string
}

// widget is /w/{id}.html, a card meant for an iframe (360 x 170).
func (s *Server) widget(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".html")
	v, _, err := s.view(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	for _, t := range v.Targets {
		if t.ID != id {
			continue
		}
		base := s.siteBase()
		p := widgetPage{T: t, V: Explain(t.Status, t.Reason, t.CheckedAt, now, s.Cfg.Interval.Duration), Uptime30: pct(t.Uptime30d),
			Link: base + "/s/" + t.ID + ".html", Base: base}
		if t.CheckedAt != nil {
			p.Checked = t.CheckedAt.In(config.KST).Format("1/2 15:04")
		}
		// always 30 slots, oldest first: a new service shows grey days, not one wide bar
		p.Days = dayCells(t.Days)
		for len(p.Days) < 30 {
			p.Days = append([]dayCell{{Tone: "none", Title: "기록 없음"}}, p.Days...)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		widgetTmpl.Execute(w, p)
		return
	}
	http.NotFound(w, r)
}

func (s *Server) siteBase() string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	return DefaultSiteURL
}

// dayCells: the last 30 days as coloured cells (the answer page's bar).
func dayCells(days []store.Day) []dayCell {
	if len(days) > 30 {
		days = days[len(days)-30:]
	}
	var out []dayCell
	for _, d := range days {
		c := dayCell{Date: d.Date, Tone: "none", Title: d.Date + " 기록 없음"}
		if d.Counted > 0 {
			c.Tone, c.Title = "ok", fmt.Sprintf("%s 가용률 %.2f%%", d.Date, float64(d.Healthy)/float64(d.Counted)*100)
			if d.Outage > 0 {
				c.Tone = "bad"
			} else if d.Degraded > 0 {
				c.Tone = "warn"
			}
		}
		out = append(out, c)
	}
	return out
}

var widgetTmpl = template.Must(template.New("w").Parse(`<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.T.Name}} 상태 | pamon</title><meta name="robots" content="noindex">
<style>
:root{--bg:#fff;--text:#1b1f24;--muted:#667080;--line:#e3e6ea;--ok:#2da44e;--warn:#bf8700;--bad:#cf222e;--none:#d0d7de;--accent:#0969da}
@media (prefers-color-scheme:dark){:root{--bg:#0d1117;--text:#e6edf3;--muted:#8b949e;--line:#30363d;--ok:#3fb950;--warn:#d29922;--bad:#f85149;--none:#30363d;--accent:#58a6ff}}
*{box-sizing:border-box}html,body{margin:0;background:var(--bg);color:var(--text);font:13px/1.45 system-ui,-apple-system,"Apple SD Gothic Neo","Malgun Gothic",sans-serif}
a.card{display:block;padding:12px 14px;border:1px solid var(--line);border-radius:10px;color:inherit;text-decoration:none;height:100vh;max-height:170px;overflow:hidden}
.top{display:flex;justify-content:space-between;gap:8px;align-items:baseline}.name{font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;min-width:0}
.top .sub{white-space:nowrap;flex-shrink:0}
.sub{color:var(--muted);font-size:12px}.h{font-weight:700;font-size:15px;margin:6px 0 2px}
.ok .h{color:var(--ok)}.warn .h{color:var(--warn)}.bad .h{color:var(--bad)}.unknown .h{color:var(--muted)}
.days{display:flex;gap:2px;margin:8px 0 4px}.days span{flex:1;height:16px;border-radius:2px;background:var(--none)}
.days .ok{background:var(--ok)}.days .warn{background:var(--warn)}.days .bad{background:var(--bad)}
.foot{display:flex;justify-content:space-between}.more{color:var(--accent)}
</style></head><body>
<a class="card {{.V.Tone}}" href="{{.Link}}" target="_blank" rel="noopener">
<div class="top"><span class="name">{{.T.Name}}</span><span class="sub">{{.T.Provider}}</span></div>
<div class="h">{{.V.Headline}}</div>
<div class="sub">{{if .Checked}}{{.Checked}} (KST) 확인{{else}}확인 기록 없음{{end}} · 30일 가용률 {{.Uptime30}}</div>
<div class="days">{{range .Days}}<span class="{{.Tone}}" title="{{.Title}}"></span>{{end}}</div>
<div class="foot sub"><span>pamon 외부 관측 (공식 발표 아님)</span><span class="more">자세히 →</span></div>
</a></body></html>`))
