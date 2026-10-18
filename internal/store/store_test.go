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
