package probe

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// FollowUps answers the question a timeout leaves open: was the server slow, or hung?
// After a check times out, the same request is sent once more in the background with a
// longer budget (at most once per target per Every, so a long outage costs a handful of
// extra calls). An answer after 14 s says a longer timeout would have worked; nothing in
// 60 s says the server is stuck. The check itself is not delayed.
type FollowUps struct {
	Wait  time.Duration // how long the follow-up waits
	Every time.Duration // at most one follow-up per target this often
	// Done, if set, is told each result (metrics).
	Done func(target string, f FollowUp)

	mu   sync.Mutex
	last map[string]FollowUp
	busy map[string]bool
}

// FollowUp is the outcome of one follow-up request.
type FollowUp struct {
	At       time.Time `json:"at"`
	Answered bool      `json:"answered"`          // got a response within Wait
	AfterMs  int64     `json:"afterMs"`           // how long it took (or Wait, if no answer)
	HTTP     int       `json:"http,omitempty"`    // status of the late answer
	WaitMs   int64     `json:"waitMs"`            // the budget it had
	Timeout  int64     `json:"timeoutMs"`         // the check's own timeout, for comparison
	Stalled  string    `json:"stalled,omitempty"` // where it was stuck, if it never answered
}

func NewFollowUps() *FollowUps {
	return &FollowUps{Wait: 60 * time.Second, Every: 30 * time.Minute, last: map[string]FollowUp{}, busy: map[string]bool{}}
}

// Last returns the latest follow-up for a target, if any.
func (f *FollowUps) Last(id string) (FollowUp, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	x, ok := f.last[id]
	return x, ok
}

// start runs a follow-up for a timed-out check unless one ran recently or is running.
func (f *FollowUps) start(client *http.Client, id, url string, header http.Header, timeout time.Duration, now func() time.Time) {
	f.mu.Lock()
	if f.busy[id] || now().Sub(f.last[id].At) < f.Every {
		f.mu.Unlock()
		return
	}
	f.busy[id] = true
	f.mu.Unlock()
	go func() {
		start := now()
		ctx, cancel := context.WithTimeout(context.Background(), f.Wait)
		defer cancel()
		clock := &phaseClock{now: now}
		res := FollowUp{At: start, WaitMs: f.Wait.Milliseconds(), Timeout: timeout.Milliseconds()}
		req, err := http.NewRequestWithContext(withTrace(ctx, clock), http.MethodGet, url, nil)
		if err == nil {
			req.Header = header.Clone()
			var resp *http.Response
			if resp, err = client.Do(req); err == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, MaxBody))
				resp.Body.Close()
				res.Answered, res.HTTP = true, resp.StatusCode
			}
		}
		res.AfterMs = now().Sub(start).Milliseconds()
		if !res.Answered {
			res.Stalled = stalledText[clock.stalled()]
		}
		f.mu.Lock()
		f.last[id], f.busy[id] = res, false
		f.mu.Unlock()
		if f.Done != nil {
			f.Done(id, res)
		}
	}()
}
