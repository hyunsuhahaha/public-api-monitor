package web

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// Answer pages (/s/ and /s/{id}): what someone who searched "is X down?" needs first.
// A one-line verdict, whether it is likely on their side, where a failure happened in
// plain words, and the evidence under it. Rendered on the server so search engines and
// assistants read it without running JavaScript.

// Verdict is the answer at the top of a page.
type Verdict struct {
	Tone     string `json:"tone"`     // ok, warn, bad, unknown
	Headline string `json:"headline"` // 정상입니다 / 장애가 있습니다 ...
	Detail   string `json:"detail"`   // one sentence: what we saw
	YourSide string `json:"yourSide"` // what that means for the reader
}

// Explain turns a check's status and reason into the reader's terms.
func Explain(status probe.Status, reason string, checked *time.Time, now time.Time, every time.Duration) Verdict {
	if checked == nil || now.Sub(*checked) > 2*every {
		return Verdict{Tone: "unknown", Headline: "최근 확인 기록이 없습니다",
			Detail:   "감시가 멈췄거나 이 서비스를 확인하지 못했습니다. 아래 기록은 오래된 것일 수 있습니다.",
			YourSide: "지금 상태를 이 페이지로 판단할 수 없습니다."}
	}
	switch status {
	case probe.StatusOK:
		return Verdict{Tone: "ok", Headline: "정상입니다", Detail: "외부에서 보낸 요청에 정상 응답을 받았습니다.",
			YourSide: "안 된다면 이용하는 쪽 문제일 가능성이 큽니다: 앱·브라우저를 다시 열거나, 다른 네트워크(와이파이 ↔ 모바일 데이터)로 해 보거나, 로그인·결제 수단·권한을 확인해 보세요. " + usOnlyNote}
	case probe.StatusSlow:
		return Verdict{Tone: "warn", Headline: "느립니다", Detail: "응답은 정상이지만 평소보다 오래 걸립니다. " + plainReason(reason),
			YourSide: "느리게 열리거나 시간 초과가 날 수 있습니다. 서비스 쪽 상황이라 기다리는 것 외에 할 일이 거의 없습니다."}
	case probe.StatusStale:
		return Verdict{Tone: "warn", Headline: "응답은 오지만 내용이 이상합니다", Detail: plainReason(reason),
			YourSide: "화면은 열려도 정보가 오래됐거나 틀릴 수 있습니다. 서비스 쪽 문제입니다."}
	case probe.StatusError, probe.StatusDown:
		return Verdict{Tone: "bad", Headline: "장애가 있습니다", Detail: plainReason(reason),
			YourSide: "당신만의 문제가 아닙니다. 외부에서도 같은 실패가 보입니다. 서비스가 복구될 때까지 기다리거나 공식 공지를 확인하세요."}
	case probe.StatusClient:
		return Verdict{Tone: "unknown", Headline: "판단할 수 없습니다",
			Detail:   "감시 쪽 설정(인증키·호출 한도) 문제로 이 서비스의 상태를 확인하지 못했습니다.",
			YourSide: "서비스 장애로 보지 않습니다. 가용률 계산에서도 뺍니다."}
	case probe.StatusMaint:
		return Verdict{Tone: "warn", Headline: "공지된 점검 중입니다", Detail: "제공 기관이 알린 점검 시간입니다.",
			YourSide: "점검이 끝나면 다시 됩니다."}
	}
	return Verdict{Tone: "unknown", Headline: "상태를 알 수 없습니다", Detail: plainReason(reason)}
}

// usOnlyNote ends the ok verdict while the US is the only location with records; the
// answer page replaces it once more locations check the service.
const usOnlyNote = "감시 지점은 미국 서부라 국내 통신사 한 곳만의 문제는 여기에 보이지 않을 수 있습니다."

var httpCode = regexp.MustCompile(`http (\d{3})`)

