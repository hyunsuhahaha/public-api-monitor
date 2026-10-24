package web

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func setup(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	cfg, err := config.Parse([]byte(`
targets:
  - id: a
    name: API A
    category: 날씨
    url: http://x
  - id: b
    name: API B
    url: http://y
`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	// a: 3 ok, 1 down, 1 client (left out) -> 3/4 today; b: never checked
	for i, s := range []probe.Status{probe.StatusOK, probe.StatusOK, probe.StatusDown, probe.StatusClient, probe.StatusOK} {
		r := probe.Result{TargetID: "a", Location: "t", At: now.Add(time.Duration(i-5) * time.Minute), Status: s, LatencyMS: int64(100 * (i + 1)), HTTPStatus: 200}
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVerdict(ctx, store.Verdict{TargetID: "a", At: r.At, Status: s, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	in := &incident.Incident{TargetID: "a", Started: now.Add(-3 * time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "timeout", LastReason: "timeout"}
	if err := st.SaveIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Ended = now.Add(-time.Minute)
	if err := st.SaveIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now },
		IngestToken: "tok", AdminToken: "admin",
		SlowLimit: func(id string, ceil time.Duration) (time.Duration, time.Duration, bool) {
			return min(450*time.Millisecond, ceil), 50 * time.Millisecond, id == "a"
		},
		Ingest: func(ctx context.Context, rs []probe.Result) error {
			for _, r := range rs {
				if r.TargetID != "a" && r.TargetID != "b" {
					return errors.New("unknown target")
				}
				if err := st.SaveCheck(ctx, r); err != nil {
					return err
				}
			}
			return nil
		}}
	return st, s.Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestStatusAPI(t *testing.T) {
	_, h := setup(t)
	w := get(t, h, "/api/v1/status")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v StatusView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Targets) != 2 {
		t.Fatalf("targets: %d", len(v.Targets))
	}
	a, b := v.Targets[0], v.Targets[1]
	if a.Status != probe.StatusOK || a.Uptime30d != 0.75 || a.Incident != nil {
		t.Fatalf("a: %+v", a)
	}
	if len(a.Locations) != 1 || a.Locations[0].Location != "t" {
		t.Fatalf("locations: %+v", a.Locations)
	}
	if a.P50Ms == 0 || a.P95Ms < a.P50Ms {
		t.Fatalf("latency percentiles: %+v", a)
	}
	if b.Status != "unknown" || b.Uptime30d != -1 {
		t.Fatalf("b: %+v", b)
	}
}

func TestIncidentsAPI(t *testing.T) {
	_, h := setup(t)
	w := get(t, h, "/api/v1/incidents?target=a")
	var list []incident.Incident
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Open() || list[0].Reason != "timeout" {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	w = get(t, h, "/api/v1/incidents?target=b")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list should be [], got %s", w.Body.String())
	}
}

func TestBadgePageHealth(t *testing.T) {
	_, h := setup(t)
	if w := get(t, h, "/badge/a.svg"); w.Code != 200 || !strings.Contains(w.Body.String(), "75.00%") {
		t.Fatalf("badge: %d %s", w.Code, w.Body.String())
	}
	if w := get(t, h, "/badge/nope.svg"); w.Code != 404 {
		t.Fatalf("unknown badge: %d", w.Code)
	}
	if w := get(t, h, "/"); w.Code != 200 || !strings.Contains(w.Body.String(), "개발자 API 상태판") {
		t.Fatalf("page: %d", w.Code)
	}
	if w := get(t, h, "/app.js"); w.Code != 200 {
		t.Fatalf("app.js: %d", w.Code)
	}
	if w := get(t, h, "/readyz"); w.Code != 200 {
		t.Fatalf("readyz: %d %s", w.Code, w.Body.String())
	}
	if w := get(t, h, "/metrics"); w.Code != 200 {
		t.Fatalf("metrics: %d", w.Code)
	}
}

func post(h http.Handler, token, body string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/ingest", bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(w, req)
	return w.Code
}

func TestIngest(t *testing.T) {
	_, h := setup(t)
	at := time.Now().UTC().Format(time.RFC3339)
	good := `[{"target":"b","location":"home","at":"` + at + `","status":"down","latencyMs":10000,"reason":"timeout"}]`
	if c := post(h, "", good); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := post(h, "wrong", good); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
	if c := post(h, "tok", `[{"target":"b","location":"home","at":"`+at+`","status":"hacked"}]`); c != 400 {
		t.Fatalf("bad status: %d", c)
	}
	if c := post(h, "tok", `[{"target":"zzz","location":"home","at":"`+at+`","status":"ok"}]`); c != 400 {
		t.Fatalf("unknown target: %d", c)
	}
	if c := post(h, "tok", good); c != 204 {
		t.Fatalf("good: %d", c)
	}
	w := get(t, h, "/api/v1/status")
	var v StatusView
	json.Unmarshal(w.Body.Bytes(), &v)
	if l := v.Targets[1].Locations; len(l) != 1 || l[0].Location != "home" || l[0].Status != probe.StatusDown {
		t.Fatalf("ingested result not shown: %+v", l)
	}
}

func TestReportAndFeed(t *testing.T) {
	_, h := setup(t)
	month := time.Now().In(config.KST).Format("2006-01")
	if w := get(t, h, "/report/"+month); w.Code != 200 || !strings.Contains(w.Body.String(), "공공 API 가용성 리포트") || !strings.Contains(w.Body.String(), "API A") {
		t.Fatalf("report page: %d", w.Code)
	}
	if w := get(t, h, "/report/bad"); w.Code != 400 {
		t.Fatalf("bad month: %d", w.Code)
	}
	if w := get(t, h, "/report"); w.Code != 302 || !strings.HasSuffix(w.Header().Get("Location"), month) {
		t.Fatalf("redirect: %d %s", w.Code, w.Header().Get("Location"))
	}
	w := get(t, h, "/api/v1/report/"+month)
	var rep struct {
		Rows []struct{ ID string } `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || len(rep.Rows) != 2 {
		t.Fatalf("report json: %v %s", err, w.Body.String())
	}
	w = get(t, h, "/feed.xml")
	var feed struct {
		Entries []struct {
			Title string `xml:"title"`
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &feed); err != nil || len(feed.Entries) != 1 || !strings.Contains(feed.Entries[0].Title, "[장애] API A (복구") {
		t.Fatalf("feed: %v %s", err, w.Body.String())
	}
}

func TestSeriesSlowLines(t *testing.T) {
	_, h := setup(t)
	var v struct{ SlowAfterMs, SlowLimitMs, UsualMs int64 }
	for _, rg := range []string{"1h", "24h", "30d"} { // raw and rollup paths
		v.SlowLimitMs = 0
		if err := json.Unmarshal(get(t, h, "/api/v1/series?target=a&range="+rg).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if v.SlowAfterMs != 3000 || v.SlowLimitMs != 450 || v.UsualMs != 50 {
			t.Fatalf("a %s: %+v", rg, v)
		}
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/series?target=b&range=1h").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.SlowLimitMs != 0 {
		t.Fatalf("b has not learned a limit yet: %+v", v)
	}
}

// a: 1 outage in 4 counted verdicts within the hour, SLO 99%: 25% bad / 1% allowed = 25x
func TestOverviewBurnRates(t *testing.T) {
	_, h := setup(t)
	var v struct {
		Targets []struct {
			ID             string
			Burn1h, Burn6h float64
		}
	}
	for _, rg := range []string{"1h", "24h"} { // raw and rollup paths
		if err := json.Unmarshal(get(t, h, "/api/v1/overview?range="+rg).Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		for _, x := range v.Targets {
			want := map[string]float64{"a": 25, "b": -1}[x.ID]
			if math.Abs(x.Burn1h-want) > 1e-9 || math.Abs(x.Burn6h-want) > 1e-9 {
				t.Fatalf("%s %s: burn %v/%v, want %v", rg, x.ID, x.Burn1h, x.Burn6h, want)
			}
		}
	}
}

func TestOverviewCoverage(t *testing.T) {
	st, h := setup(t)
	// Missing data must not turn into 100% coverage.
	for _, rg := range []string{"1h", "24h"} {
		var empty struct{ Targets []overviewTarget }
		if err := json.Unmarshal(get(t, h, "/api/v1/overview?range="+rg).Body.Bytes(), &empty); err != nil {
			t.Fatal(err)
		}
		if len(empty.Targets) != 2 || empty.Targets[1].Coverage != -1 {
			t.Fatalf("%s missing data: %+v", rg, empty)
		}
	}
	// Rebuild the handler so the prior responses' cache cannot hide new verdicts.
	cfg, err := config.Parse([]byte("targets:\n  - {id: a, url: http://x}\n  - {id: b, url: http://y}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h = (&Server{Cfg: cfg, Store: st}).Handler()
	now := time.Now()
	// b: only client results; maintenance is excluded from both ratios.
	for i, status := range []probe.Status{probe.StatusClient, probe.StatusMaint} {
		if err := st.SaveVerdict(context.Background(), store.Verdict{TargetID: "b", At: now.Add(time.Duration(i-2) * time.Minute), Status: status, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, rg := range []string{"1h", "24h", "30d"} {
		var v struct {
			Targets []struct {
				ID                     string
				Availability, Coverage float64
				ClientChecks           int
			}
			Fleet struct {
				Coverage     float64
				ClientChecks int
			}
		}
		w := get(t, h, "/api/v1/overview?range="+rg)
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		for _, row := range v.Targets {
			wantCoverage, wantAvailability := 0.8, 0.75
			if row.ID == "b" {
				wantCoverage, wantAvailability = 0, -1
			}
			if row.Coverage != wantCoverage || row.Availability != wantAvailability || row.ClientChecks != 1 {
				t.Fatalf("%s %+v: coverage must expose excluded client results", rg, row)
			}
		}
		if math.Abs(v.Fleet.Coverage-4.0/6.0) > 1e-9 || v.Fleet.ClientChecks != 2 {
			t.Fatalf("%s fleet: %+v", rg, v.Fleet)
		}
	}
}

func TestPublicStatusPage(t *testing.T) {
	_, h := setup(t)
	w := get(t, h, "/status")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "개발자 API 상태") || !strings.Contains(w.Body.String(), "status.js") {
		t.Fatalf("%d %.200s", w.Code, w.Body.String())
	}
	if w := get(t, h, "/status.js"); w.Code != 200 {
		t.Fatalf("status.js %d", w.Code)
	}
}

func TestVersion(t *testing.T) {
	h := (&Server{Cfg: &config.Config{}, Version: "abc1234", StartedAt: time.Now().Add(-time.Hour)}).Handler()
	var v struct {
		Version   string
		UptimeSec int64
	}
	if err := json.Unmarshal(get(t, h, "/version").Body.Bytes(), &v); err != nil || v.Version != "abc1234" || v.UptimeSec < 3600 {
		t.Fatalf("%+v %v", v, err)
	}
}

// shift=1d answers the same window a day earlier, with timestamps a day back, so the
// page can lay yesterday over today bucket for bucket.
func TestSeriesShift(t *testing.T) {
	_, h := setup(t)
	var now, prev struct{ T []int64 }
	json.Unmarshal(get(t, h, "/api/v1/series?target=a&range=6h").Body.Bytes(), &now)
	json.Unmarshal(get(t, h, "/api/v1/series?target=a&range=6h&shift=1d").Body.Bytes(), &prev)
	if len(now.T) == 0 || len(now.T) != len(prev.T) {
		t.Fatalf("lengths %d %d", len(now.T), len(prev.T))
	}
	for i := range now.T {
		if now.T[i]-prev.T[i] != 86400 {
			t.Fatalf("bucket %d: %d vs %d", i, now.T[i], prev.T[i])
		}
	}
}

func TestIncidentNotes(t *testing.T) {
	_, h := setup(t)
	post := func(auth, body string) int {
		req := httptest.NewRequest("POST", "/api/v1/incidents/1/notes", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := post("", `{"text":"x"}`); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := post("wrong", `{"text":"x"}`); c != 401 {
		t.Fatalf("wrong token: %d", c)
	}
	if c := post("admin", `{"text":"  "}`); c != 400 {
		t.Fatalf("empty text: %d", c)
	}
	if c := post("admin", "{\"author\":\"pamon \xbf\xee\xbf\xb5\",\"text\":\"x\"}"); c != 400 {
		t.Fatalf("invalid UTF-8 (CP949 bytes): %d", c)
	}
	if c := post("admin", `{"author":"홍길동","text":"데이터포털 공지: 서버 점검"}`); c != 200 {
		t.Fatalf("note: %d", c)
	}
	var d struct {
		Notes   []struct{ Author, Text string }
		CanNote bool
	}
	json.Unmarshal(get(t, h, "/api/v1/incidents/1").Body.Bytes(), &d)
	if !d.CanNote || len(d.Notes) != 1 || d.Notes[0].Author != "홍길동" || d.Notes[0].Text != "데이터포털 공지: 서버 점검" {
		t.Fatalf("%+v", d)
	}
}

func TestIncidentAckInList(t *testing.T) {
	_, h := setup(t)
	req := httptest.NewRequest("POST", "/api/v1/incidents/1/notes", strings.NewReader(`{"author":"당번","kind":"ack"}`))
	req.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("ack: %d %s", w.Code, w.Body.String())
	}
	var list []struct {
		ID     int64
		Target string
		AckBy  string
		Notes  int
	}
	if err := json.Unmarshal(get(t, h, "/api/v1/incidents").Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("%v %+v", err, list)
	}
	if list[0].Target != "a" || list[0].AckBy != "당번" || list[0].Notes != 0 {
		t.Fatalf("an ack names who took it and is not a note: %+v", list[0])
	}
}

func TestSilences(t *testing.T) {
	_, h := setup(t)
	do := func(method, path, auth, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	if w := do("POST", "/api/v1/silences", "", `{"target":"a","minutes":60}`); w.Code != 401 {
		t.Fatalf("no token: %d", w.Code)
	}
	if w := do("POST", "/api/v1/silences", "admin", `{"target":"nope","minutes":60}`); w.Code != 400 {
		t.Fatalf("unknown target: %d", w.Code)
	}
	if w := do("POST", "/api/v1/silences", "admin", `{"target":"a","minutes":1}`); w.Code != 400 {
		t.Fatalf("too short: %d", w.Code)
	}
	w := do("POST", "/api/v1/silences", "admin", `{"target":"a","minutes":60,"author":"당번","reason":"기관 공지된 장애"}`)
	var x struct{ ID int64 }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &x) != nil || x.ID == 0 {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	var ov struct {
		Silences []struct{ Target, Reason string }
	}
	json.Unmarshal(get(t, h, "/api/v1/overview?range=1h").Body.Bytes(), &ov)
	if len(ov.Silences) != 1 || ov.Silences[0].Target != "a" || ov.Silences[0].Reason != "기관 공지된 장애" {
		t.Fatalf("overview: %+v", ov)
	}
	if w := do("DELETE", fmt.Sprintf("/api/v1/silences/%d", x.ID), "admin", ""); w.Code != 204 {
		t.Fatalf("end: %d", w.Code)
	}
	json.Unmarshal(get(t, h, "/api/v1/overview?range=1h").Body.Bytes(), &ov)
	if len(ov.Silences) != 0 {
		t.Fatalf("ended silence still listed: %+v", ov.Silences)
	}
}
