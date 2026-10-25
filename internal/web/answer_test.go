package web

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func TestExplainSaysWhereItBrokeAndWhoseSide(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Minute)
	for _, c := range []struct {
		status probe.Status
		reason string
		tone   string
		detail string
		side   string
	}{
		{probe.StatusOK, "", "ok", "정상 응답", "이용하는 쪽"},
		{probe.StatusDown, "timeout: 서버 응답 대기 중 (연결은 됨)", "bad", "서버가 제때 답하지", "당신만의 문제가 아닙니다"},
		{probe.StatusDown, "timeout: DNS 조회 중", "bad", "DNS", "당신만의"},
		{probe.StatusDown, "http 503: Service Unavailable", "bad", "HTTP 503", "당신만의"},
		{probe.StatusError, "answer lacks \"홈택스\": an error or block page, not the service", "bad", "점검 안내나 차단", ""},
		{probe.StatusSlow, "slow: 4.2s", "warn", "4.2s", "느리게"},
		{probe.StatusClient, "http 401: key not registered", "unknown", "감시 쪽 설정", "장애로 보지 않습니다"},
	} {
		v := Explain(c.status, c.reason, &fresh, now, 5*time.Minute)
		if v.Tone != c.tone || !strings.Contains(v.Detail, c.detail) || !strings.Contains(v.YourSide, c.side) {
			t.Errorf("%s %q: %+v", c.status, c.reason, v)
		}
	}
	for _, c := range []struct {
		indicator, ours, want string
	}{
		{"none", "bad", "아직 정상이라고"}, {"major", "bad", "인정했습니다"}, {"minor", "ok", "다른 기능"}, {"none", "ok", "공식 발표도 정상"},
	} {
		if v := compareOfficial(official.Status{Indicator: c.indicator}, c.ours); !strings.Contains(v.Compare, c.want) {
			t.Errorf("%s vs ours %s: %q", c.indicator, c.ours, v.Compare)
		}
	}
	old := now.Add(-time.Hour)
	if v := Explain(probe.StatusOK, "", &old, now, 5*time.Minute); v.Tone != "unknown" {
		t.Errorf("an hour-old ok is not a current answer: %+v", v)
	}
	if v := Explain(probe.StatusOK, "", nil, now, 5*time.Minute); v.Tone != "unknown" {
		t.Errorf("never checked: %+v", v)
	}
}

