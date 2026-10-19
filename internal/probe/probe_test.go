package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

func target(t *testing.T, yaml string) *config.Target {
	t.Helper()
	c, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	return &c.Targets[0]
}

func TestCheckAgainstServer(t *testing.T) {
	// (the timed-out handler is still running when the next request arrives: guard the key)
	var mu sync.Mutex
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotKey = r.URL.Query().Get("serviceKey")
		mu.Unlock()
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
		case "/sleep":
			time.Sleep(300 * time.Millisecond)
			fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"}}}`)
		case "/fresh":
			now := time.Now().In(config.KST)
			fmt.Fprintf(w, `{"resultCode":"00","items":[{"dataTime":"%s"},{"dataTime":"%s"}]}`,
				now.Add(-5*time.Hour).Format("2006-01-02 15:04"), now.Add(-30*time.Minute).Format("2006-01-02 15:04"))
		case "/old":
			fmt.Fprint(w, `{"resultCode":"00","items":[{"dataTime":"2020-01-01 00:00"}]}`)
		}
	}))
	defer srv.Close()
	t.Setenv("DATA_GO_KR_KEY", "se+cret")
	p := New("test")
	ctx := context.Background()

	r := p.Check(ctx, target(t, "targets:\n  - id: a\n    url: "+srv.URL+"/ok?serviceKey={{.Key}}\n"))
	mu.Lock()
	key := gotKey
	mu.Unlock()
	if r.Status != StatusOK || key != "se+cret" || r.HTTPStatus != 200 {
		t.Fatalf("ok: %+v key=%q", r, key)
	}
	r = p.Check(ctx, target(t, "targets:\n  - id: a\n    timeout: 100ms\n    url: "+srv.URL+"/sleep?serviceKey={{.Key}}\n"))
	if r.Status != StatusDown || r.Reason != "timeout: 서버 응답 대기 중 (연결은 됨)" {
		t.Fatalf("timeout: %+v", r)
	}
	fresh := "\n    freshness:\n      pattern: '\"dataTime\":\"([^\"]+)\"'\n      layout: '2006-01-02 15:04'\n      max_age: 2h\n"
	r = p.Check(ctx, target(t, "targets:\n  - id: a\n    url: "+srv.URL+"/fresh?serviceKey={{.Key}}"+fresh))
	if r.Status != StatusOK {
		t.Fatalf("fresh (newest of several timestamps counts): %+v", r)
	}
	r = p.Check(ctx, target(t, "targets:\n  - id: a\n    url: "+srv.URL+"/old?serviceKey={{.Key}}"+fresh))
	if r.Status != StatusStale {
		t.Fatalf("stale: %+v", r)
	}
}

func TestNoKeyIsOurSide(t *testing.T) {
	t.Setenv("PAMON_NO_SUCH_KEY", "")
	r := New("test").Check(context.Background(), target(t, "targets:\n  - id: a\n    key_env: PAMON_NO_SUCH_KEY\n    url: http://127.0.0.1:1/\n"))
	if r.Status != StatusClient {
		t.Fatalf("%+v", r)
	}
}

func TestKeyNeverInErrors(t *testing.T) {
	t.Setenv("DATA_GO_KR_KEY", "topsecretkey")
	// nothing listens on port 1: the error message contains the URL
	r := New("test").Check(context.Background(), target(t, "targets:\n  - id: a\n    url: http://127.0.0.1:1/x?serviceKey={{.Key}}\n"))
	if r.Status != StatusDown || strings.Contains(r.Reason, "topsecretkey") {
		t.Fatalf("%+v", r)
	}
}

func TestBadValue(t *testing.T) {
	cfg, err := config.Parse([]byte(`
targets:
  - id: a
    url: http://x/?k={{.Key}}
    expect:
      - name: T1H
        pattern: '"category":"T1H"[^}]*"obsrValue":"(-?[\d.]+)"'
        min: -40
        max: 45
`))
	if err != nil {
		t.Fatal(err)
	}
	rules := cfg.Targets[0].Expect
	ok := `{"item":[{"category":"T1H","obsrValue":"12.5"},{"category":"REH","obsrValue":"88"}]}`
	if bad, why := badValue([]byte(ok), rules); bad {
		t.Fatal(why)
	}
	hot := `{"item":[{"category":"T1H","obsrValue":"300"}]}`
	if bad, why := badValue([]byte(hot), rules); !bad || why != "bad value: T1H=300 (expected -40~45)" {
		t.Fatalf("%v %q", bad, why)
	}
	none := `{"item":[{"category":"REH","obsrValue":"88"}]}`
	if bad, why := badValue([]byte(none), rules); !bad || why != "bad value: T1H missing (0 of 1)" {
		t.Fatalf("%v %q", bad, why)
	}
}
