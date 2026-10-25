package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
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
