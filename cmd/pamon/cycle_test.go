package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/monitor"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/quorum"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
	"github.com/hyunsuhahaha/public-api-monitor/internal/web"
)

// dataFile and the pages' fetch shim must name a URL's snapshot file the same way.
func TestDataFileMatchesShim(t *testing.T) {
	for u, want := range map[string]string{
		"/api/v1/status": "data/api/v1/status.json",
		"/version":       "data/version.json",
		"/api/v1/series?target=kma-typhoon&range=24h&shift=1d": "data/api/v1/series_target_kma-typhoon_range_24h_shift_1d.json",
		"/api/v1/incidents/42":                                 "data/api/v1/incidents/42.json",
	} {
		if got := dataFile(u); got != want {
			t.Errorf("dataFile(%q) = %q, want %q", u, got, want)
		}
	}
	if !strings.Contains(fetchShim, `q.replace(/[^A-Za-z0-9._-]/g, "_")`) {
		t.Error("the shim's escaping no longer matches unsafeChars")
	}
}

func TestDumpNeverPrintsRegistryToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"token":"must-not-appear-in-dump","expires_in":300}`)
	}))
	defer srv.Close()
	cfg, err := config.Parse([]byte("targets:\n  - id: token\n    auth: none\n    classifier: docker_token\n    url: " + srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	previous := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = previous }()
	code := dumpTarget(cfg, "token")
	writer.Close()
	os.Stdout = previous
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 || strings.Contains(string(output), "must-not-appear") || !strings.Contains(string(output), "body hidden") {
		t.Fatalf("unsafe dump: %s", output)
	}
}

// Exercise the actual configured canaries through collection, SQLite, HTTP views,
// and Pages export. Secrets must not appear in any generated public artifact.
func TestMapCycleExportsVerifiedResults(t *testing.T) {
	t.Setenv("KAKAO_REST_API_KEY", "cycle-kakao-secret")
	t.Setenv("NAVER_MAPS_CLIENT_ID", "cycle-naver-id")
	t.Setenv("NAVER_MAPS_CLIENT_SECRET", "cycle-naver-secret")
	t.Setenv("PAMON_WEBHOOK_URL", "")
	t.Setenv("PAMON_SITE_URL", "https://pamon.test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v2/local") {
			if r.Header.Get("Authorization") != "KakaoAK cycle-kakao-secret" {
				t.Error("missing Kakao auth")
			}
		} else if r.Header.Get("x-ncp-apigw-api-key-id") != "cycle-naver-id" || r.Header.Get("x-ncp-apigw-api-key") != "cycle-naver-secret" {
			t.Error("missing NCP auth")
		}
		switch r.URL.Path {
		case "/v2/local/search/address.json", "/v2/local/search/keyword.json":
			fmt.Fprint(w, `{"documents":[{"x":"126.978","y":"37.566"}]}`)
		case "/map-geocode/v2/geocode":
			fmt.Fprint(w, `{"status":"OK","addresses":[{"x":"126.978","y":"37.566"}]}`)
		case "/map-direction/v1/driving":
			fmt.Fprint(w, `{"code":0,"route":{"traoptimal":[{"summary":{"distance":2500,"duration":600000}}]}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	raw, err := os.ReadFile("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.NewReplacer("https://dapi.kakao.com", srv.URL, "https://maps.apigw.ntruss.com", srv.URL).Replace(string(raw))
	cfg, err := config.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	maps := cfg.Targets[:0]
	for _, target := range cfg.Targets {
		if target.Classifier == "kakao" || target.Classifier == "naver_maps" {
			maps = append(maps, target)
		}
	}
	cfg.Targets = maps
	if len(maps) != 4 {
		t.Fatalf("got %d canaries", len(maps))
	}
	dir := t.TempDir()
	site := filepath.Join(dir, "site")
	if err := runCycle(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, probe.New("test"), filepath.Join(dir, "test.db"), "test", 24*time.Hour, site); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(site, "data/api/v1/status.json"))
	if err != nil {
		t.Fatal(err)
	}
	var status struct {
		Targets []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(b, &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Targets) != 4 {
		t.Fatal("missing exported map targets")
	}
	for _, path := range []string{"llms.txt", "ai/index.html", "ai/diagnostics.json", "ai/targets/kakao-address.json"} {
		b, err := os.ReadFile(filepath.Join(site, path))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(path, ".json") {
			var diagnostic web.DiagnosticView
			if err := json.Unmarshal(b, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if len(diagnostic.Targets) == 0 {
				t.Fatal("empty agent export")
			}
			for _, target := range diagnostic.Targets {
				if target.Assessment != "no_failure_observed" || target.FreshUntil == nil {
					t.Fatalf("bad diagnostic: %+v", target)
				}
			}
		}
	}
	for _, target := range status.Targets {
		if target.Status != "ok" {
			t.Errorf("%s exported %s", target.ID, target.Status)
		}
	}
	// answer pages: one per target plus the index, all in the sitemap
	sm, err := os.ReadFile(filepath.Join(site, "sitemap.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sm), "<loc>https://pamon.test/s/</loc>") {
		t.Errorf("sitemap lacks s/: %s", sm)
	}
	// stylesheets and scripts carry a content version, so browsers never mix old and new
	idx, _ := os.ReadFile(filepath.Join(site, "index.html"))
	if !regexp.MustCompile(`href="app\.[0-9a-f]{8}\.css"`).Match(idx) || !regexp.MustCompile(`src="app\.[0-9a-f]{8}\.js"`).Match(idx) {
		t.Errorf("index.html assets not versioned")
	}
	ans, _ := os.ReadFile(filepath.Join(site, "s", maps[0].ID+".html"))
	if !regexp.MustCompile(`src="\.\./report\.[0-9a-f]{8}\.js"`).Match(ans) {
		t.Errorf("answer page script not versioned")
	}
	if css := regexp.MustCompile(`app\.[0-9a-f]{8}\.css`).Find(idx); css == nil {
		t.Error("no hashed stylesheet")
	} else if _, err := os.Stat(filepath.Join(site, string(css))); err != nil {
		t.Errorf("index.html links %s, not exported", css)
	}
	for _, page := range []string{"lead-time.html", "maintenance.html", "retry-advice.html"} {
		if !strings.Contains(string(sm), "<loc>https://pamon.test/"+page+"</loc>") {
			t.Errorf("sitemap lacks %s", page)
		}
	}
	for _, f := range []string{"lead-time.html", "maintenance.html", "maintenance.json", "maintenance.ics", "data/api/v1/lead-time.json", "retry-advice.html", "data/api/v1/retry-advice.json", "feed.xml", "feed/" + maps[0].ID + ".xml",
		"badge/summary.svg", "badge/" + maps[0].ID + ".svg", "badge/" + maps[0].ID + "-status.svg", "w/" + maps[0].ID + ".html"} {
		if _, err := os.Stat(filepath.Join(site, f)); err != nil {
			t.Errorf("export lacks %s", f)
		}
	}
	for _, target := range maps {
		b, err := os.ReadFile(filepath.Join(site, "s", target.ID+".html"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), target.Name+" 지금 되나요?") || !strings.Contains(string(b), "정상입니다") {
			t.Errorf("s/%s.html: no verdict", target.ID)
		}
		if !strings.Contains(string(sm), "<loc>https://pamon.test/s/"+target.ID+".html</loc>") {
			t.Errorf("sitemap lacks s/%s.html", target.ID)
		}
	}
	if err := filepath.WalkDir(site, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"cycle-kakao-secret", "cycle-naver-id", "cycle-naver-secret"} {
			if strings.Contains(string(b), secret) {
				t.Errorf("secret leaked in %s", path)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// -paths in CI: a 404 is "gone" unless the target expects exactly that 404 and got it.
func TestEndpointGone(t *testing.T) {
	plain := &config.Target{ID: "plain"}
	bucket := &config.Target{ID: "bucket", ExpectStatus: http.StatusNotFound}
	for _, c := range []struct {
		name string
		t    *config.Target
		r    probe.Result
		want bool
	}{
		{"code 12", plain, probe.Result{Code: "12", HTTPStatus: 200, Status: probe.StatusError}, true},
		{"plain 404", plain, probe.Result{HTTPStatus: 404, Status: probe.StatusClient}, true},
		{"403 from abroad", plain, probe.Result{HTTPStatus: 403, Status: probe.StatusClient}, false},
		{"timeout", plain, probe.Result{Status: probe.StatusDown}, false},
		{"expected 404", bucket, probe.Result{HTTPStatus: 404, Status: probe.StatusOK}, false},
		{"expected 404, slow", bucket, probe.Result{HTTPStatus: 404, Status: probe.StatusSlow}, false},
		{"404 without the service's text", bucket, probe.Result{HTTPStatus: 404, Status: probe.StatusError}, true},
	} {
		if got := endpointGone(c.t, c.r); got != c.want {
			t.Errorf("%s: endpointGone = %v, want %v", c.name, got, c.want)
		}
	}
}

// Korea pushes into the database between runs; a run counts its latest results: a
// service judged from Korea follows Korea, the rest fail when either location fails.
func TestCycleCountsRemoteLocations(t *testing.T) {
	t.Setenv("PAMON_WEBHOOK_URL", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/judged" {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	cfg, err := config.Parse([]byte(`
locations:
  - {id: us}
  - {id: kr, primary_for: [judged]}
targets:
  - {id: voted, url: "` + srv.URL + `/voted", classifier: http, auth: none}
  - {id: judged, url: "` + srv.URL + `/judged", classifier: http, auth: none}
  - {id: kr-only, url: "` + srv.URL + `/never", classifier: http, auth: none, locations: [kr]}
`))
	if err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	for _, r := range []probe.Result{
		{TargetID: "voted", Location: "kr", At: at, Status: probe.StatusDown, Reason: "timeout: 연결 대기 중"},
		{TargetID: "judged", Location: "kr", At: at, Status: probe.StatusOK},
		{TargetID: "kr-only", Location: "kr", At: at, Status: probe.StatusOK},
	} {
		if err := st.SaveCheck(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	if err := runCycle(slog.New(slog.NewTextHandler(io.Discard, nil)), cfg, probe.New("us"), db, "us", 24*time.Hour, ""); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v, err := st.LatestVerdicts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := v["voted"]; got.Status != probe.StatusDown || !strings.Contains(got.Reason, "(kr)") || got.Voters != 2 {
		t.Errorf("voted: %+v", got)
	}
	if got := v["judged"]; got.Status != probe.StatusOK || !strings.Contains(got.Reason, "failing only from us") {
		t.Errorf("judged: %+v", got)
	}
	if _, ok := v["kr-only"]; ok {
		t.Error("a Korea-only target is not checked from the US, so the US run gives it no verdict")
	}
}

// pamon -role ingest: the endpoint and a health check, nothing else; results land in
// the database, from a location that may check the target.
func TestIngestHandler(t *testing.T) {
	cfg, err := config.Parse([]byte(`
locations: [{id: us}, {id: kr}]
targets:
  - {id: a, url: "http://x"}
  - {id: kr-only, url: "http://x", locations: [kr]}
`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &monitor.Central{Cfg: cfg, Store: st, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Board: quorum.NewBoard(), Tracker: incident.NewTracker(2, 2)}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	h := (&web.Server{Cfg: cfg, Store: st, Ingest: c.Ingest, IngestToken: "tok"}).IngestHandler()
	send := func(token, body string) int {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		h.ServeHTTP(w, r)
		return w.Code
	}
	at := time.Now().UTC().Format(time.RFC3339)
	ok := `[{"target":"kr-only","location":"kr","at":"` + at + `","status":"ok","latencyMs":40}]`
	if code := send("wrong", ok); code != http.StatusUnauthorized {
		t.Errorf("bad token: %d", code)
	}
	if code := send("tok", strings.Replace(ok, `"location":"kr"`, `"location":"us"`, 1)); code != http.StatusBadRequest {
		t.Errorf("a Korea-only target from the US: %d", code)
	}
	if code := send("tok", ok); code != http.StatusNoContent {
		t.Fatalf("valid batch: %d", code)
	}
	byLoc, _ := st.LatestByLocation(context.Background(), time.Now().Add(-time.Hour))
	if len(byLoc["kr-only"]) != 1 || byLoc["kr-only"][0].Location != "kr" {
		t.Errorf("stored: %+v", byLoc)
	}
	for _, path := range []string{"/", "/api/v1/status", "/s/a"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s served by the ingest process: %d", path, w.Code)
		}
	}
}

func TestRemoveOldVersions(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor"), 0o755)
	for _, f := range []string{"app.css", "app.11111111.css", "app.22222222.css", "app.11111111.js", "vendor/uPlot.min.33333333.css"} {
		os.WriteFile(filepath.Join(dir, filepath.FromSlash(f)), nil, 0o644)
	}
	removeOldVersions(dir, "app.css", "app.22222222.css")
	removeOldVersions(dir, "vendor/uPlot.min.css", "vendor/uPlot.min.44444444.css")
	for f, want := range map[string]bool{"app.css": true, "app.11111111.css": false, "app.22222222.css": true, "app.11111111.js": true, "vendor/uPlot.min.33333333.css": false} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f))); (err == nil) != want {
			t.Errorf("%s: exists=%v, want %v", f, err == nil, want)
		}
	}
}