// A failing service: the page says so in plain words, puts the provider's "operational"
// next to it, gives the advice for its category, and the index and AI JSON agree.
func TestAnswerPageForFailingTarget(t *testing.T) {
	cfg, err := config.Parse([]byte(`
targets:
  - id: gh
    name: GitHub API
    provider: GitHub
    category: 소스·배포
    url: https://api.github.com/
  - id: wx
    name: 날씨
    provider: 기상청
    category: 날씨
    url: https://x/
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
	for _, r := range []probe.Result{
		{TargetID: "gh", Location: "t", At: now.Add(-time.Minute), Status: probe.StatusDown, HTTPStatus: 503, Reason: "http 503: Service Unavailable"},
		{TargetID: "wx", Location: "t", At: now.Add(-time.Minute), Status: probe.StatusOK, HTTPStatus: 200},
	} {
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVerdict(ctx, store.Verdict{TargetID: r.TargetID, At: r.At, Status: r.Status, Reason: r.Reason, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now },
		Official: map[string]official.Status{"GitHub": {Provider: "GitHub", Page: "https://www.githubstatus.com", Indicator: "none", Description: "All Systems Operational", FetchedAt: now}}}
	h := s.Handler()

	page := get(t, h, "/s/gh").Body.String()
	for _, want := range []string{"GitHub API 지금 되나요?", "장애가 있습니다", "HTTP 503", "당신만의 문제가 아닙니다",
		"GitHub 공식 발표", "All Systems Operational", "아직 정상이라고", "https://www.githubstatus.com",
		"이 API에 의존한다면", "미러", "ai/targets/gh.json"} {
		if !strings.Contains(page, want) {
			t.Errorf("answer page lacks %q", want)
		}
	}
	if wx := get(t, h, "/s/wx").Body.String(); !strings.Contains(wx, "정상입니다") || strings.Contains(wx, "공식 발표</h2>") || !strings.Contains(wx, "마지막으로 받은 정상 데이터") ||
		strings.Contains(wx, "지점별 결과") || !strings.Contains(wx, usOnlyNote) {
		t.Error("an ok public-data service without an official page: verdict, no announcement box, cache advice")
	}
	index := get(t, h, "/s/").Body.String()
	if !strings.Contains(index, "지금 문제가 보이는 곳 1개") || !strings.Contains(index, `href="gh.html"`) {
		t.Error("the index leads with the failing service")
	}
	if w := get(t, h, "/s/nope"); w.Code != 404 {
		t.Errorf("unknown service: %d", w.Code)
	}

	var v DiagnosticView
	if err := json.Unmarshal(get(t, h, "/ai/targets/gh.json").Body.Bytes(), &v); err != nil || len(v.Targets) != 1 {
		t.Fatal(err, v)
	}
	d := v.Targets[0]
	if d.Assessment != "failure_observed" || d.Verdict.Tone != "bad" || d.Verdict.Headline != "장애가 있습니다" {
		t.Errorf("verdict: %s %+v", d.Assessment, d.Verdict)
	}
	if d.Official == nil || !d.Official.Operational || d.Official.Indicator != "none" || !strings.Contains(d.Official.Comparison, "아직 정상이라고") {
		t.Errorf("official: %+v", d.Official)
	}
	if d.Advice.Title == "" || len(d.Advice.Points) == 0 {
		t.Errorf("advice: %+v", d.Advice)
	}
	var wx DiagnosticView
	if err := json.Unmarshal(get(t, h, "/ai/targets/wx.json").Body.Bytes(), &wx); err != nil || wx.Targets[0].Official != nil {
		t.Error("no official page: the field is left out, not guessed")
	}
}

// The advice the brief asks for, per kind of dependency, and some for every category.
func TestAdviceForEveryCategory(t *testing.T) {
	for cat, want := range map[string]string{"은행·결제": "멱등성 키", "로그인·인증": "세션은 그대로 유지", "날씨": "마지막으로 받은 정상 데이터",
		"문자·알림": "큐에 넣고", "협업·메신저": "큐에 넣고", "AI": "다른 모델", "처음 보는 분류": "시간 제한"} {
		a := AdviceFor(cat)
		if !strings.Contains(strings.Join(a.Points, " "), want) {
			t.Errorf("%s: %+v lacks %q", cat, a, want)
		}
	}
	raw, err := os.ReadFile("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	generic := AdviceFor("").Title
	for _, tg := range cfg.Targets {
		if a := AdviceFor(tg.Category); a.Title == generic && tg.Category != "정부 서비스" && tg.Category != "업무 SaaS" &&
			tg.Category != "보안·모니터링" && tg.Category != "검색·지식" && tg.Category != "SNS" && tg.Category != "게임·미디어" {
			t.Errorf("%s: category %q has no advice of its own", tg.ID, tg.Category)
		}
	}
}

// Korea judges a Korean service: its row is marked as the basis, the US failure is
// shown next to it, and a location that reported recently but not for this service
// shows "기록 없음" instead of disappearing.
func TestAnswerPageByLocation(t *testing.T) {
	cfg, err := config.Parse([]byte(`
locations:
  - {id: us, name: 미국 서부}
  - {id: kr, name: 한국 서울, primary_for: [카카오]}
  - {id: jp, name: 일본 도쿄}
targets:
  - {id: login, name: 카카오 로그인, provider: 카카오, category: 로그인·인증, url: "https://x/"}
  - {id: other, name: 다른 것, provider: X, url: "https://y/"}
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
	at := now.Add(-time.Minute)
	for _, r := range []probe.Result{
		{TargetID: "login", Location: "kr", At: at, Status: probe.StatusOK, LatencyMS: 30},
		{TargetID: "login", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: TCP 연결 중"},
		{TargetID: "other", Location: "jp", At: now.Add(-3 * time.Hour), Status: probe.StatusOK},
	} {
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveVerdict(ctx, store.Verdict{TargetID: "login", At: at, Status: probe.StatusOK, Reason: "failing only from us", Voters: 2}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now }}).Handler()
	page := get(t, h, "/s/login").Body.String()
	for _, want := range []string{"정상입니다", "지점별 결과", "한국 서울", "결론 기준", "미국 서부", "서버에 연결하는 단계",
		"일본 도쿄", "기록 없음", "30ms", "한국 서울의 결과를 결론으로 씁니다", "에서 확인합니다. 지점별 결과는 아래에"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(page, usOnlyNote) {
		t.Error("the US-only note stays once more locations check the service")
	}
	var v DiagnosticView
	if err := json.Unmarshal(get(t, h, "/ai/targets/login.json").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	d := v.Targets[0]
	if d.PrimaryLocation != "kr" || len(d.Locations) != 2 || d.Locations[0].Name == "" {
		t.Errorf("json: %s %+v", d.PrimaryLocation, d.Locations)
	}
}