// plainReason says where it broke, from the probe's reason text.
func plainReason(reason string) string {
	r := strings.ToLower(reason)
	switch {
	case strings.HasPrefix(r, "timeout: dns"):
		return "주소(DNS)를 찾는 단계에서 시간이 초과됐습니다. 도메인·DNS 쪽 문제일 가능성이 있습니다."
	case strings.HasPrefix(r, "timeout: tcp"):
		return "서버에 연결하는 단계에서 시간이 초과됐습니다. 서버가 꺼졌거나 네트워크가 막혔을 수 있습니다."
	case strings.HasPrefix(r, "timeout: tls"):
		return "보안 연결(HTTPS)을 맺는 단계에서 멈췄습니다."
	case strings.HasPrefix(r, "timeout: 서버 응답 대기"), strings.HasPrefix(r, "timeout: 본문"):
		return "연결은 됐지만 서버가 제때 답하지 않았습니다. 서버 과부하나 내부 장애일 가능성이 큽니다."
	case strings.HasPrefix(r, "timeout"):
		return "제한 시간 안에 응답이 오지 않았습니다."
	case strings.Contains(r, "no such host"):
		return "도메인 주소(DNS)를 찾을 수 없습니다."
	case strings.Contains(r, "connection refused"):
		return "서버가 연결을 거부했습니다. 서버가 내려가 있을 수 있습니다."
	case strings.Contains(r, "certificate"), strings.Contains(r, "x509"), strings.Contains(r, "tls"):
		return "보안 인증서(HTTPS) 문제로 연결하지 못했습니다. 인증서 만료나 설정 오류일 수 있습니다."
	case strings.HasPrefix(r, "slow"):
		return "응답 시간: " + strings.TrimPrefix(reason, "slow: ") + "."
	case strings.Contains(r, "redirect to "):
		return "서비스가 요청을 다른 주소로 돌려보냈습니다. 점검 안내나 오류 페이지로 보내는 경우가 많습니다."
	case r == "empty body", r == "empty http response":
		return "서비스가 빈 응답을 보냈습니다."
	case strings.Contains(r, "answer lacks"):
		return "응답은 왔지만 정상 화면이 아닙니다. 점검 안내나 차단 페이지가 대신 나온 것으로 보입니다."
	case strings.Contains(r, "bad data"), strings.Contains(r, "bad value"):
		return "응답은 왔지만 데이터가 기대와 다릅니다. (" + reason + ")"
	case strings.Contains(r, "stale"), strings.Contains(r, "old"):
		return "응답은 왔지만 데이터 갱신이 멈췄습니다. (" + reason + ")"
	}
	if m := httpCode.FindStringSubmatch(r); m != nil {
		if m[1][0] == '5' {
			return fmt.Sprintf("서버가 오류(HTTP %s)를 돌려줬습니다. 서비스 쪽 장애입니다.", m[1])
		}
		return fmt.Sprintf("기대와 다른 응답(HTTP %s)을 받았습니다.", m[1])
	}
	if reason == "" {
		return ""
	}
	return "실패 사유: " + reason
}

type phaseRow struct {
	Name, Plain string
	Ms          int64
	Width       int
}

type answerPage struct {
	T          TargetView
	V          Verdict
	Checked    string
	Phases     []phaseRow
	PhaseNote  string
	Days       []dayCell
	Incidents  []incidentRow
	Uptime30   string
	Uptime90   string
	P95        string
	Base       string // canonical base URL ("" when unknown)
	Generated  string
	Every      string
	Categories []answerCategory
	Problems   []answerItem // failing or degraded now, worst first (index page)
	Official   *officialView
	Advice     Advice
	More       answerMore    // lead time, maintenance, feeds (answer_more.go)
	Locations  []locationRow // by location, when more than one checks the service
	LocNote    string        // how the verdict comes from them
	Where      string        // who checks: 미국 서부·한국 서울
}

// locationRow is one location's latest check of the service, in the reader's terms.
type locationRow struct {
	Name, Tone, Headline, Detail, Checked, Latency string
	Primary                                        bool
}

// Advice is what a developer whose service calls this one can do about its failures,
// by category: what keeps their own users working while it is down.
type Advice struct {
	Title  string   `json:"title"`
	Points []string `json:"points"`
}

// publicData: the data.go.kr and Seoul open-data categories.
var publicData = map[string]bool{"날씨": true, "날씨·재해": true, "교통": true, "환경": true, "생활": true, "의료": true,
	"관광": true, "부동산": true, "금융": true, "공공행정": true}

