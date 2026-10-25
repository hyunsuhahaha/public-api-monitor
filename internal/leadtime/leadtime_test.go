package leadtime

import (
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

var t0 = time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func run(from, to int, ind string) store.OfficialRun {
	return store.OfficialRun{Provider: "GitHub", Since: at(from), Until: at(to), Indicator: ind, Description: ind}
}

func ep(start, end int) Episode {
	e := Episode{Provider: "GitHub", Targets: []string{"gh"}, Started: at(start), Confirmed: at(start + 5)}
	if end != 0 {
		e.Ended = at(end)
	}
	return e
}

func TestCompare(t *testing.T) {
	now := at(24 * 60)
	cases := []struct {
		name string
		e    Episode
		runs []store.OfficialRun
		kind Kind
		lead int
	}{
		{"pamon 20 minutes first", ep(0, 60), []store.OfficialRun{run(-180, 20, "none"), run(25, 70, "major"), run(75, 300, "none")}, Announced, 20},
		{"official first", ep(0, 60), []store.OfficialRun{run(-180, -10, "none"), run(-5, 70, "minor"), run(75, 300, "none")}, Announced, -10},
		{"never announced", ep(0, 60), []store.OfficialRun{run(-180, 300, "none")}, Never, 0},
		{"a hole while it happened", ep(0, 60), []store.OfficialRun{run(-180, 0, "none"), run(90, 300, "none")}, NoCoverage, 0},
		{"announced after a hole: undated", ep(0, 60), []store.OfficialRun{run(-180, 0, "none"), run(40, 300, "major")}, NoCoverage, 0},
		{"page started watching mid-outage", ep(0, 60), []store.OfficialRun{run(30, 300, "major")}, NoCoverage, 0},
		{"already reporting something else", ep(0, 60), []store.OfficialRun{run(-300, -200, "none"), run(-195, 300, "minor")}, AlreadyOn, 0},
		{"open, nothing yet", ep(0, 0), []store.OfficialRun{run(-180, 24*60, "none")}, Pending, 0},
		{"ended 30 minutes ago: too soon for never", ep(0, 23*60+30), []store.OfficialRun{run(-180, 24*60, "none")}, Pending, 0},
		{"a later unrelated incident is outside the window", ep(0, 60), []store.OfficialRun{run(-180, 200, "none"), run(205, 300, "major")}, Never, 0},
	}
	for _, c := range cases {
		r := Compare(c.e, c.runs, now)
		if r.Kind != c.kind || r.LeadMinutes != c.lead {
			t.Errorf("%s: got %s %d, want %s %d", c.name, r.Kind, r.LeadMinutes, c.kind, c.lead)
		}
	}
}

func TestEpisodesJoinOneProviderAndSkipDegradations(t *testing.T) {
	incs := []*incident.Incident{
		{ID: 1, TargetID: "gh-api", Started: at(0), Ended: at(30), Severity: 2},
		{ID: 2, TargetID: "gh-git", Started: at(10), Ended: at(50), Severity: 2},
		{ID: 3, TargetID: "gh-api", Started: at(200), Ended: at(210), Severity: 2},
		{ID: 4, TargetID: "gh-api", Started: at(400), Severity: 1}, // slow: not an outage
		{ID: 5, TargetID: "x", Started: at(0), Severity: 2},        // no status page
	}
	prov := func(id string) string {
		if id == "x" {
			return ""
		}
		return "GitHub"
	}
	eps := Episodes(incs, prov, func(string) time.Duration { return 5 * time.Minute })
	if len(eps) != 2 || len(eps[0].Incidents) != 2 || len(eps[0].Targets) != 2 || !eps[0].Ended.Equal(at(50)) || !eps[0].Confirmed.Equal(at(5)) {
		t.Fatalf("episodes: %+v", eps)
	}
}

func TestSummarizeCountsOnlyComparable(t *testing.T) {
	rs := []Result{{Kind: Announced, LeadMinutes: 20}, {Kind: Announced, LeadMinutes: 10}, {Kind: Announced, LeadMinutes: -10},
		{Kind: Never}, {Kind: Pending}, {Kind: NoCoverage}}
	s := Summarize("GitHub", rs)
	if s.Announced != 3 || s.Never != 1 || s.Excluded != 2 || s.Counted() != 4 || s.PamonFirst != 2 || s.OfficialFirst != 1 ||
		s.MedianMinutes != 10 || s.MeanMinutes != 20.0/3 {
		t.Fatalf("%+v", s)
	}
}
