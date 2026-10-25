package official

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Incident is one incident a provider posted on its status page. Created is when it was
// first posted: the announcement time pamon's detection is compared with (more exact
// than the 5-minute reads of the indicator, and available for the past).
type Incident struct {
	Provider string    `json:"provider"`
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Impact   string    `json:"impact"` // none, minor, major, critical (Slack: incident, outage, notice)
	Created  time.Time `json:"created"`
	Resolved time.Time `json:"resolved"` // zero while open
	URL      string    `json:"url,omitempty"`
}

// History is a provider's incident list as read once.
type History struct {
	Incidents []Incident
	// Since: the list holds every incident posted since then (Statuspage returns the
	// latest 50; when it returns fewer, the whole history). Older ones may be missing.
	Since time.Time
}

// historyURL: the incident list of a source.
func (s source) historyURL() string {
	if s.slack {
		return "https://slack-status.com/api/v2.0.0/history"
	}
	return strings.TrimSuffix(s.api, "/status.json") + "/incidents.json"
}

// FetchHistory reads every source's incident list concurrently; a source that fails or
// answers in an unknown shape is left out (the comparison then falls back to the
// indicator readings).
func FetchHistory(ctx context.Context, client *http.Client) map[string]History {
	out := map[string]History{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range Sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, ok := get(ctx, client, src.historyURL())
			if !ok {
				return
			}
			var h History
			if src.slack {
				h, ok = ParseSlackHistory(src.provider, body)
			} else {
				h, ok = ParseStatuspageIncidents(src.provider, body)
			}
			if ok {
				mu.Lock()
				out[src.provider] = h
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

func get(ctx context.Context, client *http.Client, u string) ([]byte, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "pamon (+https://pamon.hyunsuhahaha.workers.dev/)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return b, err == nil
}

// statuspageLimit is how many incidents /api/v2/incidents.json returns at most.
const statuspageLimit = 50

// ParseStatuspageIncidents reads Statuspage's /api/v2/incidents.json.
func ParseStatuspageIncidents(provider string, body []byte) (History, bool) {
	var v struct {
		Incidents []struct {
			ID         string     `json:"id"`
			Name       string     `json:"name"`
			Impact     string     `json:"impact"`
			CreatedAt  time.Time  `json:"created_at"`
			ResolvedAt *time.Time `json:"resolved_at"`
			Shortlink  string     `json:"shortlink"`
		} `json:"incidents"`
	}
	if json.Unmarshal(body, &v) != nil || v.Incidents == nil {
		return History{}, false
	}
	h := History{}
	for _, x := range v.Incidents {
		if x.ID == "" || x.CreatedAt.IsZero() {
			continue
		}
		in := Incident{Provider: provider, ID: x.ID, Name: x.Name, Impact: x.Impact, Created: x.CreatedAt, URL: x.Shortlink}
		if x.ResolvedAt != nil {
			in.Resolved = *x.ResolvedAt
		}
		h.Incidents = append(h.Incidents, in)
	}
	h.Since = since(h.Incidents, len(v.Incidents) >= statuspageLimit)
	return h, true
}

// ParseSlackHistory reads Slack's /api/v2.0.0/history.
func ParseSlackHistory(provider string, body []byte) (History, bool) {
	var v []struct {
		ID          json.Number `json:"id"`
		Title       string      `json:"title"`
		Type        string      `json:"type"`
		Status      string      `json:"status"`
		DateCreated time.Time   `json:"date_created"`
		DateUpdated time.Time   `json:"date_updated"`
		URL         string      `json:"url"`
	}
	if json.Unmarshal(body, &v) != nil || v == nil {
		return History{}, false
	}
	h := History{}
	for _, x := range v {
		if x.ID == "" || x.DateCreated.IsZero() {
			continue
		}
		in := Incident{Provider: provider, ID: x.ID.String(), Name: x.Title, Impact: x.Type, Created: x.DateCreated, URL: x.URL}
		if x.Status == "resolved" || x.Status == "completed" {
			in.Resolved = x.DateUpdated
		}
		h.Incidents = append(h.Incidents, in)
	}
	// Slack's history is not documented to be capped: count it from its oldest entry
	h.Since = since(h.Incidents, true)
	return h, true
}

// since: a full page (capped) covers only back to its oldest entry; a short one covers
// the whole history.
func since(list []Incident, capped bool) time.Time {
	if !capped {
		return time.Unix(0, 0)
	}
	oldest := time.Now() // an empty capped list vouches for nothing before now
	for _, in := range list {
		if in.Created.Before(oldest) {
			oldest = in.Created
		}
	}
	return oldest
}
