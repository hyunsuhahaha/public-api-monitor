package quorum

import (
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

var now = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func r(loc string, s probe.Status, reason string) probe.Result {
	return probe.Result{TargetID: "a", Location: loc, At: now, Status: s, Reason: reason}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name   string
		in     []probe.Result
		status probe.Status
		reason string // substring
	}{
		{"single location ok", []probe.Result{r("seoul", probe.StatusOK, "")}, probe.StatusOK, ""},
		{"single location down", []probe.Result{r("seoul", probe.StatusDown, "timeout")}, probe.StatusDown, "timeout"},
		{"one of two down is regional", []probe.Result{r("seoul", probe.StatusOK, ""), r("chuncheon", probe.StatusDown, "timeout")}, probe.StatusOK, "failing only from chuncheon"},
		{"two of three down", []probe.Result{r("a", probe.StatusDown, "timeout"), r("b", probe.StatusError, "code 02"), r("c", probe.StatusOK, "")}, probe.StatusDown, "timeout (a, b)"},
		{"all down", []probe.Result{r("a", probe.StatusError, "code 05"), r("b", probe.StatusError, "code 05")}, probe.StatusError, "code 05"},
		{"client does not vote", []probe.Result{r("a", probe.StatusClient, "key"), r("b", probe.StatusDown, "timeout")}, probe.StatusDown, "timeout"},
		{"only client", []probe.Result{r("a", probe.StatusClient, "http 403")}, probe.StatusClient, "http 403"},
		{"nobody", nil, probe.StatusClient, "no recent checks"},
		{"majority slow", []probe.Result{r("a", probe.StatusSlow, "slow: 4s"), r("b", probe.StatusSlow, "slow: 5s"), r("c", probe.StatusOK, "")}, probe.StatusSlow, "slow: 4s"},
		{"slow and down vs ok: degraded, not outage", []probe.Result{r("a", probe.StatusSlow, "slow"), r("b", probe.StatusDown, "timeout"), r("c", probe.StatusOK, "")}, probe.StatusSlow, "failing only from b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Decide("a", now, c.in)
			if v.Status != c.status || !strings.Contains(v.Reason, c.reason) {
				t.Fatalf("got %s %q, want %s ~%q", v.Status, v.Reason, c.status, c.reason)
			}
		})
	}
}

func TestBoardFreshAndOrder(t *testing.T) {
	b := NewBoard()
	b.Update(r("seoul", probe.StatusOK, ""))
	old := r("tokyo", probe.StatusDown, "timeout")
	old.At = now.Add(-30 * time.Minute)
	b.Update(old)
	// an out-of-order older result must not replace a newer one
	stale := r("seoul", probe.StatusDown, "late")
	stale.At = now.Add(-time.Minute)
	b.Update(stale)
	got := b.Fresh("a", now, 12*time.Minute)
	if len(got) != 1 || got[0].Location != "seoul" || got[0].Status != probe.StatusOK {
		t.Fatalf("%+v", got)
	}
}
