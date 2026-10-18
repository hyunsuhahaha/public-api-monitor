package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func setup(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	cfg, err := config.Parse([]byte(`
targets:
  - id: a
    name: API A
    category: 날씨
    url: http://x
  - id: b
    name: API B
    url: http://y
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
	// a: 3 ok, 1 down, 1 client (left out) -> 3/4 today; b: never checked
	for i, s := range []probe.Status{probe.StatusOK, probe.StatusOK, probe.StatusDown, probe.StatusClient, probe.StatusOK} {
		r := probe.Result{TargetID: "a", Location: "t", At: now.Add(time.Duration(i-5) * time.Minute), Status: s, LatencyMS: int64(100 * (i + 1)), HTTPStatus: 200}
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVerdict(ctx, store.Verdict{TargetID: "a", At: r.At, Status: s, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	in := &incident.Incident{TargetID: "a", Started: now.Add(-3 * time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "timeout", LastReason: "timeout"}
	if err := st.SaveIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Ended = now.Add(-time.Minute)
	if err := st.SaveIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	s := &Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now },
		IngestToken: "tok",
		Ingest: func(ctx context.Context, rs []probe.Result) error {
			for _, r := range rs {
				if r.TargetID != "a" && r.TargetID != "b" {
					return errors.New("unknown target")
				}
				if err := st.SaveCheck(ctx, r); err != nil {
					return err
				}
			}
			return nil
		}}
	return st, s.Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestStatusAPI(t *testing.T) {
	_, h := setup(t)
	w := get(t, h, "/api/v1/status")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var v StatusView
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Targets) != 2 {
		t.Fatalf("targets: %d", len(v.Targets))
	}
	a, b := v.Targets[0], v.Targets[1]
	if a.Status != probe.StatusOK || a.Uptime30d != 0.75 || a.Incident != nil {
		t.Fatalf("a: %+v", a)
	}
	if len(a.Locations) != 1 || a.Locations[0].Location != "t" {
		t.Fatalf("locations: %+v", a.Locations)
	}
	if a.P50Ms == 0 || a.P95Ms < a.P50Ms {
		t.Fatalf("latency percentiles: %+v", a)
	}
	if b.Status != "unknown" || b.Uptime30d != -1 {
		t.Fatalf("b: %+v", b)
	}
}

func TestIncidentsAPI(t *testing.T) {
	_, h := setup(t)
	w := get(t, h, "/api/v1/incidents?target=a")
	var list []incident.Incident
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 || list[0].Open() || list[0].Reason != "timeout" {
		t.Fatalf("%v %s", err, w.Body.String())
	}
	w = get(t, h, "/api/v1/incidents?target=b")
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("empty list should be [], got %s", w.Body.String())
	}
}

func TestBadgePageHealth(t *testing.T) {
	_, h := setup(t)
	if w := get(t, h, "/badge/a.svg"); w.Code != 200 || !strings.Contains(w.Body.String(), "75.00%") {
		t.Fatalf("badge: %d %s", w.Code, w.Body.String())
	}
	if w := get(t, h, "/badge/nope.svg"); w.Code != 404 {
		t.Fatalf("unknown badge: %d", w.Code)
	}
	if w := get(t, h, "/"); w.Code != 200 || !strings.Contains(w.Body.String(), "공공 API 상태판") {
		t.Fatalf("page: %d", w.Code)
	}
	if w := get(t, h, "/app.js"); w.Code != 200 {
		t.Fatalf("app.js: %d", w.Code)
	}
	if w := get(t, h, "/readyz"); w.Code != 200 {
		t.Fatalf("readyz: %d %s", w.Code, w.Body.String())
	}
	if w := get(t, h, "/metrics"); w.Code != 200 {
		t.Fatalf("metrics: %d", w.Code)
	}
}

func post(h http.Handler, token, body string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/ingest", bytes.NewBufferString(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	h.ServeHTTP(w, req)
	return w.Code
}

func TestIngest(t *testing.T) {
	_, h := setup(t)
	at := time.Now().UTC().Format(time.RFC3339)
	good := `[{"target":"b","location":"home","at":"` + at + `","status":"down","latencyMs":10000,"reason":"timeout"}]`
	if c := post(h, "", good); c != 401 {
		t.Fatalf("no token: %d", c)
	}
	if c := post(h, "wrong", good); c != 401 {
		t.Fatalf("bad token: %d", c)
	}
	if c := post(h, "tok", `[{"target":"b","location":"home","at":"`+at+`","status":"hacked"}]`); c != 400 {
		t.Fatalf("bad status: %d", c)
	}
	if c := post(h, "tok", `[{"target":"zzz","location":"home","at":"`+at+`","status":"ok"}]`); c != 400 {
		t.Fatalf("unknown target: %d", c)
	}
	if c := post(h, "tok", good); c != 204 {
		t.Fatalf("good: %d", c)
	}
	w := get(t, h, "/api/v1/status")
	var v StatusView
	json.Unmarshal(w.Body.Bytes(), &v)
	if l := v.Targets[1].Locations; len(l) != 1 || l[0].Location != "home" || l[0].Status != probe.StatusDown {
		t.Fatalf("ingested result not shown: %+v", l)
	}
}
