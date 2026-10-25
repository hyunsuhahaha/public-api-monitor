package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/leadtime"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// a GitHub API down for an hour; the official page said "major" 20 minutes after pamon
// confirmed it. A second outage the page never mentioned.
func leadSetup(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte(`
targets:
  - id: gh
    name: GitHub API
    provider: GitHub
    category: 소스·배포
    url: http://x
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
	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Minute)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	for m := -120; m <= 47*60; m += 5 {
		ind := "none"
		if m >= 25 && m <= 70 {
			ind = "major"
		}
		if err := st.RecordOfficial(ctx, "GitHub", ind, map[string]string{"none": "All Systems Operational", "major": "Partial outage"}[ind], at(m), 15*time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range [][2]int{{0, 60}, {600, 640}} {
		in := &incident.Incident{TargetID: "gh", Started: at(x[0]), Ended: at(x[1]), Severity: 2, Status: probe.StatusDown, Reason: "http 503"}
		if err := st.SaveIncident(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	st.SaveVerdict(ctx, store.Verdict{TargetID: "gh", At: time.Now(), Status: probe.StatusOK, Voters: 1})
	return &Server{Cfg: cfg, Store: st, LastCheck: time.Now}
}

func TestLeadTimePages(t *testing.T) {
	h := leadSetup(t).Handler()
	var rep leadtime.Report
	if err := json.Unmarshal(get(t, h, "/api/v1/lead-time").Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Total.Announced != 1 || rep.Total.Never != 1 || rep.Total.PamonFirst != 1 || rep.Total.MedianMinutes != 20 {
		t.Fatalf("total: %+v", rep.Total)
	}
	page := get(t, h, "/lead-time.html").Body.String()
	for _, want := range []string{"비교한 장애", "2건", "중앙값 pamon이 20분 먼저", "pamon이 20분 먼저 확인", "공식 발표 없음", "Partial outage", `href="s/gh.html"`} {
		if !strings.Contains(page, want) {
			t.Errorf("lead-time page lacks %q", want)
		}
	}
	month := time.Now().Add(-48 * time.Hour).In(config.KST).Format("2006-01")
	if rp := get(t, h, "/report/"+month).Body.String(); !strings.Contains(rp, "공식 상태 페이지와 비교") || !strings.Contains(rp, "20분 먼저") {
		t.Errorf("monthly report lacks the lead time: %s", rp)
	}
	ans := get(t, h, "/s/gh").Body.String()
	for _, want := range []string{"공식 발표보다 얼마나 빨랐나", "pamon이 20분 먼저 확인", `href="../lead-time.html"`} {
		if !strings.Contains(ans, want) {
			t.Errorf("answer page lacks %q", want)
		}
	}
}
