package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// The server's think time lands in WaitMs; a fresh connection has a connect time.
func TestPhases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(60 * time.Millisecond)
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DATA_GO_KR_KEY", "k")
	cfg, err := config.Parse([]byte("targets:\n  - id: a\n    url: " + srv.URL + "/?k={{.Key}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("test")
	r := p.Check(context.Background(), &cfg.Targets[0])
	if r.Phases.WaitMs < 60 || r.Phases.WaitMs > r.LatencyMS || r.Phases.Reused {
		t.Fatalf("first call: %+v (latency %d)", r.Phases, r.LatencyMS)
	}
	if r2 := p.Check(context.Background(), &cfg.Targets[0]); !r2.Phases.Reused || r2.Phases.ConnectMs != 0 {
		t.Fatalf("second call reuses the connection: %+v", r2.Phases)
	}
}
