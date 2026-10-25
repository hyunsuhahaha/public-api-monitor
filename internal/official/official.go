// Package official reads what providers announce on their own status pages, to show
// next to what pamon observed: "we see failures, the provider still says operational"
// is the gap a reader most needs to know about.
package official

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

// Status is one provider's announcement.
type Status struct {
	Provider    string    `json:"provider"`
	Page        string    `json:"page"`        // the human status page
	Indicator   string    `json:"indicator"`   // none, minor, major, critical (Statuspage terms)
	Description string    `json:"description"` // as the provider wrote it
	FetchedAt   time.Time `json:"fetchedAt"`
}

// Operational: the provider announces no problem.
func (s Status) Operational() bool { return s.Indicator == "none" }

type source struct {
	provider, api, page string
	slack               bool // Slack's own format instead of Statuspage
}

// Sources are the providers pamon checks that publish a machine-readable status.
var Sources = []source{
	{provider: "GitHub", api: "https://www.githubstatus.com/api/v2/status.json", page: "https://www.githubstatus.com"},
	{provider: "Discord", api: "https://discordstatus.com/api/v2/status.json", page: "https://discordstatus.com"},
	{provider: "npm", api: "https://status.npmjs.org/api/v2/status.json", page: "https://status.npmjs.org"},
	{provider: "PyPI", api: "https://status.python.org/api/v2/status.json", page: "https://status.python.org"},
	{provider: "Docker Hub", api: "https://www.dockerstatus.com/api/v2/status.json", page: "https://www.dockerstatus.com"},
	{provider: "Slack", api: "https://slack-status.com/api/v2.0.0/current", page: "https://slack-status.com", slack: true},
}

// Fetch reads every source concurrently; a source that fails is left out (the page then
// shows no announcement rather than a wrong one).
func Fetch(ctx context.Context, client *http.Client) map[string]Status {
	out := map[string]Status{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, src := range Sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, ok := fetchOne(ctx, client, src)
			if ok {
				mu.Lock()
				out[src.provider] = st
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return out
}

func fetchOne(ctx context.Context, client *http.Client, src source) (Status, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.api, nil)
	if err != nil {
		return Status{}, false
	}
	req.Header.Set("User-Agent", "pamon (+https://pamon.hyunsuhahaha.workers.dev/)")
	resp, err := client.Do(req)
	if err != nil {
		return Status{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Status{}, false
	}
	st := Status{Provider: src.provider, Page: src.page, FetchedAt: time.Now()}
	if src.slack {
		var v struct {
			Status    string `json:"status"`
			Incidents []struct {
				Title string `json:"title"`
				Type  string `json:"type"`
			} `json:"active_incidents"`
		}
		if json.Unmarshal(body, &v) != nil || v.Status == "" {
			return Status{}, false
		}
		st.Indicator, st.Description = "none", "All Systems Operational"
		if v.Status != "ok" || len(v.Incidents) > 0 {
			st.Indicator, st.Description = "minor", v.Status
			if len(v.Incidents) > 0 {
				st.Description = v.Incidents[0].Title
				if v.Incidents[0].Type == "outage" {
					st.Indicator = "major"
				}
			}
		}
		return st, true
	}
	var v struct {
		Status struct {
			Indicator   string `json:"indicator"`
			Description string `json:"description"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &v) != nil || v.Status.Indicator == "" {
		return Status{}, false
	}
	st.Indicator, st.Description = v.Status.Indicator, v.Status.Description
	return st, true
}