// AdviceFor gives the advice for a category; every category gets some.
func AdviceFor(category string) Advice {
	switch {
	case category == "은행·결제":
		return Advice{"결제가 실패하거나 응답이 없을 때", []string{
			"재시도는 간격을 늘려 가며(지수 백오프와 무작위 지연) 몇 번만 합니다. 즉시 반복하면 복구 중인 서버에 부담을 더합니다.",
			"결제 요청마다 멱등성 키(Idempotency-Key 같은 주문별 고유 값)를 붙여, 응답을 못 받은 요청을 다시 보내도 두 번 결제되지 않게 합니다.",
			"시간 초과는 실패가 아니라 결과를 모르는 상태입니다. 다시 결제하기 전에 결제 조회 API로 실제로 승인됐는지 먼저 확인합니다.",
			"장애 중에는 주문을 결제 대기로 남겨 두고, 복구 뒤 조회·대사로 마무리합니다. 사용자에게는 중복 결제가 없다는 점을 알립니다."}}
	case category == "로그인·인증":
		return Advice{"로그인 제공자가 안 될 때", []string{
			"이미 로그인한 사용자의 세션은 그대로 유지합니다. 제공자 장애 중에 토큰 재검증 실패를 이유로 모두 로그아웃시키지 않습니다.",
			"OpenID 설정과 서명 키(JWKS)는 캐시해 두고, 새로 받지 못하면 마지막으로 받은 값을 계속 씁니다.",
			"새 로그인만 막히는 상황이므로 다른 로그인 수단(이메일, 다른 소셜 로그인)을 안내합니다.",
			"콜백 대기에는 짧은 시간 제한을 두고, 실패하면 \"잠시 뒤 다시 시도\" 화면을 보여 줍니다."}}
	case category == "문자·알림" || category == "협업·메신저":
		return Advice{"메시지 발송이 실패할 때", []string{
			"보낼 메시지는 먼저 큐에 넣고 작업자가 꺼내 보내게 합니다. 제공자가 멈춰도 요청 처리는 멈추지 않고, 복구 뒤 쌓인 것을 보냅니다.",
			"재시도는 지수 백오프로 하고, 메시지마다 고유 ID를 두어 재시도로 같은 알림이 두 번 가지 않게 합니다.",
			"계속 실패하는 메시지는 별도 큐(dead letter)로 옮겨 나중에 확인합니다.",
			"인증번호처럼 급한 메시지는 다른 제공자나 다른 채널(문자 ↔ 이메일·앱 푸시)로 보낼 수 있게 해 둡니다."}}
	case category == "AI":
		return Advice{"AI API가 느리거나 실패할 때", []string{
			"다른 모델이나 다른 제공자를 예비로 정해 두고, 오류나 시간 초과가 이어지면 그쪽으로 요청을 돌립니다.",
			"요청마다 시간 제한을 두고, 429·5xx에는 지수 백오프로 몇 번만 재시도합니다.",
			"실패가 계속되면 서킷 브레이커로 잠시 호출을 멈춰 대기열이 쌓이지 않게 합니다.",
			"AI 기능이 없어도 핵심 기능은 동작하게 하고, 사용자에게는 \"AI 응답을 잠시 쓸 수 없습니다\"라고 알립니다."}}
	case category == "클라우드":
		return Advice{"클라우드 API가 실패할 때", []string{
			"SDK의 재시도(지수 백오프)를 켜 두고 요청마다 시간 제한을 둡니다.",
			"한 리전·가용 영역 장애에 대비해 중요한 데이터와 서비스는 다른 영역에도 둡니다.",
			"배포·확장처럼 제어 API가 필요한 작업은 장애 중에 미루고, 이미 실행 중인 서버는 건드리지 않습니다."}}
	case category == "패키지·빌드" || category == "소스·배포":
		return Advice{"빌드·배포가 이 서비스에 막힐 때", []string{
			"패키지와 이미지는 사내 미러나 캐시 프록시를 거쳐 받아, 원본 저장소가 멈춰도 이미 받은 버전으로 빌드합니다.",
			"버전과 다이제스트를 고정(lock 파일)해 같은 결과물을 다시 받을 수 있게 합니다.",
			"CI 작업은 네트워크 오류에 한해 간격을 두고 재시도하고, 급한 배포는 이미 만든 결과물로 합니다."}}
	case category == "가상자산·시세":
		return Advice{"시세 API가 멈출 때", []string{
			"마지막으로 받은 시세에 받은 시각을 함께 보여 주고, 오래된 값임을 표시합니다.",
			"오래된 시세로 자동 주문을 내지 않습니다. 기준 시간보다 오래되면 주문을 멈춥니다.",
			"실시간 연결(WebSocket)이 끊기면 간격을 늘려 가며 다시 연결합니다."}}
	case category == "지도·위치":
		return Advice{"지도·위치 API가 실패할 때", []string{
			"주소 → 좌표 변환 결과는 캐시해 두고 같은 주소는 다시 묻지 않습니다.",
			"실패하면 다른 지도 제공자나 직접 입력한 주소로 대신합니다.",
			"재시도는 지수 백오프로 하고, 호출 한도(429)를 넘기지 않게 합니다."}}
	case publicData[category]:
		return Advice{"공공 데이터 API가 멈출 때", []string{
			"마지막으로 받은 정상 데이터를 저장해 두고, 실패하면 그것을 받은 시각과 함께 보여 줍니다. 빈 화면보다 낫습니다.",
			"공공 API는 갱신 주기가 정해져 있으므로(예: 매시 정각 발표) 그보다 자주 묻지 말고 결과를 캐시합니다.",
			"오류 응답도 확인합니다. 공공 API는 실패를 HTTP 200 안의 오류 코드로 돌려주는 경우가 많습니다.",
			"재시도는 간격을 늘려 가며 하고, 일일 호출 한도를 재시도로 다 쓰지 않게 합니다."}}
	}
	return Advice{"이 서비스가 실패할 때", []string{
		"요청마다 시간 제한을 두고, 5xx·시간 초과에만 지수 백오프로 몇 번 재시도합니다. 4xx는 재시도해도 같은 결과입니다.",
		"실패가 이어지면 서킷 브레이커로 잠시 호출을 멈춰 우리 서비스까지 느려지지 않게 합니다.",
		"마지막으로 받은 정상 결과를 캐시해 두고, 장애 중에는 그것으로 대신합니다."}}
}

