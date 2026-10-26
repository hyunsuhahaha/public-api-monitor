package web

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// Trace: why one check failed or was slow, from what else was seen at that moment. A
// check alone says where it stopped (DNS, TCP, TLS, the server); the other services
// checked from the same place in the same minutes say whose problem that was: many other
// servers stuck in the same network step is the monitor's own path, the same provider's
// other APIs failing is the provider, everything else fine is this one server.

// CheckTrace is one check's evidence and the conclusion drawn from it.
type CheckTrace struct {
	Kind     string   `json:"kind"`            // monitor_network, path, server_edge, server, provider, dns, monitor, content, unknown
	Phase    string   `json:"phase,omitempty"` // dns, connect, tls, wait, body, pool: where it stopped or spent its time
	Headline string   `json:"headline"`        // the conclusion, one sentence
	Evidence []string `json:"evidence"`        // what it rests on, in the reader's words
	Peers    struct {
		Hosts       int `json:"hosts"`       // other servers checked from here in the window
		SameStep    int `json:"sameStep"`    // of them, stuck or slow in the same step
		Provider    int `json:"provider"`    // the same provider's other APIs checked
		ProviderBad int `json:"providerBad"` // of them, failing or slow
	} `json:"peers"`
	Elsewhere []traceLoc `json:"elsewhere,omitempty"` // this target from other locations, same window
	FollowUp  string     `json:"followUp,omitempty"`  // the re-sent request after a timeout
}

type traceLoc struct {
	Location string `json:"location"`
	Name     string `json:"name"`
	Status   string `json:"status"`
}

// the window around a check that counts as "the same moment": one interval each side
// covers every target once (their slots spread over the interval)
const traceWindow = 3 * time.Minute

var phaseWords = map[string]string{"dns": "DNS 조회", "connect": "TCP 연결", "tls": "TLS 연결", "wait": "서버 대기", "body": "본문 받기",
	"pool": "연결 대기", "send": "요청 보내기"}

// stepOf: the step a check stopped in (timeouts name it) or spent most of its time in.
func stepOf(c probe.Result) string {
	if p := probe.StalledPhase(c.Reason); p != "" {
		return p
	}
	if p := failedStep(c); p != "" {
		return p
	}
	ph := c.Phases
	rest := c.LatencyMS - ph.DNSMs - ph.ConnectMs - ph.TLSMs - ph.WaitMs
	best, step := int64(-1), ""
	for _, x := range []struct {
		k string
		v int64
	}{{"dns", ph.DNSMs}, {"connect", ph.ConnectMs}, {"tls", ph.TLSMs}, {"wait", ph.WaitMs}, {"body", rest}} {
		if x.v > best {
			best, step = x.v, x.k
		}
	}
	return step
}

// failedStep: the step a request that failed outright (no timeout, no answer) broke in,
// from the error text.
func failedStep(c probe.Result) string {
	if !strings.HasPrefix(c.Reason, "request failed") {
		return ""
	}
	r := strings.ToLower(c.Reason)
	switch {
	case strings.Contains(r, "no such host"), strings.Contains(r, "server misbehaving"), strings.Contains(r, "lookup "):
		return "dns"
	case strings.Contains(r, "x509"), strings.Contains(r, "certificate"), strings.Contains(r, "tls"), strings.Contains(r, "handshake"):
		return "tls"
	case c.Phases.WaitMs > 0, strings.Contains(r, "eof"):
		return "wait" // connected and sent, then the server hung up
	}
	return "connect"
}

func networkStep(s string) bool { return s == "dns" || s == "connect" || s == "tls" || s == "pool" }

// troubled: the check failed or was slow, in a way that says something about the path.
func troubled(c probe.Result) bool { return c.Status.Counts() && c.Status.Severity() >= 1 }

// stuckIn: a peer check was stuck in or slowed by this step: it timed out or failed
// there, or the step alone took over a second (a handshake across the Pacific is ~0.3 s).
func stuckIn(c probe.Result, step string) bool {
	if probe.StalledPhase(c.Reason) == step || failedStep(c) == step {
		return true
	}
	switch step {
	case "dns":
		return c.Phases.DNSMs >= 1000
	case "connect":
		return c.Phases.ConnectMs >= 1000
	case "tls":
		return c.Phases.TLSMs >= 1000
	case "wait":
		return c.Phases.WaitMs >= 1000
	}
	return false
}

