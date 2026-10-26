package web

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// One check, and what else happened at that moment, decides whose problem it was.
func TestTraceCheck(t *testing.T) {
	cfg, err := config.Parse([]byte(`
locations: [{id: us, name: 미국 서부}, {id: kr, name: 한국 서울}]
targets:
  - {id: a, provider: P, url: "https://a.example/x"}
  - {id: a2, provider: P, url: "https://a.example/y"}
  - {id: a3, provider: P, url: "https://a3.example/"}
  - {id: h1, provider: Q, url: "https://h1.example/"}
  - {id: h2, provider: Q, url: "https://h2.example/"}
  - {id: h3, provider: Q, url: "https://h3.example/"}
  - {id: h4, provider: Q, url: "https://h4.example/"}
`))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-10 * time.Minute)
	tlsStuck := probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: TLS 연결 중", LatencyMS: 5000,
		Phases: probe.Phases{DNSMs: 20, ConnectMs: 130, TLSMs: 4800}}
	okPeer := func(id string) probe.Result {
		return probe.Result{TargetID: id, Location: "us", At: at.Add(time.Minute), Status: probe.StatusOK, LatencyMS: 400,
			Phases: probe.Phases{DNSMs: 10, ConnectMs: 130, TLSMs: 140, WaitMs: 100}}
	}
	stuckPeer := func(id string) probe.Result {
		r := okPeer(id)
		r.Status, r.LatencyMS, r.Phases.TLSMs = probe.StatusSlow, 3000, 2500
		return r
	}
	for _, c := range []struct {
		name   string
		check  probe.Result
		others []probe.Result
		follow []probe.FollowUp
		kind   string
		says   []string
	}{
		{"many other servers stuck in TLS: our path", tlsStuck,
			[]probe.Result{stuckPeer("h1"), stuckPeer("h2"), stuckPeer("h3"), okPeer("h4"), okPeer("a3")}, nil,
			"monitor_network", []string{"감시 지점 쪽 네트워크", "다른 서버 5곳 중 3곳", "TLS 연결 단계에서 시간 초과"}},
		{"everyone else fine: that server's edge", tlsStuck,
			[]probe.Result{okPeer("h1"), okPeer("h2"), okPeer("h3"), okPeer("h4"), stuckPeer("a2")}, nil,
			"server_edge", []string{"방화벽·로드밸런서", "다른 서버 4곳 중 0곳"}},
		{"fine from Korea: the path from the US", tlsStuck,
			[]probe.Result{okPeer("h1"), {TargetID: "a", Location: "kr", At: at.Add(30 * time.Second), Status: probe.StatusOK}}, nil,
			"path", []string{"미국 서부에서 이 서버로 가는 경로", "한국 서울에서는 같은 시각 정상"}},
		{"the provider's other APIs slow too", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusSlow, LatencyMS: 4000, Phases: probe.Phases{ConnectMs: 100, TLSMs: 100, WaitMs: 3700}},
			[]probe.Result{stuckPeer("a2"), {TargetID: "a3", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: 서버 응답 대기 중 (연결은 됨)"}, okPeer("h1")}, nil,
			"provider", []string{"P 전체가", "다른 API 2건 중 2건"}},
		{"a server error", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, HTTPStatus: 503, Reason: "http 503", LatencyMS: 300, Phases: probe.Phases{WaitMs: 250}},
			nil, nil, "server", []string{"HTTP 503"}},
		{"a timeout the retry got through", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: 서버 응답 대기 중 (연결은 됨)", LatencyMS: 10000, Phases: probe.Phases{WaitMs: 9800}},
			nil, []probe.FollowUp{{At: at.Add(10 * time.Second), Answered: true, AfterMs: 140}}, "server", []string{"140ms 만에 응답", "서버가 처리하는 데"}},
		{"no such host: the domain's DNS", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "request failed: dial tcp: lookup a.example: no such host"},
			[]probe.Result{okPeer("h1"), okPeer("h2"), okPeer("h3")}, nil, "dns", []string{"DNS 응답", "DNS 조회 단계에서 실패했습니다"}},
		{"connection refused, everyone else fine", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "request failed: dial tcp 1.2.3.4:443: connect: connection refused"},
			[]probe.Result{okPeer("h1"), okPeer("h2"), okPeer("h3")}, nil, "server_edge", []string{"TCP 연결", "connection refused"}},
		{"the server hung up", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "request failed: Get \"https://a.example/x\": EOF", Phases: probe.Phases{ConnectMs: 100, TLSMs: 100, WaitMs: 30}},
			nil, nil, "server", []string{"연결을 끊었습니다"}},
		{"an old record: '연결 대기 중' with TCP measured at 5 s", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: 연결 대기 중", LatencyMS: 5000, Phases: probe.Phases{DNSMs: 1, ConnectMs: 5000}},
			[]probe.Result{okPeer("h1"), okPeer("h2"), okPeer("h3")}, nil, "server_edge", []string{"TCP 연결 단계에서 시간 초과", "잘못된 표기"}},
		{"a waiting-for-the-connection timeout", probe.Result{TargetID: "a", Location: "us", At: at, Status: probe.StatusDown, Reason: "timeout: 연결 대기 중", LatencyMS: 5000},
			[]probe.Result{okPeer("h1")}, nil, "monitor", []string{"연결을 시작하기도 전에", "근거가 없습니다"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			for _, r := range append([]probe.Result{c.check}, c.others...) {
				if err := st.SaveCheck(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			s := &Server{Cfg: cfg, Store: st}
			tr := s.traceCheck(context.Background(), &cfg.Targets[0], c.check, c.follow)
			if tr == nil {
				t.Fatal("no trace")
			}
			all := tr.Headline + " | " + strings.Join(tr.Evidence, " | ")
			if tr.Kind != c.kind {
				t.Errorf("kind %s, want %s: %s", tr.Kind, c.kind, all)
			}
			for _, w := range c.says {
				if !strings.Contains(all, w) {
					t.Errorf("lacks %q: %s", w, all)
				}
			}
		})
	}
	if (&Server{Cfg: cfg}).traceCheck(context.Background(), &cfg.Targets[0], okPeer("a"), nil) != nil {
		t.Error("an ok check needs no trace")
	}
}