// officialView is the provider's announcement next to our observation.
type officialView struct {
	official.Status
	Tone, Compare, Fetched string
}

// compareOfficial says what the reader should make of the two together.
func compareOfficial(st official.Status, ours string) *officialView {
	v := &officialView{Status: st, Tone: "ok", Fetched: st.FetchedAt.In(config.KST).Format("15:04")}
	if !st.Operational() {
		v.Tone = "bad"
		if st.Indicator == "minor" {
			v.Tone = "warn"
		}
	}
	switch {
	case ours == "bad" && st.Operational():
		v.Compare = "공식 상태 페이지는 아직 정상이라고 합니다. 발표가 늦어지는 중이거나, 우리가 확인하는 기능만의 문제일 수 있습니다."
	case ours == "bad":
		v.Compare = "공식 발표도 문제를 인정했습니다."
	case ours == "ok" && !st.Operational():
		v.Compare = "공식 발표에 문제가 있습니다. 우리가 확인하는 기능은 정상이지만 다른 기능이 영향을 받을 수 있습니다."
	case ours == "ok":
		v.Compare = "공식 발표도 정상입니다."
	default:
		v.Compare = "공식 발표 내용입니다."
	}
	return v
}

type dayCell struct{ Date, Tone, Title string }

type incidentRow struct{ Start, Duration, Status, Reason string }

type answerCategory struct {
	Name     string
	Services []answerItem
}

type answerItem struct {
	ID, Name, Provider, Tone, Headline string
}

func kst(t time.Time) string { return t.In(config.KST).Format("2006-01-02 15:04") }

func pct(u float64) string {
	if u < 0 {
		return "기록 없음"
	}
	return fmt.Sprintf("%.2f%%", u*100)
}

