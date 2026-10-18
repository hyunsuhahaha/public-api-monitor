package incident

import (
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

var t0 = time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)

func res(i int, s probe.Status, reason string) probe.Result {
	return probe.Result{TargetID: "a", At: t0.Add(time.Duration(i) * 5 * time.Minute), Status: s, Reason: reason}
}

func TestOneBadCheckIsNoise(t *testing.T) {
	tr := NewTracker(2, 2)
	if ev, _ := tr.Observe(res(0, probe.StatusDown, "timeout")); ev != None {
		t.Fatal("one failure must not open an incident")
	}
	if ev, _ := tr.Observe(res(1, probe.StatusOK, "")); ev != None {
		t.Fatal("recovery without incident")
	}
	if ev, _ := tr.Observe(res(2, probe.StatusDown, "timeout")); ev != None {
		t.Fatal("bad count must reset after a good check")
	}
}

func TestOpenUpdateClose(t *testing.T) {
	tr := NewTracker(2, 2)
	tr.Observe(res(0, probe.StatusSlow, "slow: 4s"))
	ev, in := tr.Observe(res(1, probe.StatusSlow, "slow: 5s"))
	if ev != Opened || in.Severity != 1 || !in.Started.Equal(res(0, "", "").At) || in.Reason != "slow: 4s" {
		t.Fatalf("open: %v %+v", ev, in)
	}
	ev, in = tr.Observe(res(2, probe.StatusDown, "timeout"))
	if ev != Updated || in.Severity != 2 || in.Status != probe.StatusDown {
		t.Fatalf("escalate: %v %+v", ev, in)
	}
	if ev, _ := tr.Observe(res(3, probe.StatusDown, "timeout")); ev != None {
		t.Fatal("same reason, same severity: no update")
	}
	// client results (our key) are ignored: they neither close nor count as good
	if ev, _ := tr.Observe(res(4, probe.StatusClient, "key")); ev != None {
		t.Fatal("client must be ignored")
	}
	tr.Observe(res(5, probe.StatusOK, ""))
	ev, in = tr.Observe(res(6, probe.StatusOK, ""))
	if ev != Closed || !in.Ended.Equal(res(6, "", "").At) || in.Open() {
		t.Fatalf("close: %v %+v", ev, in)
	}
}

func TestOpenTakesWorstOfTheRun(t *testing.T) {
	tr := NewTracker(2, 2)
	tr.Observe(res(0, probe.StatusDown, "http 503"))
	_, in := tr.Observe(res(1, probe.StatusSlow, "slow"))
	if in.Severity != 2 || in.Status != probe.StatusDown {
		t.Fatalf("want outage, got %+v", in)
	}
}

func TestRestore(t *testing.T) {
	tr := NewTracker(2, 2)
	tr.Restore(&Incident{ID: 7, TargetID: "a", Started: t0, Severity: 2})
	tr.Observe(res(1, probe.StatusOK, ""))
	ev, in := tr.Observe(res(2, probe.StatusOK, ""))
	if ev != Closed || in.ID != 7 {
		t.Fatalf("restored incident should close: %v %+v", ev, in)
	}
}
