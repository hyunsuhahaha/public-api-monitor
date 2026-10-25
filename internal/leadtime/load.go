package leadtime

import (
	"context"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Report is the comparison over a period, as the pages and the monthly report show it.
type Report struct {
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Total     Stats     `json:"total"`
	Providers []Stats   `json:"providers"`
	Results   []Result  `json:"results"` // newest first
	// Watched: providers whose page pamon reads, and since when it has a record of each
	Watched map[string]time.Time `json:"watched"`
}

// Load compares the outages that overlap [from, to) of every target whose provider has
// a status page pamon reads.
func Load(ctx context.Context, st *store.Store, cfg *config.Config, from, to, now time.Time) (*Report, error) {
	incs, err := st.IncidentsOverlapping(ctx, from, to)
	if err != nil {
		return nil, err
	}
	runs, err := st.OfficialRuns(ctx, "", from.Add(-Before-MaxGap))
	if err != nil {
		return nil, err
	}
	targets := map[string]*config.Target{}
	for i := range cfg.Targets {
		targets[cfg.Targets[i].ID] = &cfg.Targets[i]
	}
	provider := func(id string) string {
		if t := targets[id]; t != nil && official.Has(t.Provider) {
			return t.Provider
		}
		return ""
	}
	confirm := func(id string) time.Duration {
		if t := targets[id]; t != nil && t.Interval.Duration > 0 {
			return t.Interval.Duration
		}
		return cfg.Interval.Duration
	}
	r := &Report{From: from, To: to, Watched: map[string]time.Time{}}
	r.Results, r.Providers, r.Total = ByProvider(Episodes(incs, provider, confirm), runs, now)
	if r.Results == nil {
		r.Results = []Result{}
	}
	if r.Providers == nil {
		r.Providers = []Stats{}
	}
	firsts, err := st.OfficialFirstSeen(ctx)
	if err != nil {
		return nil, err
	}
	for p, at := range firsts {
		if official.Has(p) {
			r.Watched[p] = at
		}
	}
	return r, nil
}

// For is the results that include a target, newest first.
func (r *Report) For(target string) []Result {
	var out []Result
	for _, x := range r.Results {
		if contains(x.Targets, target) {
			out = append(out, x)
		}
	}
	return out
}

// Provider is one provider's totals (zero Stats when it had no outage).
func (r *Report) Provider(name string) Stats {
	for _, s := range r.Providers {
		if s.Provider == name {
			return s
		}
	}
	return Stats{Provider: name}
}
