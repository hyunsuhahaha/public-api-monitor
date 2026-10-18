// Package quorum combines the latest check of each probe location into one verdict per
// target. One location failing is usually that location's network, not the API: the
// verdict is bad only when a majority of the locations that reported recently see it bad.
package quorum

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// Board keeps the latest result per target and location. Safe for concurrent use.
type Board struct {
	mu     sync.Mutex
	latest map[string]map[string]probe.Result // target -> location -> result
}

func NewBoard() *Board { return &Board{latest: map[string]map[string]probe.Result{}} }

// Update records a location's result (an older one than held is ignored).
func (b *Board) Update(r probe.Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m := b.latest[r.TargetID]
	if m == nil {
		m = map[string]probe.Result{}
		b.latest[r.TargetID] = m
	}
	if old, ok := m[r.Location]; ok && old.At.After(r.At) {
		return
	}
	m[r.Location] = r
}

// Fresh returns the target's results no older than maxAge, sorted by location.
func (b *Board) Fresh(target string, now time.Time, maxAge time.Duration) []probe.Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []probe.Result
	for _, r := range b.latest[target] {
		if now.Sub(r.At) <= maxAge {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Location < out[j].Location })
	return out
}

// Verdict is the combined result. Locations lists who agreed with it.
type Verdict struct {
	TargetID string
	At       time.Time
	Status   probe.Status
	Reason   string
	Voters   int // locations that counted
	Bad      int // of them, how many saw a problem
}

// Decide combines fresh results:
//   - locations with a client status (our key/quota there) don't vote;
//   - nobody voting: client (unknown) with the first client reason;
//   - a strict majority bad: the worst bad status, reason from the worst location;
//   - otherwise healthy (slow/stale if a majority is degraded but up), and a minority
//     failure is noted in the reason as a regional problem.
func Decide(target string, now time.Time, results []probe.Result) Verdict {
	v := Verdict{TargetID: target, At: now, Status: probe.StatusClient}
	var voters, bad, degraded []probe.Result
	for _, r := range results {
		if !r.Status.Counts() {
			if v.Reason == "" {
				v.Reason = r.Reason
			}
			continue
		}
		voters = append(voters, r)
		switch r.Status.Severity() {
		case 2:
			bad = append(bad, r)
		case 1:
			degraded = append(degraded, r)
		}
	}
	v.Voters, v.Bad = len(voters), len(bad)
	if len(voters) == 0 {
		if len(results) == 0 {
			v.Reason = "no recent checks"
		}
		return v
	}
	if 2*len(bad) > len(voters) {
		worst := bad[0]
		for _, r := range bad[1:] {
			if r.Status == probe.StatusDown && worst.Status != probe.StatusDown {
				worst = r
			}
		}
		v.Status, v.Reason = worst.Status, worst.Reason
		if len(voters) > 1 {
			v.Reason += " (" + locs(bad) + ")"
		}
		return v
	}
	v.Status, v.Reason = probe.StatusOK, ""
	if 2*(len(bad)+len(degraded)) > len(voters) && len(degraded) > 0 {
		v.Status, v.Reason = degraded[0].Status, degraded[0].Reason
	}
	if len(bad) > 0 {
		note := "failing only from " + locs(bad)
		if v.Reason != "" {
			v.Reason += "; " + note
		} else {
			v.Reason = note
		}
	}
	return v
}

func locs(rs []probe.Result) string {
	names := make([]string, len(rs))
	for i, r := range rs {
		names[i] = r.Location
	}
	return strings.Join(names, ", ")
}

// Result turns the verdict into a probe.Result for the incident tracker.
func (v Verdict) Result() probe.Result {
	return probe.Result{TargetID: v.TargetID, Location: "quorum", At: v.At, Status: v.Status, Reason: v.Reason}
}
