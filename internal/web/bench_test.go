package web

import (
	"context"
	"fmt"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// A month of production-sized data: 45 targets checked every 5 minutes from 2 locations
// (raw checks) with one verdict per check slot. Built once into a cached file because it
// takes a while; delete it to rebuild.
func monthDB(b *testing.B) (*store.Store, *config.Config) {
	b.Helper()
	var sb strings.Builder
	sb.WriteString("targets:\n")
	for i := 0; i < 45; i++ {
		fmt.Fprintf(&sb, "  - {id: t%02d, name: API %02d, provider: p%d, category: c%d, url: http://x}\n", i, i, i%9, i%7)
	}
	cfg, err := config.Parse([]byte(sb.String()))
	if err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(os.TempDir(), "pamon-bench-month.db")
	_, statErr := os.Stat(path)
	st, err := store.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	if statErr == nil {
		return st, cfg
	}
	ctx := context.Background()
	rng := rand.New(rand.NewSource(1))
	now := time.Now()
	start := now.Add(-30 * 24 * time.Hour)
	for at := start; at.Before(now); at = at.Add(5 * time.Minute) {
		for _, t := range cfg.Targets {
			status := probe.StatusOK
			if rng.Float64() < 0.004 {
				status = probe.StatusDown
			}
			for _, loc := range []string{"central", "probe"} {
				st.SaveCheck(ctx, probe.Result{TargetID: t.ID, Location: loc, At: at, Status: status, LatencyMS: int64(30 + rng.Intn(400)), HTTPStatus: 200})
			}
			st.SaveVerdict(ctx, store.Verdict{TargetID: t.ID, At: at, Status: status, Voters: 2})
		}
	}
	return st, cfg
}

// go test ./internal/web -run x -bench Overview -benchtime 5x
func BenchmarkOverview(b *testing.B) {
	st, cfg := monthDB(b)
	defer st.Close()
	h := (&Server{Cfg: cfg, Store: st, LastCheck: time.Now}).Handler()
	for _, rg := range []string{"6h", "24h", "7d", "30d"} {
		b.Run(rg+"-cold", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				// a new server each time: no response cache
				h := (&Server{Cfg: cfg, Store: st, LastCheck: time.Now}).Handler()
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/overview?range="+rg, nil))
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
		b.Run(rg+"-cached", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/overview?range="+rg, nil))
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
		})
	}
	b.Run("series-30d-cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			h := (&Server{Cfg: cfg, Store: st, LastCheck: time.Now}).Handler()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/series?target=t07&range=30d", nil))
			if w.Code != 200 {
				b.Fatal(w.Code)
			}
		}
	})
}
