package probe

import (
	"slices"
	"sync"
	"time"
)

// Baseline learns each target's usual response time, so "slow" means slow for that API:
// an API that answers in 50 ms and suddenly takes 800 ms is degraded long before it
// crosses a fixed 3-second limit. The usual time is the median of the last Window healthy
// answers; the limit is min(slow_after, max(Factor×median, median+Margin, Spread×p95)):
// the p95 term keeps an API whose answers always swing (40 ms to 1 s) from paging on its
// ordinary slow tail, while a steady API still gets a tight limit. The p95 is taken over
// answers that were not themselves judged slow: otherwise an hour of slowness (5% of the
// window) lifts the p95 and the monitor stops seeing the slowness it is in. A lasting
// change becomes the new normal once it fills half the window (about 12 hours at 5 min).
type Baseline struct {
	Window     int           // answers remembered per target
	MinSamples int           // no adaptive limit until this many
	Factor     float64       // limit is at least Factor × median ...
	Margin     time.Duration // ... and at least median + Margin
	Spread     float64       // ... and at least Spread × p95 of the window

	mu      sync.Mutex
	samples map[string][]sample // ring per target
	next    map[string]int
}

type sample struct {
	d    time.Duration
	slow bool // judged slow when it came in
}

func NewBaseline() *Baseline {
	return &Baseline{Window: 288, MinSamples: 24, Factor: 4, Margin: 400 * time.Millisecond, Spread: 1.5,
		samples: map[string][]sample{}, next: map[string]int{}}
}

// Limit returns the adaptive slow limit for a target, capped at ceil; ok is false while
// there are too few samples.
func (b *Baseline) Limit(id string, ceil time.Duration) (limit, usual time.Duration, ok bool) {
	b.mu.Lock()
	all := make([]time.Duration, 0, len(b.samples[id]))
	var normal []time.Duration
	for _, x := range b.samples[id] {
		all = append(all, x.d)
		if !x.slow {
			normal = append(normal, x.d)
		}
	}
	b.mu.Unlock()
	if len(all) < b.MinSamples {
		return 0, 0, false
	}
	slices.Sort(all)
	usual = all[len(all)/2]
	limit = max(time.Duration(float64(usual)*b.Factor), usual+b.Margin)
	if len(normal) > 0 {
		slices.Sort(normal)
		limit = max(limit, time.Duration(float64(normal[len(normal)*95/100])*b.Spread))
	}
	if ceil > 0 {
		limit = min(limit, ceil)
	}
	return limit, usual, true
}

// Add remembers one healthy answer's response time.
func (b *Baseline) Add(id string, d time.Duration) { b.Observe(id, d, false) }

// Observe remembers an answer's response time and whether it was judged slow (slow
// answers move the median, so a lasting change becomes normal, but not the p95 spread).
func (b *Baseline) Observe(id string, d time.Duration, slow bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	x := sample{d, slow}
	s := b.samples[id]
	if len(s) < b.Window {
		b.samples[id] = append(s, x)
		return
	}
	i := b.next[id]
	s[i] = x
	b.next[id] = (i + 1) % b.Window
}
