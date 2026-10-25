package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestOfficialRuns(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Now().Add(-time.Hour).Truncate(time.Minute)
	gap := 15 * time.Minute
	for _, x := range []struct {
		min int
		ind string
	}{{0, "none"}, {5, "none"}, {10, "major"}, {15, "major"}, {20, "none"}, {60, "none"}} { // 20 -> 60: a hole
		if err := st.RecordOfficial(ctx, "GitHub", x.ind, x.ind, t0.Add(time.Duration(x.min)*time.Minute), gap); err != nil {
			t.Fatal(err)
		}
	}
	st.RecordOfficial(ctx, "Slack", "none", "ok", t0, gap)
	runs, err := st.OfficialRuns(ctx, "GitHub", t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := [][3]any{{0, 5, "none"}, {10, 15, "major"}, {20, 20, "none"}, {60, 60, "none"}}
	if len(runs) != len(want) {
		t.Fatalf("runs: %+v", runs)
	}
	for i, w := range want {
		r := runs[i]
		if r.Since != t0.Add(time.Duration(w[0].(int))*time.Minute) || r.Until != t0.Add(time.Duration(w[1].(int))*time.Minute) || r.Indicator != w[2] {
			t.Errorf("run %d: %+v, want %v", i, r, w)
		}
	}
	all, _ := st.OfficialRuns(ctx, "", t0.Add(-time.Hour))
	if len(all) != 5 {
		t.Fatalf("all providers: %d", len(all))
	}
}
