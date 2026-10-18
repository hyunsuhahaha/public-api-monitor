package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSchemaPaths(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)

	// first answer: the baseline (everything is "new" but there was no shape before)
	added, had, err := st.ObservePaths(ctx, "a", []string{"x", "y"}, t0)
	if err != nil || had || len(added) != 2 {
		t.Fatalf("baseline: %v %v %v", added, had, err)
	}
	// same fields again: nothing new
	if added, had, _ := st.ObservePaths(ctx, "a", []string{"x", "y"}, t0.Add(5*time.Minute)); !had || len(added) != 0 {
		t.Fatalf("unchanged: %v %v", added, had)
	}
	// a new field appears
	if added, _, _ := st.ObservePaths(ctx, "a", []string{"x", "y", "z"}, t0.Add(10*time.Minute)); !reflect.DeepEqual(added, []string{"z"}) {
		t.Fatalf("added: %v", added)
	}

	// "y" stops coming; the API keeps answering for 25 hours
	at := t0.Add(10 * time.Minute)
	for i := 0; i < 25; i++ {
		at = at.Add(time.Hour)
		st.ObservePaths(ctx, "a", []string{"x", "z"}, at)
	}
	gone, err := st.StalePaths(ctx, at, 24*time.Hour, time.Hour)
	if err != nil || !reflect.DeepEqual(gone["a"], []string{"y"}) {
		t.Fatalf("removed: %v %v", gone, err)
	}
	// reported once: the path is forgotten
	if gone, _ := st.StalePaths(ctx, at, 24*time.Hour, time.Hour); len(gone) != 0 {
		t.Fatalf("reported twice: %v", gone)
	}

	// "b" went quiet altogether (down or empty for 2 days): nothing is "removed"
	st.ObservePaths(ctx, "b", []string{"p"}, t0)
	if gone, _ := st.StalePaths(ctx, t0.Add(48*time.Hour), 24*time.Hour, time.Hour); len(gone["b"]) != 0 {
		t.Fatalf("a silent API lost fields: %v", gone)
	}

	e := &SchemaEvent{Target: "a", At: at, Kind: "removed", Paths: []string{"y", "w"}}
	if err := st.SaveSchemaEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	list, _ := st.SchemaEvents(ctx, "a", t0, 10)
	if len(list) != 1 || !reflect.DeepEqual(list[0].Paths, []string{"y", "w"}) || list[0].Kind != "removed" {
		t.Fatalf("events: %+v", list)
	}
}
