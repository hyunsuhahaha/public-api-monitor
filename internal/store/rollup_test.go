package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

func TestHistQuantile(t *testing.T) {
	h := NewHist()
	for i := 0; i < 90; i++ {
		h.add(40) // bucket (25, 50]
	}
	for i := 0; i < 10; i++ {
		h.add(900) // bucket (750, 1000]
	}
	if p50 := h.Quantile(0.5); p50 <= 25 || p50 > 50 {
		t.Fatalf("p50 %d not in (25,50]", p50)
	}
	if p99 := h.Quantile(0.99); p99 <= 750 || p99 > 1000 {
		t.Fatalf("p99 %d not in (750,1000]", p99)
	}
	if NewHist().Quantile(0.5) != 0 {
		t.Fatal("empty histogram")
	}
}

// A database written before the rollup tables existed (raw rows, no hourly rows) must be
// backfilled on open, and must not hang: the backfill once ran a statement outside its
// transaction while that held the only connection, and the first start never returned.
func TestBackfillOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Date(2026, 10, 18, 10, 5, 0, 0, time.UTC)
	for i, s := range []probe.Status{probe.StatusOK, probe.StatusOK, probe.StatusDown} {
		r := probe.Result{TargetID: "a", Location: "home", At: at.Add(time.Duration(i) * time.Minute), Status: s, LatencyMS: 40, HTTPStatus: 200}
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVerdict(ctx, Verdict{TargetID: "a", At: r.At, Status: s, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// make it look like an old database
	for _, q := range []string{`DELETE FROM hourly`, `DELETE FROM latest_checks`, `DELETE FROM latest_verdicts`} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()

	done := make(chan error, 1)
	go func() {
		st2, err := Open(path)
		if err == nil {
			defer st2.Close()
			rows, err2 := st2.Hourly(ctx, "a", at.Add(-time.Hour), at.Add(time.Hour), false)
			if err2 == nil && len(rows) != 3 { // "*", "home", "ALL"
				err2 = fmt.Errorf("hourly rows: %d", len(rows))
			}
			for _, r := range rows {
				if r.Location == "*" && (r.Counted != 3 || r.Healthy != 2 || r.Outage != 1) {
					err2 = fmt.Errorf("verdict counts: %+v", r.Bucket)
				}
				if r.Location == AllLocations && r.Hist.Count() != 2 {
					err2 = fmt.Errorf("ALL histogram count: %d", r.Hist.Count())
				}
			}
			latest, _ := st2.LatestByLocation(ctx, at.Add(-time.Hour))
			if err2 == nil && (len(latest["a"]) != 1 || latest["a"][0].Status != probe.StatusDown) {
				err2 = fmt.Errorf("latest check: %+v", latest)
			}
			err = err2
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Open hung while backfilling")
	}
}