func targetHost(t *config.Target) string {
	authority := strings.SplitN(t.URL, "://", 2)
	if len(authority) != 2 {
		return ""
	}
	hostPart := strings.SplitN(strings.SplitN(strings.SplitN(authority[1], "/", 2)[0], "?", 2)[0], "#", 2)[0]
	u, err := url.Parse("https://" + hostPart)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// aroundCache keeps the checks around a moment for a minute: an outage seen everywhere
// makes every target's recent list ask about the same few minutes (the export asks for
// each target and range).
type aroundCache struct {
	mu      sync.Mutex
	entries map[string]aroundEntry
	traces  map[string]traceEntry // one check's trace, asked for by every range's series

	once sync.Once
	info map[string]peerInfo // target id -> provider and host
}

type traceEntry struct {
	trace   *CheckTrace
	expires time.Time
}

type peerInfo struct{ provider, host string }

func (s *Server) peerInfo(id string) (peerInfo, bool) {
	s.around.once.Do(func() {
		s.around.info = map[string]peerInfo{}
		for i := range s.Cfg.Targets {
			t := &s.Cfg.Targets[i]
			s.around.info[t.ID] = peerInfo{t.Provider, targetHost(t)}
		}
	})
	p, ok := s.around.info[id]
	return p, ok
}

type aroundEntry struct {
	checks  []probe.Result
	expires time.Time
}

func (s *Server) checksAround(ctx context.Context, at time.Time) []probe.Result {
	// one key per minute: a check at 12:03:40 and one at 12:03:10 share it
	from := at.Truncate(time.Minute).Add(-traceWindow)
	to := at.Truncate(time.Minute).Add(traceWindow + time.Minute)
	key := from.Format(time.RFC3339)
	s.around.mu.Lock()
	if s.around.entries == nil {
		s.around.entries = map[string]aroundEntry{}
	}
	e, ok := s.around.entries[key]
	s.around.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.checks
	}
	checks, err := s.Store.ChecksAround(ctx, from, to)
	if err != nil {
		return nil
	}
	s.around.mu.Lock()
	for k, old := range s.around.entries {
		if time.Now().After(old.expires) {
			delete(s.around.entries, k)
		}
	}
	s.around.entries[key] = aroundEntry{checks, time.Now().Add(time.Minute)}
	s.around.mu.Unlock()
	return checks
}

// traceCheck concludes on one check of target t (kept for a minute: the dashboard's
// every range, and the export, ask for the same recent checks). ok checks get no trace.
func (s *Server) traceCheck(ctx context.Context, t *config.Target, c probe.Result, followUps []probe.FollowUp) *CheckTrace {
	if c.Status == probe.StatusOK || c.Status == probe.StatusMaint {
		return nil
	}
	key := c.TargetID + "|" + c.Location + "|" + c.At.Format(time.RFC3339Nano)
	s.around.mu.Lock()
	e, ok := s.around.traces[key]
	s.around.mu.Unlock()
	if ok && time.Now().Before(e.expires) {
		return e.trace
	}
	tr := s.buildTrace(ctx, t, c, followUps)
	s.around.mu.Lock()
	if s.around.traces == nil || len(s.around.traces) > 20000 {
		s.around.traces = map[string]traceEntry{}
	}
	s.around.traces[key] = traceEntry{tr, time.Now().Add(time.Minute)}
	s.around.mu.Unlock()
	return tr
}

