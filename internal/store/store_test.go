package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// An open incident (zero Ended) must be found as open, in the list and after reopening the
// database. It was stored as year-1 milliseconds and vanished from all three.
func TestOpenIncidentRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	in := &incident.Incident{TargetID: "a", Started: time.Now().Add(-time.Minute), Severity: 2, Status: probe.StatusDown, Reason: "timeout"}
	if err := st.SaveIncident(ctx, in); err != nil {
		t.Fatal(err)
	}
	open, _ := st.OpenIncidents(ctx)
	list, _ := st.Incidents(ctx, "a", time.Now().Add(-time.Hour), 10)
	if len(open) != 1 || !open[0].Open() || len(list) != 1 {
		t.Fatalf("open %d, listed %d", len(open), len(list))
	}
	// a row written by the old code: ended as year-1 milliseconds
	st.db.Exec(`UPDATE incidents SET ended=? WHERE id=?`, time.Time{}.UnixMilli(), in.ID)
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if open, _ := st.OpenIncidents(ctx); len(open) != 1 {
		t.Fatalf("old open row not migrated: %d", len(open))
	}
}

func TestCheckPhasesRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	ph := probe.Phases{DNSMs: 3, ConnectMs: 12, TLSMs: 40, WaitMs: 900}
	if err := st.SaveCheck(ctx, probe.Result{TargetID: "a", Location: "l", At: time.Now(), Status: probe.StatusOK, LatencyMS: 960, HTTPStatus: 200, Phases: ph}); err != nil {
		t.Fatal(err)
	}
	rs, err := st.RecentChecks(ctx, "a", 5)
	if err != nil || len(rs) != 1 || rs[0].Phases != ph {
		t.Fatalf("%+v %v", rs, err)
	}
}

func TestFollowUpsRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now()
	for i, f := range []probe.FollowUp{
		{At: now.Add(-2 * time.Hour), Answered: true, AfterMs: 79, HTTP: 200, WaitMs: 60000, Timeout: 10000},
		{At: now.Add(-time.Hour), Answered: false, AfterMs: 60000, WaitMs: 60000, Timeout: 10000, Stalled: "x"},
	} {
		if err := st.SaveFollowUp(ctx, "a", f); err != nil {
			t.Fatal(i, err)
		}
	}
	list, err := st.FollowUps(ctx, "a", now.Add(-24*time.Hour))
	if err != nil || len(list) != 2 || list[0].Outcome() != "hung" || list[1].Outcome() != "retried" || list[1].AfterMs != 79 {
		t.Fatalf("%+v %v", list, err)
	}
}
