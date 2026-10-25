package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestObserveTypes(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	t0 := time.Now().Add(-48 * time.Hour)
	day := 24 * time.Hour
	obs := func(at time.Time, types map[string]string) []TypeChange {
		ch, err := st.ObserveTypes(ctx, "x", types, at, day)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}
	if ch := obs(t0, map[string]string{"price": "number", "items": "array"}); ch != nil {
		t.Fatalf("baseline: %v", ch)
	}
	if ch := obs(t0.Add(time.Hour), map[string]string{"price": "null"}); ch != nil {
		t.Fatalf("inside the warm-up: %v", ch)
	}
	ch := obs(t0.Add(2*day), map[string]string{"price": "string", "items": "empty_array", "new": "string"})
	if len(ch) != 2 || ch[0].Path != "items" || ch[0].Now != "empty_array" || ch[1].Path != "price" || len(ch[1].Was) != 2 {
		t.Fatalf("changes: %+v", ch)
	}
	if s := ch[1].String(func(k string) string { return k }); s != "price: null/number → string" {
		t.Fatal(s)
	}
	if ch := obs(t0.Add(2*day+time.Hour), map[string]string{"price": "string"}); ch != nil {
		t.Fatalf("a kind is reported once: %v", ch)
	}
}