func (s *Server) answerData(ctx context.Context, id string) (*answerPage, error) {
	v, _, err := s.view(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	p := &answerPage{Base: strings.TrimSuffix(s.PublicURL, "/"), Generated: kst(v.GeneratedAt), Every: koDuration(s.Cfg.Interval.Duration)}
	if id == "" {
		byCat := map[string][]answerItem{}
		for _, t := range v.Targets {
			x := Explain(t.Status, t.Reason, t.CheckedAt, now, s.Cfg.Interval.Duration)
			byCat[t.Category] = append(byCat[t.Category], answerItem{ID: t.ID, Name: t.Name, Provider: t.Provider, Tone: x.Tone, Headline: x.Headline})
		}
		for name, items := range byCat {
			p.Categories = append(p.Categories, answerCategory{Name: name, Services: items})
			for _, it := range items {
				if it.Tone == "bad" || it.Tone == "warn" {
					p.Problems = append(p.Problems, it)
				}
			}
		}
		// what most readers look for first; the public-data APIs follow alphabetically
		rank := func(c string) int {
			for i, first := range []string{"로그인·인증", "은행·결제", "정부 서비스", "협업·메신저", "문자·알림", "AI", "클라우드", "업무 SaaS", "소스·배포", "패키지·빌드", "보안·모니터링", "검색·지식", "지도·위치", "SNS", "게임·미디어", "가상자산·시세", "날씨·재해"} {
				if c == first {
					return i
				}
			}
			return 100
		}
		sort.Slice(p.Categories, func(i, j int) bool {
			a, b := p.Categories[i].Name, p.Categories[j].Name
			if rank(a) != rank(b) {
				return rank(a) < rank(b)
			}
			return a < b
		})
		sort.Slice(p.Problems, func(i, j int) bool { return p.Problems[i].Tone == "bad" && p.Problems[j].Tone != "bad" })
		return p, nil
	}
	found := false
	for _, t := range v.Targets {
		if t.ID == id {
			p.T, found = t, true
		}
	}
	if !found {
		return nil, nil
	}
	p.V = Explain(p.T.Status, p.T.Reason, p.T.CheckedAt, now, s.Cfg.Interval.Duration)
	p.Advice = AdviceFor(p.T.Category)
	s.locationRows(ctx, p, now)
	if st, ok := s.Official[p.T.Provider]; ok {
		p.Official = compareOfficial(st, p.V.Tone)
	}
	p.More = s.answerMore(ctx, p.T, now)
	if p.T.CheckedAt != nil {
		p.Checked = kst(*p.T.CheckedAt)
	}
	p.Uptime30, p.Uptime90 = pct(p.T.Uptime30d), pct(p.T.Uptime90d)
	if p.T.P95Ms > 0 {
		p.P95 = fmt.Sprintf("%dms", p.T.P95Ms)
	}
	if recent, err := s.Store.RecentChecks(ctx, id, 1); err == nil && len(recent) == 1 {
		ph := recent[0].Phases
		rows := []phaseRow{{"DNS 조회", "도메인 주소를 IP로 찾기", ph.DNSMs, 0}, {"TCP 연결", "서버에 접속", ph.ConnectMs, 0},
			{"TLS 연결", "보안 연결(HTTPS) 맺기", ph.TLSMs, 0}, {"응답 대기", "요청을 보내고 첫 응답까지 (서버 처리 시간)", ph.WaitMs, 0}}
		var max int64 = 1
		for _, r := range rows {
			if r.Ms > max {
				max = r.Ms
			}
		}
		for i := range rows {
			rows[i].Width = int(rows[i].Ms * 100 / max)
		}
		p.Phases = rows
		if ph.Reused {
			p.PhaseNote = "이전 연결을 다시 써서 DNS·TCP·TLS 단계가 없었습니다."
		}
	}
	p.Days = dayCells(p.T.Days)
	list, _ := s.Store.Incidents(ctx, id, now.Add(-30*24*time.Hour), 10)
	for _, in := range list {
		p.Incidents = append(p.Incidents, incidentRow{Start: kst(in.Started), Duration: incidentLength(in, now),
			Status: map[bool]string{true: "장애", false: "저하"}[in.Severity >= 2], Reason: plainReason(in.Reason)})
	}
	return p, nil
}

// locationRows: the service's latest check from each location that reports (a declared
// location that has reported anything in the last day and has nothing for this service
// shows as such, so a probe that stopped is visible), and where the verdict comes from.
func (s *Server) locationRows(ctx context.Context, p *answerPage, now time.Time) {
	t := s.target(p.T.ID)
	if t == nil {
		return
	}
	active := map[string]bool{}
	if all, err := s.Store.LatestByLocation(ctx, now.Add(-24*time.Hour)); err == nil {
		for _, rs := range all {
			for _, r := range rs {
				active[r.Location] = true
			}
		}
	}
	seen := map[string]LocationView{}
	var order []string
	for _, l := range p.T.Locations {
		seen[l.Location] = l
		order = append(order, l.Location)
	}
	for _, l := range s.Cfg.Locations {
		if _, ok := seen[l.ID]; !ok && active[l.ID] && t.CheckedFrom(l.ID) {
			order = append(order, l.ID)
		}
	}
	primary := s.Cfg.PrimaryLocation(t)
	var names []string
	for _, id := range order {
		row := locationRow{Name: s.Cfg.LocationName(id), Tone: "unknown", Headline: "기록 없음",
			Detail: "이 지점의 최근 확인 기록이 없습니다. 감시기가 멈췄을 수 있습니다.", Primary: id == primary}
		if l, ok := seen[id]; ok {
			at := l.CheckedAt
			x := Explain(l.Status, l.Reason, &at, now, t.Interval.Duration)
			row.Tone, row.Headline, row.Detail, row.Checked = x.Tone, x.Headline, x.Detail, kst(at)
			if l.LatencyMs > 0 {
				row.Latency = fmt.Sprintf("%dms", l.LatencyMs)
			}
		}
		names = append(names, row.Name)
		p.Locations = append(p.Locations, row)
	}
	p.Where = strings.Join(names, "·")
	if p.Where == "" {
		p.Where = s.Cfg.LocationName("gcp-us-west1")
	}
	if len(p.Locations) < 2 {
		p.Locations = nil
		return
	}
	p.V.YourSide = strings.Replace(p.V.YourSide, usOnlyNote, p.Where+"에서 확인합니다. 지점별 결과는 아래에 있습니다.", 1)
	if primary != "" {
		p.LocNote = s.Cfg.LocationName(primary) + "의 결과를 결론으로 씁니다. 이 서비스의 사용자가 있는 곳입니다. 다른 지점의 결과가 다르면 그 지역의 네트워크나 접속 제한 문제일 수 있습니다."
	} else {
		p.LocNote = "한 곳이라도 실패하면 장애로 봅니다(세 곳 이상이면 과반수). 한 지점에서만 실패하면 그 지역의 네트워크나 접속 제한 문제일 수 있습니다."
	}
}

// koDuration: 5분, 1시간 20분, 2일 3시간.
func koDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	days, h, m := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d일", days))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d시간", h))
	}
	if m > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d분", m))
	}
	if len(parts) == 0 {
		return "1분 미만"
	}
	return strings.Join(parts, " ")
}

