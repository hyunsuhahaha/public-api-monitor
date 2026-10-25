package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// Maintenance feed: the announced windows of targets.yaml as JSON (for a circuit breaker
// or a deploy script: "skip calls during this window"), as an iCalendar feed (subscribe
// in Google Calendar or Outlook) and as a page. Windows that ended more than
// MaintKeep ago are left out.

// MaintKeep is how long an ended window stays in the feed.
const MaintKeep = 30 * 24 * time.Hour

// MaintWindow is one announced window, as /maintenance.json publishes it.
type MaintWindow struct {
	ID      string        `json:"id"` // stable while start, end and match stay the same
	Start   time.Time     `json:"start"`
	End     time.Time     `json:"end"`
	State   string        `json:"state"` // upcoming, active, ended
	Note    string        `json:"note"`
	URL     string        `json:"url,omitempty"` // the provider's notice
	Match   []string      `json:"match"`
	Targets []MaintTarget `json:"targets"`
}

// MaintTarget is a monitored API a window covers.
type MaintTarget struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// MaintFeed is /maintenance.json.
type MaintFeed struct {
	SchemaVersion string        `json:"schemaVersion"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Usage         string        `json:"usage"`
	Windows       []MaintWindow `json:"windows"` // by start
}

func maintID(m config.Maintenance) string {
	h := sha256.Sum256([]byte(m.Start.UTC().Format(time.RFC3339) + "|" + m.End.UTC().Format(time.RFC3339) + "|" + strings.Join(m.Match, ",")))
	return hex.EncodeToString(h[:6])
}

// maintWindows lists the windows that are coming, going on, or ended within MaintKeep.
func (s *Server) maintWindows(now time.Time) []MaintWindow {
	out := []MaintWindow{}
	for _, m := range s.Cfg.Maintenance {
		if m.End.Before(now.Add(-MaintKeep)) {
			continue
		}
		w := MaintWindow{ID: maintID(m), Start: m.Start, End: m.End, Note: m.Note, URL: m.URL, Match: m.Match, State: "upcoming", Targets: []MaintTarget{}}
		switch {
		case !now.Before(m.End):
			w.State = "ended"
		case !now.Before(m.Start):
			w.State = "active"
		}
		for _, t := range s.Cfg.Targets {
			if slices.Contains(m.Match, t.ID) || slices.Contains(m.Match, t.Provider) {
				w.Targets = append(w.Targets, MaintTarget{t.ID, t.Name, t.Provider})
			}
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (s *Server) maintJSON(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(MaintFeed{SchemaVersion: "1.0", GeneratedAt: time.Now(), Windows: s.maintWindows(time.Now()),
		Usage: "Announced maintenance windows of the APIs pamon monitors, as the providers posted them. Before calling a target, skip or queue the call (do not retry) when now is in [start, end) of a window whose targets include it. Times are RFC 3339. Re-read at most every 5 minutes. A window here is an announcement, not an observation: check /ai/targets/{id}.json for what pamon saw."})
}

// ICS: RFC 5545. Lines end in CRLF and fold at 75 octets.
func (s *Server) maintICS(w http.ResponseWriter, r *http.Request) {
	base := s.baseURL(r)
	now := time.Now()
	var b strings.Builder
	line := func(k, v string) { b.WriteString(icsFold(k + ":" + v)) }
	line("BEGIN", "VCALENDAR")
	line("VERSION", "2.0")
	line("PRODID", "-//pamon//maintenance//KO")
	line("CALSCALE", "GREGORIAN")
	line("METHOD", "PUBLISH")
	line("X-WR-CALNAME", icsText("pamon: 공지된 점검"))
	line("X-WR-TIMEZONE", "Asia/Seoul")
	line("REFRESH-INTERVAL;VALUE=DURATION", "PT1H")
	stamp := now.UTC().Format("20060102T150405Z")
	for _, m := range s.maintWindows(now) {
		names := make([]string, 0, len(m.Targets))
		for _, t := range m.Targets {
			names = append(names, t.Name)
		}
		summary := "[점검] " + strings.Join(m.Match, ", ")
		if m.Note != "" {
			summary += ": " + m.Note
		}
		desc := "대상 API: " + strings.Join(names, ", ") + "\n이 시간에는 호출을 미루거나 큐에 넣고, 재시도하지 마세요."
		if m.URL != "" {
			desc += "\n공지: " + m.URL
		}
		line("BEGIN", "VEVENT")
		line("UID", m.ID+"@pamon")
		line("DTSTAMP", stamp)
		line("DTSTART", m.Start.UTC().Format("20060102T150405Z"))
		line("DTEND", m.End.UTC().Format("20060102T150405Z"))
		line("SUMMARY", icsText(summary))
		line("DESCRIPTION", icsText(desc))
		if m.URL != "" {
			line("URL", m.URL)
		} else if base != "" {
			line("URL", base+"/maintenance.html")
		}
		line("TRANSP", "TRANSPARENT")
		line("END", "VEVENT")
	}
	line("END", "VCALENDAR")
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(b.String()))
}

// icsText escapes a TEXT value.
func icsText(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\r\n", `\n`, "\n", `\n`).Replace(s)
}

// icsFold breaks a content line into 75-octet pieces without splitting a UTF-8 character.
func icsFold(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		size := len(string(r))
		if n+size > 75 {
			b.WriteString("\r\n ")
			n = 1
		}
		b.WriteRune(r)
		n += size
	}
	b.WriteString("\r\n")
	return b.String()
}

// baseURL is the public address without a trailing slash: PublicURL, else the request's.
func (s *Server) baseURL(r *http.Request) string {
	base := s.PublicURL
	if base == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return strings.TrimRight(base, "/")
}

// maintRow is a window on a page.
type maintRow struct {
	MaintWindow
	When, StateText, Tone, Who string
}

func maintRows(ws []MaintWindow) []maintRow {
	var out []maintRow
	for _, m := range ws {
		row := maintRow{MaintWindow: m, When: kst(m.Start) + " ~ " + m.End.In(config.KST).Format("15:04"), Who: strings.Join(m.Match, ", ")}
		if m.End.Sub(m.Start) >= 24*time.Hour || m.End.In(config.KST).Format("01-02") != m.Start.In(config.KST).Format("01-02") {
			row.When = kst(m.Start) + " ~ " + kst(m.End)
		}
		switch m.State {
		case "active":
			row.StateText, row.Tone = "지금 점검 중", "warn"
		case "upcoming":
			row.StateText, row.Tone = "예정", "ok"
		default:
			row.StateText = "끝남"
		}
		out = append(out, row)
	}
	return out
}

// maintFor is the windows that cover one target.
func (s *Server) maintFor(id string, now time.Time) []maintRow {
	var ws []MaintWindow
	for _, m := range s.maintWindows(now) {
		if slices.ContainsFunc(m.Targets, func(t MaintTarget) bool { return t.ID == id }) {
			ws = append(ws, m)
		}
	}
	return maintRows(ws)
}

type maintPage struct {
	Rows      []maintRow
	Generated string
}

func (s *Server) maintPage(w http.ResponseWriter, r *http.Request) {
	p := maintPage{Rows: maintRows(s.maintWindows(time.Now())), Generated: kst(time.Now())}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	answerTmpl.ExecuteTemplate(w, "maintenance", p)
}

func init() {
	template.Must(answerTmpl.Parse(maintTemplates))
}

const maintTemplates = `
{{define "maintenance"}}{{template "head" .}}
<title>공지된 점검 일정: 공공 API·서비스 점검 캘린더 | pamon</title>
<meta name="description" content="pamon이 감시하는 공공 API와 서비스의 공지된 점검 일정입니다. 캘린더(iCal)로 구독하거나 JSON으로 읽어 서킷 브레이커에 연결할 수 있습니다.">
<link rel="alternate" type="text/calendar" href="maintenance.ics">
<link rel="alternate" type="application/json" href="maintenance.json">
</head><body><main>
<nav><a href="s/index.html">서비스별 상태</a> › 점검 일정</nav>
<h1>공지된 점검 일정</h1>
<p class="sub">제공 기관이 공지한 점검을 pamon에 등록한 것입니다. 데이터 생성 {{.Generated}} (KST) · 지난 30일 안에 끝난 점검까지 보입니다.</p>
{{if .Rows}}<table><tr><th>시간 (KST)</th><th>대상</th><th>내용</th></tr>
{{range .Rows}}<tr><td>{{.When}}<br><span class="h {{.Tone}}">{{.StateText}}</span></td><td>{{.Who}}<br><span class="sub">{{range $i, $t := .Targets}}{{if $i}}, {{end}}<a href="s/{{$t.ID}}.html">{{$t.Name}}</a>{{end}}</span></td>
<td>{{.Note}}{{if .URL}}<br><a href="{{.URL}}" rel="nofollow">공지</a>{{end}}</td></tr>{{end}}</table>
{{else}}<section class="verdict ok"><b class="h">등록된 점검 일정이 없습니다</b><p>예정된 점검이 공지되면 여기와 아래 피드에 나타납니다.</p></section>{{end}}

<h2>구독하기</h2>
<ul>
<li><b>캘린더</b>: <a href="maintenance.ics">maintenance.ics</a> 주소를 Google 캘린더(다른 캘린더 → URL로 추가)나 Outlook(인터넷 캘린더 구독)에 넣으면 점검 일정이 자동으로 들어옵니다.</li>
<li><b>JSON</b>: <a href="maintenance.json">maintenance.json</a>. 창마다 <code>start</code>, <code>end</code>, <code>state</code>, 대상 API의 <code>targets[].id</code>가 있습니다.</li>
</ul>

<h2>서킷 브레이커에 연결하기</h2>
<p>점검 시간에 재시도를 계속하면 호출 한도를 다 쓰거나 IP가 차단될 수 있습니다. 호출 전에 점검 창을 확인해 그 시간에는 요청을 미루거나 큐에 넣으세요. 이 JSON은 5분마다 다시 만들어지므로 그보다 자주 읽을 필요가 없습니다.</p>
<pre style="overflow-x:auto;background:#0e1420;border:1px solid var(--line);border-radius:8px;padding:12px;font-size:13px">// Go: github.com/hyunsuhahaha/public-api-monitor/pkg/pamonclient
c := pamonclient.New("https://pamon.hyunsuhahaha.workers.dev")
b := c.Breaker("airkorea-sido", pamonclient.BreakerOptions{})
err := b.Do(ctx, func(ctx context.Context) error { return callAPI(ctx) })
if errors.Is(err, pamonclient.ErrMaintenance) { /* 점검 중: 큐에 넣고 나중에 */ }

// 다른 언어: 점검 창 안이면 호출하지 않기
const now = Date.now();
const {windows} = await (await fetch(PAMON + "/maintenance.json")).json();
const inMaint = windows.some(w => w.targets.some(t => t.id === "airkorea-sido")
  &amp;&amp; Date.parse(w.start) &lt;= now &amp;&amp; now &lt; Date.parse(w.end));</pre>
<p class="sub">공지는 관측이 아닙니다. 점검이 일찍 끝났는지, 공지 없이 멈췄는지는 <a href="s/index.html">서비스별 상태</a>와 <code>ai/targets/{id}.json</code>으로 확인하세요. 자세한 설명: 저장소의 docs/maintenance.md</p>
<footer>pamon: 개발자와 사용자가 의존하는 서비스의 외부 관측 기록. 공식 발표가 아닙니다.</footer>
</main></body></html>{{end}}

{{define "maint"}}<h2>공지된 점검</h2>
{{if .}}<table><tr><th>시간 (KST)</th><th>내용</th></tr>
{{range .}}<tr><td>{{.When}}<br><span class="h {{.Tone}}">{{.StateText}}</span></td><td>{{.Note}}{{if .URL}} <a href="{{.URL}}" rel="nofollow">공지</a>{{end}}</td></tr>{{end}}</table>
{{else}}<p>pamon에 등록된 점검 공지가 없습니다.</p>{{end}}
<p class="sub"><a href="../maintenance.html">전체 점검 일정</a> · <a href="../maintenance.ics">캘린더 구독(iCal)</a> · <a href="../maintenance.json">JSON</a></p>{{end}}
`
