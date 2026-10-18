package notify

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
)

type sink struct {
	mu   sync.Mutex
	msgs []string
}

func (s *sink) send(_ context.Context, text string) {
	s.mu.Lock()
	s.msgs = append(s.msgs, text)
	s.mu.Unlock()
}

func (s *sink) got() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.msgs...)
}

func TestGrouperOneAgencyOnePage(t *testing.T) {
	var s sink
	g := &Grouper{Wait: 50 * time.Millisecond, Send: s.send}
	air := func(id, name string) *config.Target {
		return &config.Target{ID: id, Name: name, Provider: "한국환경공단"}
	}
	bus := &config.Target{ID: "bus", Name: "버스", Provider: "국토교통부"}
	ctx := context.Background()
	now := time.Now()
	g.Notify(ctx, incident.Opened, &incident.Incident{ID: 1, Started: now, Severity: 2, Reason: "timeout"}, air("a", "예보"))
	g.Notify(ctx, incident.Opened, &incident.Incident{ID: 2, Started: now, Severity: 2, Reason: "timeout"}, air("b", "측정소"))
	g.Notify(ctx, incident.Opened, &incident.Incident{ID: 3, Started: now, Severity: 2, Reason: "http 503"}, bus)
	g.Notify(ctx, incident.Updated, &incident.Incident{ID: 4, LastReason: "x"}, bus) // not held
	if m := s.got(); len(m) != 1 || !strings.Contains(m[0], "진행 중") {
		t.Fatalf("only the update goes out at once: %q", m)
	}
	time.Sleep(200 * time.Millisecond)
	m := s.got()
	if len(m) != 3 {
		t.Fatalf("want update + one grouped + one single, got %q", m)
	}
	var grouped, single string
	for _, x := range m[1:] {
		if strings.Contains(x, "기관 장애") {
			grouped = x
		} else {
			single = x
		}
	}
	if !strings.Contains(grouped, "한국환경공단 API 2개") || !strings.Contains(grouped, "예보") || !strings.Contains(grouped, "측정소") {
		t.Fatalf("grouped: %q", grouped)
	}
	if !strings.Contains(single, "버스") || strings.Contains(single, "기관") {
		t.Fatalf("a lone incident reads as usual: %q", single)
	}
}
