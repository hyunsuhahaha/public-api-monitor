package insight

import "github.com/hyunsuhahaha/public-api-monitor/internal/store"

// Accuracy checks retryAdvice against what happened at the next check (about one
// interval later), from verdict transitions:
//   - wait (pamon saw a failure): right when the next check failed too, i.e. a retry
//     right away would have failed as well
//   - retry (pamon saw it working): right when the next check did not fail
//   - backoff (slow): right when the next check did not fail
//
// client and maint verdicts say nothing about the provider and are left out.
type Accuracy struct {
	Days        int     `json:"days"`
	Wait        int     `json:"waitChecks"`
	WaitRight   int     `json:"waitRight"`
	Retry       int     `json:"retryChecks"`
	RetryRight  int     `json:"retryRight"`
	Backoff     int     `json:"backoffChecks"`
	BackoffOK   int     `json:"backoffRight"`
	WaitPct     float64 `json:"waitRightPct"`  // -1 without samples
	RetryPct    float64 `json:"retryRightPct"` // -1 without samples
	BackoffPct  float64 `json:"backoffRightPct"`
	Recoveries  int     `json:"recoveries"` // wait followed by a working check
	FailedAfter int     `json:"failedAfterRetry"`
}

func failing(s string) bool { return s == "error" || s == "down" || s == "stale" }

func counts(s string) bool { return s != "client" && s != "maint" }

// AccuracyOf one target's (or several targets' merged) transitions.
func AccuracyOf(tr store.Transition, days int) Accuracy {
	a := Accuracy{Days: days}
	for from, nexts := range tr {
		if !counts(from) {
			continue
		}
		for next, n := range nexts {
			if !counts(next) {
				continue
			}
			switch {
			case failing(from):
				a.Wait += n
				if failing(next) {
					a.WaitRight += n
				} else {
					a.Recoveries += n
				}
			case from == "slow":
				a.Backoff += n
				if !failing(next) {
					a.BackoffOK += n
				}
			case from == "ok":
				a.Retry += n
				if !failing(next) {
					a.RetryRight += n
				} else {
					a.FailedAfter += n
				}
			}
		}
	}
	a.WaitPct, a.RetryPct, a.BackoffPct = share(a.WaitRight, a.Wait), share(a.RetryRight, a.Retry), share(a.BackoffOK, a.Backoff)
	return a
}

// Merge adds transitions of several targets.
func Merge(all map[string]store.Transition) store.Transition {
	out := store.Transition{}
	for _, tr := range all {
		for from, nexts := range tr {
			if out[from] == nil {
				out[from] = map[string]int{}
			}
			for next, n := range nexts {
				out[from][next] += n
			}
		}
	}
	return out
}

func share(a, b int) float64 {
	if b == 0 {
		return -1
	}
	return pct(a, b)
}
