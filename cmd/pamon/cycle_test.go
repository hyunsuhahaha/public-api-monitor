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
		if target.Classifier != "public" {
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
