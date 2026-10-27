package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// A real outage gets a dated page worded the way people search for it afterwards; a
// two-check blip and a monitor-side key problem do not.
func TestIncidentPages(t *testing.T) {
	cfg, err := config.Parse([]byte(`
locations:
  - id: kr-seoul
    name: 한국 서울
  - id: gcp-us-west1
    name: 미국 서부
targets:
  - id: kakao-login
    name: 카카오 로그인
    provider: 카카오
    category: 로그인·인증
    url: https://kauth.kakao.com/.well-known/openid-configuration
  - id: kakao-map
    name: 카카오맵
    provider: 카카오
    category: 지도·위치
    url: https://dapi.kakao.com/
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
	start := now.Add(-3 * time.Hour).Truncate(time.Minute)
	outage := &incident.Incident{TargetID: "kakao-login", Started: start, Ended: start.Add(35 * time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "http 503: Service Unavailable", Flaps: 1}
	alongside := &incident.Incident{TargetID: "kakao-map", Started: start.Add(5 * time.Minute), Ended: start.Add(20 * time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "timeout: tcp"}
	blip := &incident.Incident{TargetID: "kakao-map", Started: now.Add(-time.Hour), Ended: now.Add(-50 * time.Minute), Severity: 1, Status: probe.StatusSlow, Reason: "slow: 3s"}
	key := &incident.Incident{TargetID: "kakao-map", Started: now.Add(-2 * time.Hour), Ended: now.Add(-time.Hour), Severity: 2, Status: probe.StatusClient, Reason: "http 401"}
	open := &incident.Incident{TargetID: "kakao-map", Started: now.Add(-20 * time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "timeout: dns"}
	for _, in := range []*incident.Incident{outage, alongside, blip, key, open} {
		if err := st.SaveIncident(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 7; i++ { // Seoul failed throughout, US West only once
		at := start.Add(time.Duration(i) * 5 * time.Minute)
		st.SaveCheck(ctx, probe.Result{TargetID: "kakao-login", Location: "kr-seoul", At: at, Status: probe.StatusDown, Reason: "http 503"})
		us := probe.StatusOK
		if i == 0 {
			us = probe.StatusDown
		}
		st.SaveCheck(ctx, probe.Result{TargetID: "kakao-login", Location: "gcp-us-west1", At: at, Status: us})
	}
	s := &Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now }, PublicURL: "https://pamon.test"}
	h := s.Handler()

	slug := IncidentSlug(outage)
	if want := start.In(config.KST).Format("2006-01-02") + "-kakao-login-1.html"; slug != want {
		t.Fatalf("slug %q, want %q", slug, want)
	}
	page := get(t, h, "/i/"+slug).Body.String()
	date := start.In(config.KST).Format("2006년 1월 2일")
	for _, want := range []string{
		"<title>" + date + " 카카오 로그인 장애 (", "<h1>" + date + " 카카오 로그인 장애</h1>", "35분 동안 카카오 로그인(카카오)이 외부에서 실패했습니다",
		"HTTP 503", "에 복구됐습니다", "1번 잠깐 회복했다가", "한국 서울</td><td class=\"n\">7 / 7", "미국 서부</td><td class=\"n\">1 / 7",
		"같은 시간에 함께 실패한 서비스", `href="` + IncidentSlug(alongside) + `">카카오맵`, `href="../s/kakao-login.html"`, "갱신 시각 <time"} {
		if !strings.Contains(page, want) {
			t.Errorf("incident page lacks %q", want)
		}
	}
	if strings.Contains(page, "http 401") {
		t.Error("a monitor-side key problem is not listed as failing alongside")
	}
	m := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(page)
	var ld map[string]any
	if m == nil || json.Unmarshal([]byte(m[1]), &ld) != nil || ld["@type"] != "Report" || ld["url"] != "https://pamon.test/i/"+slug ||
		ld["datePublished"] != start.UTC().Format(time.RFC3339) || ld["dateModified"] != start.Add(35*time.Minute).UTC().Format(time.RFC3339) {
		t.Errorf("JSON-LD: %v", ld)
	}

	if w := get(t, h, "/i/"+IncidentSlug(open)); w.Code != 200 || !strings.Contains(w.Body.String(), "진행 중인 장애") || !strings.Contains(w.Body.String(), "아직 복구되지 않았습니다") {
		t.Errorf("open outage page: %d", w.Code)
	}
	for _, in := range []*incident.Incident{blip, key} {
		if w := get(t, h, "/i/"+IncidentSlug(in)); w.Code != 404 {
			t.Errorf("%s %s got a page: %d", in.Status, in.Reason, w.Code)
		}
	}
	if w := get(t, h, "/i/2000-01-01-kakao-map-1.html"); w.Code != 301 || w.Header().Get("Location") != "/i/"+slug {
		t.Errorf("a wrong slug moves to the right one: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := get(t, h, "/i/x.html"); w.Code != 404 {
		t.Errorf("bad slug: %d", w.Code)
	}

	index := get(t, h, "/i/").Body.String()
	hrefs := regexp.MustCompile(`href="(\d{4}-\d{2}-\d{2}-[^"]+)"`).FindAllStringSubmatch(index, -1)
	if len(hrefs) != 3 || hrefs[0][1] != IncidentSlug(open) || !strings.Contains(index, "최근 서비스 장애 기록") {
		t.Errorf("index lists outages newest first, not blips or key problems: %v", hrefs)
	}
	// the answer page links its outage to the dated page
	if ans := get(t, h, "/s/kakao-login").Body.String(); !strings.Contains(ans, `href="../i/`+slug+`"`) || !strings.Contains(ans, `href="../i/">모든 서비스의 최근 장애 기록`) {
		t.Error("answer page does not link the incident page")
	}
}
