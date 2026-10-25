package web

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func TestTargetFeeds(t *testing.T) {
	s := maintServer(t)
	ctx := context.Background()
	now := time.Now()
	for _, in := range []*incident.Incident{
		{TargetID: "air", Started: now.Add(-3 * time.Hour), Ended: now.Add(-2 * time.Hour), Severity: 1, Status: probe.StatusStale, Reason: "stale: data 6h old"},
		{TargetID: "b", Started: now.Add(-time.Hour), Severity: 2, Status: probe.StatusDown, Reason: "http 503"},
	} {
		if err := s.Store.SaveIncident(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	s.Store.SaveSchemaEvent(ctx, &store.SchemaEvent{Target: "air", At: now.Add(-30 * time.Minute), Kind: "removed", Paths: []string{"response.body.items.pm25Value"}})
	h := s.Handler()
	type feed struct {
		ID      string `xml:"id"`
		Entries []struct {
			Title   string `xml:"title"`
			Summary string `xml:"summary"`
			Link    struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
			Category struct {
				Term string `xml:"term,attr"`
			} `xml:"category"`
		} `xml:"entry"`
	}
	var one feed
	w := get(t, h, "/feed/air.xml")
	if err := xml.Unmarshal(w.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	var terms []string
	for _, e := range one.Entries {
		terms = append(terms, e.Category.Term)
	}
	// newest first: the schema change, the stale data, the window starting tomorrow sorts first
	if got := strings.Join(terms, ","); got != "maintenance,schema,data" || one.ID != "https://pamon.example/feed/air.xml" ||
		!strings.Contains(one.Entries[2].Title, "[데이터 이상] 에어코리아 대기오염 (복구") || one.Entries[1].Link.Href != "https://pamon.example/s/air.html" ||
		!strings.Contains(one.Entries[1].Summary, "pm25Value") {
		t.Fatalf("feed: %s", w.Body.String())
	}
	var all feed
	xml.Unmarshal(get(t, h, "/feed.xml").Body.Bytes(), &all)
	if len(all.Entries) != 5 { // 2 incidents, 1 schema change, 2 windows not over
		t.Fatalf("combined feed: %d entries", len(all.Entries))
	}
	for _, p := range []string{"/feed/nope.xml", "/feed/air"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	ans := get(t, h, "/s/air").Body.String()
	if !strings.Contains(ans, "이 API가 바뀌거나 이상해지면 알림 받기") || !strings.Contains(ans, `href="../feed/air.xml"`) || !strings.Contains(ans, `type="application/atom+xml"`) {
		t.Fatal("answer page lacks the feed")
	}
}