func (s *Server) buildTrace(ctx context.Context, t *config.Target, c probe.Result, followUps []probe.FollowUp) *CheckTrace {
	tr := &CheckTrace{Kind: "unknown", Evidence: []string{}}
	if c.Status == probe.StatusClient {
		tr.Kind, tr.Headline = "monitor", "감시 쪽 설정(인증키·호출 한도) 문제입니다. 서비스 장애로 보지 않습니다."
		tr.Evidence = append(tr.Evidence, "서비스가 우리 요청을 거절한 이유가 키·승인·한도입니다: "+c.Reason)
		return tr
	}
	step := stepOf(c)
	tr.Phase = step
	self, _ := s.peerInfo(t.ID)
	host := self.host
	latest := map[string]probe.Result{} // this target from the other locations, nearest in time
	hostStuck := map[string]bool{}      // other servers: one vote per host (data.go.kr's fifty APIs are one server)
	providerBad := map[string]bool{}    // the same provider's other APIs: one vote per API
	network := networkStep(step)
	for _, p := range s.checksAround(ctx, c.At) {
		if p.At.Sub(c.At).Abs() > traceWindow {
			continue
		}
		if p.TargetID == c.TargetID {
			if l, ok := latest[p.Location]; p.Location != c.Location && (!ok || p.At.Sub(c.At).Abs() < l.At.Sub(c.At).Abs()) {
				latest[p.Location] = p
			}
			continue
		}
		if p.Location != c.Location {
			continue
		}
		pt, ok := s.peerInfo(p.TargetID)
		if !ok {
			continue
		}
		if pt.provider == t.Provider {
			providerBad[p.TargetID] = providerBad[p.TargetID] || troubled(p)
		}
		if pt.host == "" || pt.host == host {
			continue
		}
		hostStuck[pt.host] = hostStuck[pt.host] || (network && stuckIn(p, step)) || (step == "pool" && stuckIn(p, "connect"))
	}
	tr.Peers.Provider = len(providerBad)
	for _, bad := range providerBad {
		if bad {
			tr.Peers.ProviderBad++
		}
	}
	tr.Peers.Hosts = len(hostStuck)
	for _, stuck := range hostStuck {
		if stuck {
			tr.Peers.SameStep++
		}
	}
	// the same target from other locations at that moment
	locs := make([]string, 0, len(latest))
	for l := range latest {
		locs = append(locs, l)
	}
	sort.Strings(locs)
	for _, l := range locs {
		tr.Elsewhere = append(tr.Elsewhere, traceLoc{l, s.Cfg.LocationName(l), string(latest[l].Status)})
	}
	for _, f := range followUps {
		if d := f.At.Sub(c.At); d >= 0 && d < 2*time.Minute {
			if f.Answered {
				tr.FollowUp = fmt.Sprintf("시간 초과 직후 다시 보낸 요청은 %s 만에 응답했습니다.", koMs(f.AfterMs))
			} else {
				tr.FollowUp = "시간 초과 직후 다시 보낸 요청도 응답이 없었습니다."
			}
			break
		}
	}
	otherOK, otherBad := 0, 0
	for _, e := range tr.Elsewhere {
		switch probe.Status(e.Status) {
		case probe.StatusOK:
			otherOK++
		case probe.StatusDown, probe.StatusError:
			otherBad++
		}
	}

	what := phaseWords[step]
	where := s.Cfg.LocationName(c.Location)
	switch {
	case c.HTTPStatus >= 500:
		tr.Kind, tr.Headline = "server", fmt.Sprintf("서버가 오류(HTTP %d)를 돌려줬습니다. 연결은 됐고, 서비스 안쪽의 장애입니다.", c.HTTPStatus)
		tr.Evidence = append(tr.Evidence, "DNS·TCP·TLS를 모두 마치고 서버가 직접 오류로 답했습니다.")
	case strings.Contains(c.Reason, "answer lacks"):
		tr.Kind, tr.Headline = "content", "정상 화면 대신 다른 페이지(점검 안내·차단·오류 페이지)가 왔습니다."
		tr.Evidence = append(tr.Evidence, "응답은 왔지만, 그 서비스가 정상일 때 늘 들어 있는 문구가 없습니다.")
	case c.Status == probe.StatusError && c.Code != "":
		tr.Kind, tr.Headline = "server", fmt.Sprintf("서비스가 오류 코드(%s)로 답했습니다. 연결은 정상이고, 서비스 쪽 처리 오류입니다.", c.Code)
	case c.Status == probe.StatusStale:
		tr.Kind, tr.Headline = "server", "응답은 정상이지만 데이터 갱신이 멈췄거나 값이 이상합니다. 제공 기관의 데이터 쪽 문제입니다."
	case failedStep(c) == "wait":
		tr.Kind, tr.Headline = "server", "연결과 요청까지는 됐지만 서버가 답하지 않고 연결을 끊었습니다."
	case networkStep(step) && tr.Peers.Hosts >= 3 && tr.Peers.SameStep*3 >= tr.Peers.Hosts:
		tr.Kind = "monitor_network"
		tr.Headline = fmt.Sprintf("%s 감시 지점 쪽 네트워크 문제로 보입니다. 같은 시각 다른 서버들도 %s에서 막혔습니다.", where, what)
	case step == "dns":
		tr.Kind = "dns"
		tr.Headline = "이 도메인의 DNS 응답이 늦거나 없었습니다. 같은 시각 다른 서버들의 DNS는 정상이었습니다."
	case networkStep(step) && otherOK > 0:
		tr.Kind = "path"
		tr.Headline = fmt.Sprintf("%s에서 이 서버로 가는 경로 문제로 보입니다. 다른 지점에서는 같은 시각에 정상이었습니다.", where)
	case networkStep(step):
		tr.Kind = "server_edge"
		tr.Headline = fmt.Sprintf("이 서버(또는 앞단의 방화벽·로드밸런서)가 %s을 늦게 받았습니다. 같은 시각 다른 서버들은 정상이었습니다.", what)
	case step == "wait" && tr.Peers.ProviderBad >= 2 && tr.Peers.ProviderBad*2 >= tr.Peers.Provider:
		tr.Kind = "provider"
		tr.Headline = fmt.Sprintf("%s 전체가 느리거나 실패했습니다. 연결은 정상이고, 같은 제공자의 다른 API도 같은 시각에 이상이었습니다.", t.Provider)
	case step == "wait":
		tr.Kind = "server"
		tr.Headline = "연결은 정상이고, 서버가 처리하는 데 오래 걸렸습니다."
	case step == "body":
		tr.Kind = "server"
		tr.Headline = "서버는 답을 시작했지만 본문을 다 보내는 데 오래 걸렸습니다. 응답이 크거나 전송 구간이 느렸습니다."
	}
	if tr.Headline == "" {
		tr.Headline = "원인을 좁힐 근거가 부족합니다."
	}
	// the evidence, in the order a reader checks it
	if step != "" {
		ev := fmt.Sprintf("이 요청은 %s 단계에서 ", what)
		if probe.StalledPhase(c.Reason) == step {
			ev += "시간 초과로 멈췄습니다."
		} else if failedStep(c) == step {
			ev += "실패했습니다: " + c.Reason
		} else {
			ev += "가장 오래 걸렸습니다."
		}
		if step == "pool" {
			ev = "이 요청은 자기 연결을 맺기도 전에, 같은 서버로 다른 요청이 맺고 있던 연결을 기다리다 시간 초과됐습니다. 그 연결이 막혀 있었다는 뜻입니다."
		}
		tr.Evidence = append(tr.Evidence, ev)
	}
	if tr.Peers.Hosts > 0 && networkStep(step) {
		tr.Evidence = append(tr.Evidence, fmt.Sprintf("같은 시각(±3분) %s에서 확인한 다른 서버 %d곳 중 %d곳이 같은 단계에서 막히거나 1초 넘게 걸렸습니다.", where, tr.Peers.Hosts, tr.Peers.SameStep))
	}
	if tr.Peers.Provider > 0 {
		tr.Evidence = append(tr.Evidence, fmt.Sprintf("같은 제공자(%s)의 다른 API %d건 중 %d건이 같은 시각에 실패하거나 느렸습니다.", t.Provider, tr.Peers.Provider, tr.Peers.ProviderBad))
	}
	for _, e := range tr.Elsewhere {
		tr.Evidence = append(tr.Evidence, fmt.Sprintf("%s에서는 같은 시각 %s.", e.Name, map[string]string{"ok": "정상", "slow": "느림", "down": "장애", "error": "오류", "stale": "데이터 이상", "client": "확인 불가"}[e.Status]))
	}
	if otherBad > 0 && otherOK == 0 && tr.Kind != "monitor_network" {
		tr.Evidence = append(tr.Evidence, "다른 지점에서도 실패해, 한 지역만의 문제는 아닙니다.")
	}
	if tr.FollowUp != "" {
		tr.Evidence = append(tr.Evidence, tr.FollowUp)
	}
	return tr
}

func koMs(v int64) string {
	if v < 1000 {
		return fmt.Sprintf("%dms", v)
	}
	return fmt.Sprintf("%.1f초", float64(v)/1000)
}
