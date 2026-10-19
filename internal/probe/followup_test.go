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

// A server that answers in 300 ms times out a 100 ms check; the follow-up waits and sees
// it answer (late, not hung), once.
func TestFollowUpLateAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
	}))
	defer srv.Close()
	t.Setenv("DATA_GO_KR_KEY", "k")
	cfg, err := config.Parse([]byte("timeout: 100ms\ntargets:\n  - id: a\n    url: " + srv.URL + "/?k={{.Key}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p := New("test")
	done := make(chan FollowUp, 2)
	p.FollowUps = NewFollowUps()
	p.FollowUps.Wait = 2 * time.Second
	p.FollowUps.Done = func(_ string, f FollowUp) { done <- f }
	if r := p.Check(context.Background(), &cfg.Targets[0]); r.Status != StatusDown {
		t.Fatalf("check: %+v", r)
	}
	select {
	case f := <-done:
		if !f.Answered || f.HTTP != 200 || f.AfterMs < 250 || f.Timeout != 100 {
			t.Fatalf("follow-up: %+v", f)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no follow-up")
	}
	p.Check(context.Background(), &cfg.Targets[0]) // within Every: no second follow-up
	select {
	case f := <-done:
		t.Fatalf("second follow-up: %+v", f)
	case <-time.After(600 * time.Millisecond):
	}
	if f, ok := p.FollowUps.Last("a"); !ok || !f.Answered {
		t.Fatalf("Last: %+v %v", f, ok)
	}
}

func TestFollowUpRememberKeepsTheLimit(t *testing.T) {
	f := NewFollowUps()
	now := time.Now()
	f.Remember("a", FollowUp{At: now.Add(-10 * time.Minute)})
	started := false
	f.Done = func(string, FollowUp) { started = true }
	f.start(http.DefaultClient, "a", "http://127.0.0.1:1/", http.Header{}, time.Second, time.Now)
	time.Sleep(50 * time.Millisecond)
	if started {
		t.Fatal("a follow-up 10 minutes after the remembered one must wait")
	}
}
