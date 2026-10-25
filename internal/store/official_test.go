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

func TestOfficialIncidentsAndHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	in := func(id string, d int, resolved int) OfficialIncident {
		x := OfficialIncident{Provider: "GitHub", ID: id, Name: id, Impact: "minor", Created: day(d)}
		if resolved > 0 {
			x.Resolved = day(resolved)
		}
		return x
	}
	// first read: a full page back to the 5th; next read: back to the 10th only (newer
	// incidents pushed older ones off), joining the first: still complete since the 5th
	if err := st.SaveOfficialIncidents(ctx, "GitHub", []OfficialIncident{in("a", 5, 6), in("b", 12, 0)}, day(5), day(15)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveOfficialIncidents(ctx, "GitHub", []OfficialIncident{in("b", 12, 13), in("c", 16, 0)}, day(10), day(17)); err != nil {
		t.Fatal(err)
	}
	list, hist, err := st.OfficialIncidents(ctx, day(1))
	if err != nil {
		t.Fatal(err)
	}
	h := hist["GitHub"]
	if len(list) != 3 || !list[1].Resolved.Equal(day(13)) || !h.Since.Equal(day(5)) || !h.Fetched.Equal(day(17)) || !h.Covers(day(6), day(16)) || h.Covers(day(4), day(16)) {
		t.Fatalf("%+v %+v", list, h)
	}
	// a read that does not join (its list starts after the last read): starts over
	st.SaveOfficialIncidents(ctx, "GitHub", nil, day(25), day(26))
	_, hist, _ = st.OfficialIncidents(ctx, day(1))
	if !hist["GitHub"].Since.Equal(day(25)) {
		t.Fatalf("%+v", hist["GitHub"])
	}
}
