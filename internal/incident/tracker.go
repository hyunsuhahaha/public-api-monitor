// Package incident opens and closes incidents from a stream of check results.
//
// One bad check is noise (a dropped packet, our own network): an incident opens only after
// OpenAfter bad checks in a row and closes after CloseAfter good ones. Client results (our
// key or quota) neither open nor close anything.
package incident

import (
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// Incident is an outage or degradation of one target.
type Incident struct {
	ID         int64        `json:"id"`
	TargetID   string       `json:"target"`
	Started    time.Time    `json:"started"`
	Ended      time.Time    `json:"ended"`    // zero (0001-01-01) while open
	Severity   int          `json:"severity"` // 1 degraded, 2 outage (worst seen)
	Status     probe.Status `json:"status"`
	Reason     string       `json:"reason"` // the first bad check's reason
	LastReason string       `json:"lastReason"`
}

// Open reports whether the incident is still going.
func (i *Incident) Open() bool { return i.Ended.IsZero() }

// Event is what a result changed.
type Event int

const (
	None Event = iota
	Opened
	Updated // still open, got worse or a new reason
	Closed
)

type state struct {
	bad, good int
	firstBad  probe.Result
	open      *Incident
}

// Tracker keeps per-target state. Safe for concurrent use.
type Tracker struct {
	OpenAfter  int
	CloseAfter int

	mu     sync.Mutex
	states map[string]*state
}

func NewTracker(openAfter, closeAfter int) *Tracker {
	return &Tracker{OpenAfter: openAfter, CloseAfter: closeAfter, states: map[string]*state{}}
}

// Restore puts an incident that was still open at the last shutdown back.
func (t *Tracker) Restore(in *Incident) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.get(in.TargetID).open = in
}

func (t *Tracker) get(id string) *state {
	s := t.states[id]
	if s == nil {
		s = &state{}
		t.states[id] = s
	}
	return s
}

// Observe feeds one result and returns what changed and the incident it concerns.
func (t *Tracker) Observe(r probe.Result) (Event, *Incident) {
	if !r.Status.Counts() {
		return None, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.get(r.TargetID)
	sev := r.Status.Severity()
	if sev == 0 {
		s.bad = 0
		s.good++
		if s.open != nil && s.good >= t.CloseAfter {
			in := s.open
			in.Ended = r.At
			s.open = nil
			return Closed, in
		}
		return None, nil
	}
	s.good = 0
	s.bad++
	if s.bad == 1 {
		s.firstBad = r
	}
	if s.open != nil {
		changed := r.Reason != s.open.LastReason
		s.open.LastReason = r.Reason
		if sev > s.open.Severity {
			s.open.Severity, s.open.Status = sev, r.Status
			changed = true
		}
		if changed {
			return Updated, s.open
		}
		return None, nil
	}
	if s.bad >= t.OpenAfter {
		worst := r
		if s.firstBad.Status.Severity() > sev {
			worst = s.firstBad
		}
		s.open = &Incident{
			TargetID:   r.TargetID,
			Started:    s.firstBad.At,
			Severity:   worst.Status.Severity(),
			Status:     worst.Status,
			Reason:     s.firstBad.Reason,
			LastReason: r.Reason,
		}
		return Opened, s.open
	}
	return None, nil
}
