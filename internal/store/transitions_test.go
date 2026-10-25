package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

func TestTransitionsOnWriteAndBackfill(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t0 := time.Now().Add(-6 * time.Hour).Truncate(time.Minute)
	seq := []probe.Status{probe.StatusOK, probe.StatusDown, probe.StatusDown, probe.StatusOK, probe.StatusOK}
	for i, s := range seq {
		st.SaveVerdict(ctx, Verdict{TargetID: "x", At: t0.Add(time.Duration(i) * 5 * time.Minute), Status: s, Voters: 1})
	}
	// an hour later: not the next check of the last one
	st.SaveVerdict(ctx, Verdict{TargetID: "x", At: t0.Add(80 * time.Minute), Status: probe.StatusDown, Voters: 1})
	got, err := st.Transitions(ctx, t0.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := Transition{"ok": {"down": 1, "ok": 1}, "down": {"down": 1, "ok": 1}}
	if !reflect.DeepEqual(got["x"], want) {
		t.Fatalf("on write: %v", got["x"])
	}
	// the same counts from the stored verdicts, for a database from before the table
	st.db.Exec(`DELETE FROM transitions`)
	if err := st.backfillTransitions(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Transitions(ctx, t0.Add(-24*time.Hour)); !reflect.DeepEqual(got["x"], want) {
		t.Fatalf("backfill: %v", got["x"])
	}
	st.Close()
}