func incidentLength(in *incident.Incident, now time.Time) string {
	end := in.Ended
	open := end.IsZero()
	if open {
		end = now
	}
	s := koDuration(end.Sub(in.Started))
	if open {
		return s + " (진행 중)"
	}
	return s
}

func (s *Server) answer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".html")
	if id == "index" {
		id = ""
	}
	p, err := s.answerData(r.Context(), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if p == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=60")
	name := "service"
	if id == "" {
		name = "index"
	}
	answerTmpl.ExecuteTemplate(w, name, p)
}

var answerTmpl = template.Must(template.New("").Parse(`
{{define "head"}}<!doctype html>
<html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="dark">
<style>
:root{--bg:#0b0f17;--panel:#121826;--line:#1f2a3a;--text:#d8dee9;--muted:#8a96a8;--ok:#34d399;--warn:#fbbf24;--bad:#f87171;--none:#334155;--accent:#38bdf8}
*{box-sizing:border-box}[hidden]{display:none!important}body{margin:0;background:var(--bg);color:var(--text);font:16px/1.65 system-ui,-apple-system,"Segoe UI","Malgun Gothic",sans-serif}
main{max-width:820px;margin:0 auto;padding:24px 16px 48px}a{color:var(--accent)}
nav{font-size:14px;color:var(--muted);margin-bottom:16px}nav a{color:var(--muted)}
h1{font-size:26px;margin:0 0 4px;line-height:1.3}h2{font-size:17px;margin:28px 0 10px}
.sub{color:var(--muted);font-size:14px}
.verdict{border:1px solid var(--line);border-left:6px solid var(--none);border-radius:10px;background:var(--panel);padding:18px 20px;margin:18px 0}
.verdict.ok{border-left-color:var(--ok)}.verdict.warn{border-left-color:var(--warn)}.verdict.bad{border-left-color:var(--bad)}
.verdict b{font-size:22px;display:block}.verdict p{margin:8px 0 0}
.ok .h{color:var(--ok)}.warn .h{color:var(--warn)}.bad .h{color:var(--bad)}.unknown .h{color:var(--muted)}
.you{background:#0e1420;border:1px solid var(--line);border-radius:10px;padding:14px 18px}
table{width:100%;border-collapse:collapse;font-size:14px}td,th{padding:8px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top}th{color:var(--muted);font-weight:500}
td.n{text-align:right;white-space:nowrap;font-variant-numeric:tabular-nums}
.bar{height:8px;background:var(--accent);border-radius:4px;min-width:2px}
.days{display:flex;gap:3px;margin-top:6px}.days span{flex:1;height:28px;border-radius:3px;background:var(--none)}
.days .ok{background:var(--ok)}.days .warn{background:var(--warn)}.days .bad{background:var(--bad)}
.facts{display:grid;grid-template-columns:repeat(3,1fr);gap:10px}.facts div{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:10px 12px}
.facts small{color:var(--muted);display:block}.facts b{font-size:18px}
.cat{margin-top:22px}.svc{display:flex;justify-content:space-between;gap:12px;padding:9px 0;border-bottom:1px solid var(--line)}
.svc span.h{white-space:nowrap}
footer{margin-top:36px;color:var(--muted);font-size:13px}
@media(max-width:560px){.facts{grid-template-columns:1fr 1fr}h1{font-size:22px}}
</style>{{end}}

{{define "service"}}{{template "head" .}}
<title>{{.T.Name}} 지금 되나요? 장애·접속 상태 | pamon</title>
<meta name="description" content="{{.T.Name}}({{.T.Provider}}) 지금 상태: {{.V.Headline}}. {{.V.Detail}} 5분마다 외부에서 직접 확인한 기록입니다.">
<link rel="alternate" type="application/atom+xml" title="{{.T.Name}} 장애·변경 알림" href="../feed/{{.T.ID}}.xml">
</head><body><main>
<nav><a href="index.html">서비스별 상태</a> › {{.T.Category}}</nav>
<h1>{{.T.Name}} 지금 되나요?</h1>
<div class="sub">{{.T.Provider}} · 외부 감시 기록 (공식 발표가 아닙니다)</div>

<section class="verdict {{.V.Tone}}">
  <b class="h">{{.V.Headline}}</b>
  <p>{{.V.Detail}}</p>
  <p class="sub">마지막 확인: {{if .Checked}}{{.Checked}} (KST){{else}}없음{{end}} · {{.Every}}마다 확인</p>
</section>

<section class="you"><b>나만 안 되나요?</b><p style="margin:6px 0 0">{{.V.YourSide}}</p></section>

<section class="you" id="report" data-target="{{.T.ID}}" hidden style="margin-top:10px;display:flex;gap:12px;align-items:center;flex-wrap:wrap">
  <button type="button" style="font:inherit;padding:8px 14px;border-radius:8px;border:1px solid var(--bad);background:#2a1215;color:var(--text);cursor:pointer">나도 안 돼요</button>
  <span class="count sub"></span>
</section>

{{if .Locations}}<h2>지점별 결과</h2>
<table><tr><th>지점</th><th>결과</th><th>확인 (KST)</th><th class="n">응답</th></tr>
{{range .Locations}}<tr><td><b>{{.Name}}</b>{{if .Primary}} <span class="sub">결론 기준</span>{{end}}</td><td class="{{.Tone}}"><b class="h">{{.Headline}}</b><br><span class="sub">{{.Detail}}</span></td><td>{{.Checked}}</td><td class="n">{{.Latency}}</td></tr>{{end}}</table>
<p class="sub">{{.LocNote}}</p>{{end}}

{{with .Official}}<h2>{{.Provider}} 공식 발표</h2>
<section class="verdict {{.Tone}}"><b class="h" style="font-size:17px">{{.Description}}</b>
<p>{{.Compare}}</p><p class="sub"><a href="{{.Page}}" rel="nofollow">{{.Page}}</a> · {{.Fetched}} (KST) 확인</p></section>{{end}}

{{if .Phases}}<h2>마지막 요청은 어디서 시간이 걸렸나</h2>
<table>{{range .Phases}}<tr><td style="width:34%"><b>{{.Name}}</b><br><span class="sub">{{.Plain}}</span></td><td><div class="bar" style="width:{{.Width}}%"></div></td><td class="n">{{.Ms}}ms</td></tr>{{end}}</table>
{{if .PhaseNote}}<p class="sub">{{.PhaseNote}}</p>{{end}}{{end}}

<h2>최근 30일</h2>
<div class="days">{{range .Days}}<span class="{{.Tone}}" title="{{.Title}}"></span>{{end}}</div>
<p class="sub">초록 정상 · 노랑 느림·이상 · 빨강 장애 · 회색 기록 없음</p>
<div class="facts">
  <div><small>30일 가용률</small><b>{{.Uptime30}}</b></div>
  <div><small>90일 가용률</small><b>{{.Uptime90}}</b></div>
  <div><small>응답 시간 p95</small><b>{{if .P95}}{{.P95}}{{else}}–{{end}}</b></div>
</div>

<h2>최근 장애 (30일)</h2>
{{if .Incidents}}<table><tr><th>시작 (KST)</th><th>길이</th><th>내용</th></tr>
{{range .Incidents}}<tr><td>{{.Start}}</td><td>{{.Duration}}</td><td><b>{{.Status}}</b> {{.Reason}}</td></tr>{{end}}</table>
{{else}}<p>지난 30일 동안 기록된 장애가 없습니다.</p>{{end}}
{{template "more" .More}}

<h2>이 API에 의존한다면: {{.Advice.Title}}</h2>
<ul>{{range .Advice.Points}}<li>{{.}}</li>{{end}}</ul>

<h2>무엇을 확인하나</h2>
<p>{{.Where}}의 감시기가 {{.Every}}마다 <b>{{.T.Name}}</b>에 직접 요청을 보내 응답 코드와 내용까지 확인합니다. 로그인·결제를 실제로 하지는 않으므로, 계정이나 결제 수단에 따른 문제는 보이지 않습니다.{{if .T.Docs}} 확인하는 곳: <a href="{{.T.Docs}}" rel="nofollow">{{.T.Docs}}</a>{{end}}</p>
<p><a href="../#t={{.T.ID}}">운영자용 상세 그래프</a> · <a href="../ai/targets/{{.T.ID}}.json">AI·개발자용 JSON</a></p>

<footer>pamon: 개발자와 사용자가 의존하는 서비스의 외부 관측 기록. 데이터 생성 {{.Generated}} (KST). 제보는 누가 보냈는지 알 수 없게 저장합니다.</footer>
</main><script src="../report.js"></script></body></html>{{end}}

{{define "index"}}{{template "head" .}}
<title>지금 안 되는 서비스가 있나요? 서비스별 장애·접속 상태 | pamon</title>
<meta name="description" content="카카오·네이버 로그인, 홈택스, 정부24, 은행, AWS·네이버클라우드, Slack·Discord, 공공 API가 지금 되는지 5분마다 외부에서 확인한 기록입니다.">
</head><body><main>
<h1>지금 안 되는 서비스가 있나요?</h1>
<p class="sub">{{.Every}}마다 외부에서 직접 확인한 결과입니다. 데이터 생성 {{.Generated}} (KST) · 공식 발표가 아닙니다.</p>
<section class="verdict {{if .Problems}}bad{{else}}ok{{end}}">
{{if .Problems}}<b class="h">지금 문제가 보이는 곳 {{len .Problems}}개</b>
{{range .Problems}}<div class="svc"><a href="{{.ID}}.html">{{.Name}} <span class="sub">{{.Provider}}</span></a><span class="h {{.Tone}}">{{.Headline}}</span></div>{{end}}
{{else}}<b class="h">지금 확인한 서비스는 모두 정상입니다</b><p>안 되는 게 있다면 아래에서 그 서비스를 눌러 자세한 기록을 보세요.</p>{{end}}
</section>
{{range .Categories}}<section class="cat"><h2>{{.Name}}</h2>
{{range .Services}}<div class="svc"><a href="{{.ID}}.html">{{.Name}} <span class="sub">{{.Provider}}</span></a><span class="h {{.Tone}}">{{.Headline}}<span class="sub" data-report="{{.ID}}" hidden></span></span></div>{{end}}
</section>{{end}}
<footer><a href="../">운영자 대시보드</a> · <a href="../status.html">공개 상태 요약</a> · <a href="../lead-time.html">공식 발표보다 얼마나 빨랐나</a> · <a href="../llms.txt">AI용 안내</a></footer>
</main><script src="../report.js"></script></body></html>{{end}}
`))
