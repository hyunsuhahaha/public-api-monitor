package probe

import (
	"slices"
	"sync"
	"time"
)

// Baseline learns each target's usual response time, so "slow" means slow for that API:
// an API that answers in 50 ms and suddenly takes 800 ms is degraded long before it
// crosses a fixed 3-second limit. The usual time is the median of the last Window healthy
// answers; the limit is min(slow_after, max(Factor×median, median+Margin)). A lasting
// change becomes the new normal once it fills half the window (about 12 hours at 5 min).
type Baseline struct {
	Window     int           // answers remembered per target
	MinSamples int           // no adaptive limit until this many
	Factor     float64       // limit is at least Factor × median ...
	Margin     time.Duration // ... and at least median + Margin

	mu      sync.Mutex
	samples map[string][]time.Duration // ring per target
	next    map[string]int
}

func NewBaseline() *Baseline {
	return &Baseline{Window: 288, MinSamples: 24, Factor: 4, Margin: 400 * time.Millisecond,
		samples: map[string][]time.Duration{}, next: map[string]int{}}
}

// Limit returns the adaptive slow limit for a target, capped at ceil; ok is false while
// there are too few samples.
func (b *Baseline) Limit(id string, ceil time.Duration) (limit, usual time.Duration, ok bool) {
	b.mu.Lock()
	s := slices.Clone(b.samples[id])
	b.mu.Unlock()
	if len(s) < b.MinSamples {
		return 0, 0, false
	}
	slices.Sort(s)
	usual = s[len(s)/2]
	limit = max(time.Duration(float64(usual)*b.Factor), usual+b.Margin)
	if ceil > 0 {
		limit = min(limit, ceil)
	}
	return limit, usual, true
}

// Add remembers one healthy answer's response time.
func (b *Baseline) Add(id string, d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.samples[id]
	if len(s) < b.Window {
		b.samples[id] = append(s, d)
		return
	}
	i := b.next[id]
	s[i] = d
	b.next[id] = (i + 1) % b.Window
}
