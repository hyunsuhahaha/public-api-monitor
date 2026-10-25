// Package leadtime compares when pamon confirmed an outage with when the provider's own
// status page first admitted a problem: the measured version of "independent monitoring
// is faster than the official page".
//
// The numbers are only as good as what was watched, so every case says why it counts or
// not. An outage counts as "announced" only when pamon saw the official page operational
// shortly before it changed, and as "never announced" only when pamon read the page the
// whole time around the outage. Everything else (pamon was not reading the page, the
// outage is still going, the page was already reporting another problem) is kept out of
// the averages and shown as such.
package leadtime

import (
	"sort"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Kind is how one outage compares with the official page.
type Kind string

const (
	Announced  Kind = "announced"         // the page changed within the window: LeadMinutes says who was first
	Never      Kind = "never_announced"   // the page stayed operational the whole window
	Pending    Kind = "pending"           // still going, or ended too recently to say "never"
	AlreadyOn  Kind = "already_announced" // the page was reporting a problem since well before: not counted
	NoCoverage Kind = "no_coverage"       // pamon was not reading the page around then: not counted
)

const (
	// Before and After bound the window an announcement is matched in, around the
	// outage: an announcement up to an hour before pamon (it saw it first) or up to an
	// hour after the outage ended (a late post) is the same event.
	Before = 60 * time.Minute
	After  = 60 * time.Minute
	// MaxGap: two reads of a page further apart than this leave a hole in the record
	// (pamon reads every 5 minutes).
	MaxGap = 15 * time.Minute
	// Join: outages of one provider starting within this of the previous one's end are
	// one episode (three GitHub APIs failing together are one outage, one announcement).
	Join = 30 * time.Minute
)

// Episode is one outage of a provider: one or more incidents of its targets.
type Episode struct {
	Provider  string    `json:"provider"`
	Targets   []string  `json:"targets"`
	Incidents []int64   `json:"incidents"`
	Started   time.Time `json:"started"` // first failed check
	Confirmed time.Time `json:"confirmed"`
	Ended     time.Time `json:"ended"` // zero while open
}

// Result is an episode compared with the official page.
type Result struct {
	Episode
	Kind Kind `json:"kind"`
	// LeadMinutes (Announced only): official announcement minus pamon's confirmation.
	// Positive: pamon confirmed it that many minutes before the page admitted it.
	LeadMinutes int        `json:"leadMinutes"`
	AnnouncedAt *time.Time `json:"announcedAt,omitempty"`
	Official    string     `json:"official,omitempty"` // the page's description or incident title
	OfficialURL string     `json:"officialUrl,omitempty"`
	// Source: what the comparison rests on: "incidents" (the provider's posted incident
	// list, exact to the minute) or "indicator" (pamon's 5-minute reads of the page).
	Source string `json:"source,omitempty"`
}

// Episodes groups outage incidents (severity 2) by provider. confirm says how long after
// an incident's first failed check pamon confirmed it (the second failure in a row: one
// check interval); the lead is measured from there, not from the first failure, so it is
// the time a reader of pamon would really have had.
func Episodes(incs []*incident.Incident, provider func(target string) string, confirm func(target string) time.Duration) []Episode {
	sorted := append([]*incident.Incident(nil), incs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Started.Before(sorted[j].Started) })
	open := map[string]*Episode{}
	var out []*Episode
	for _, in := range sorted {
		if in.Severity < 2 {
			continue
		}
		p := provider(in.TargetID)
		if p == "" {
			continue
		}
		conf := in.Started.Add(confirm(in.TargetID))
		if e := open[p]; e != nil && (e.Ended.IsZero() || !in.Started.After(e.Ended.Add(Join))) {
			e.Incidents = append(e.Incidents, in.ID)
			if !contains(e.Targets, in.TargetID) {
				e.Targets = append(e.Targets, in.TargetID)
			}
			if conf.Before(e.Confirmed) {
				e.Confirmed = conf
			}
			if in.Ended.IsZero() || (!e.Ended.IsZero() && in.Ended.After(e.Ended)) {
				e.Ended = in.Ended
			}
			continue
		}
		e := &Episode{Provider: p, Targets: []string{in.TargetID}, Incidents: []int64{in.ID}, Started: in.Started, Confirmed: conf, Ended: in.Ended}
		open[p] = e
		out = append(out, e)
	}
	res := make([]Episode, len(out))
	for i, e := range out {
		res[i] = *e
	}
	return res
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// segments merges a provider's runs (oldest first) into stretches pamon watched without
// a hole longer than MaxGap.
func segments(runs []store.OfficialRun) [][2]time.Time {
	var out [][2]time.Time
	for _, r := range runs {
		if n := len(out); n > 0 && !r.Since.After(out[n-1][1].Add(MaxGap)) {
			if r.Until.After(out[n-1][1]) {
				out[n-1][1] = r.Until
			}
			continue
		}
		out = append(out, [2]time.Time{r.Since, r.Until})
	}
	return out
}

func covered(segs [][2]time.Time, from, to time.Time) bool {
	for _, s := range segs {
		if !s[0].After(from) && !s[1].Before(to) {
			return true
		}
	}
	return false
}

// CompareIncidents matches an episode with the incidents its provider posted (oldest
// first). ok is false when the stored list does not reach back over the window: the
// caller then falls back to the indicator readings (Compare).
func CompareIncidents(e Episode, incs []store.OfficialIncident, hist store.OfficialHistory, now time.Time) (Result, bool) {
	r := Result{Episode: e, Source: "incidents"}
	end := e.Ended
	if end.IsZero() || end.After(now) {
		end = now
	}
	from, to := e.Confirmed.Add(-Before), end.Add(After)
	if hist.Fetched.IsZero() || hist.Since.After(from) {
		return r, false
	}
	for _, in := range incs {
		if in.Created.After(to) {
			break
		}
		if in.Created.Before(from) {
			// posted before the window and still open in it (one left open for days is
			// a forgotten post, not a running announcement)
			if (in.Resolved.IsZero() || !in.Resolved.Before(from)) && in.Created.After(from.Add(-24*time.Hour)) {
				r.Kind = AlreadyOn
				return r, true
			}
			continue
		}
		at := in.Created
		r.Kind, r.AnnouncedAt, r.Official, r.OfficialURL = Announced, &at, in.Name, in.URL
		r.LeadMinutes = int(at.Sub(e.Confirmed).Round(time.Minute).Minutes())
		return r, true
	}
	if e.Ended.IsZero() || to.After(now) {
		r.Kind = Pending
		return r, true
	}
	if hist.Fetched.Before(to) {
		return r, false // not read since the window closed
	}
	r.Kind = Never
	return r, true
}

// Compare matches an episode with its provider's runs (oldest first) at a time.
func Compare(e Episode, runs []store.OfficialRun, now time.Time) Result {
	r := Result{Episode: e, Source: "indicator"}
	end := e.Ended
	if end.IsZero() || end.After(now) {
		end = now
	}
	from, to := e.Confirmed.Add(-Before), end.Add(After)
	segs := segments(runs)
	for i, run := range runs {
		if run.Operational() || run.Until.Before(from) || run.Since.After(to) {
			continue
		}
		// the first run in the window that admits a problem
		if run.Since.Before(from) || (i > 0 && !runs[i-1].Operational() && !run.Since.After(runs[i-1].Until.Add(MaxGap))) {
			r.Kind = AlreadyOn // reporting something since before the window
			return r
		}
		// the change is only dated if the page was read as operational just before it
		start := e.Confirmed
		if run.Since.Before(start) {
			start = run.Since
		}
		if i == 0 || !covered(segs, start.Add(-time.Second), run.Since) {
			r.Kind = NoCoverage
			return r
		}
		at := run.Since
		r.Kind, r.AnnouncedAt, r.Official = Announced, &at, run.Description
		r.LeadMinutes = int(at.Sub(e.Confirmed).Round(time.Minute).Minutes())
		return r
	}
	if e.Ended.IsZero() || to.After(now) {
		r.Kind = Pending
		return r
	}
	if !covered(segs, from, to) {
		r.Kind = NoCoverage
		return r
	}
	r.Kind = Never
	return r
}

// Stats sums up results: the averages are over Announced only, and every count is shown
// next to them so a reader can judge the sample.
type Stats struct {
	Provider      string  `json:"provider,omitempty"`
	Episodes      int     `json:"episodes"`  // all outages of providers with a status page
	Announced     int     `json:"announced"` // compared: the page changed in the window
	PamonFirst    int     `json:"pamonFirst"`
	Tie           int     `json:"tie"` // within 5 minutes, pamon's check interval
	OfficialFirst int     `json:"officialFirst"`
	Never         int     `json:"neverAnnounced"`
	Excluded      int     `json:"excluded"` // pending, already announced or not watched
	MeanMinutes   float64 `json:"meanLeadMinutes"`
	MedianMinutes float64 `json:"medianLeadMinutes"`
}

// Counted is how many outages the comparison really rests on.
func (s Stats) Counted() int { return s.Announced + s.Never }

// Summarize totals results.
func Summarize(provider string, rs []Result) Stats {
	s := Stats{Provider: provider, Episodes: len(rs)}
	var leads []int
	for _, r := range rs {
		switch r.Kind {
		case Announced:
			s.Announced++
			leads = append(leads, r.LeadMinutes)
			switch {
			case r.LeadMinutes >= 5:
				s.PamonFirst++
			case r.LeadMinutes <= -5:
				s.OfficialFirst++
			default:
				s.Tie++
			}
		case Never:
			s.Never++
		default:
			s.Excluded++
		}
	}
	if len(leads) > 0 {
		sum := 0
		for _, l := range leads {
			sum += l
		}
		s.MeanMinutes = float64(sum) / float64(len(leads))
		sort.Ints(leads)
		n := len(leads)
		if n%2 == 1 {
			s.MedianMinutes = float64(leads[n/2])
		} else {
			s.MedianMinutes = float64(leads[n/2-1]+leads[n/2]) / 2
		}
	}
	return s
}

// ByProvider compares every episode and totals per provider (most compared first) and
// overall. runs and incs are every provider's, oldest first per provider.
func ByProvider(eps []Episode, runs []store.OfficialRun, incs []store.OfficialIncident, hist map[string]store.OfficialHistory, now time.Time) (results []Result, providers []Stats, total Stats) {
	byP := map[string][]store.OfficialRun{}
	for _, r := range runs {
		byP[r.Provider] = append(byP[r.Provider], r)
	}
	incBy := map[string][]store.OfficialIncident{}
	for _, in := range incs {
		incBy[in.Provider] = append(incBy[in.Provider], in)
	}
	per := map[string][]Result{}
	for _, e := range eps {
		// the posted incidents when their list reaches back that far, else the readings
		r, ok := CompareIncidents(e, incBy[e.Provider], hist[e.Provider], now)
		if !ok {
			r = Compare(e, byP[e.Provider], now)
		}
		results = append(results, r)
		per[e.Provider] = append(per[e.Provider], r)
	}
	for p, rs := range per {
		providers = append(providers, Summarize(p, rs))
	}
	sort.Slice(providers, func(i, j int) bool {
		a, b := providers[i], providers[j]
		if a.Counted() != b.Counted() {
			return a.Counted() > b.Counted()
		}
		return a.Provider < b.Provider
	})
	sort.Slice(results, func(i, j int) bool { return results[i].Started.After(results[j].Started) })
	return results, providers, Summarize("", results)
}
