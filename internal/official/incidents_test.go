package official

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseStatuspageIncidents(t *testing.T) {
	body := `{"page":{"id":"x"},"incidents":[
	 {"id":"abc","name":"Degraded Actions","status":"resolved","impact":"minor","created_at":"2026-10-20T18:31:43.123Z","resolved_at":"2026-10-20T19:02:00.000Z","shortlink":"https://stspg.io/abc"},
	 {"id":"def","name":"API outage","status":"investigating","impact":"major","created_at":"2026-10-21T01:00:00-07:00","resolved_at":null,"shortlink":"https://stspg.io/def"}]}`
	h, ok := ParseStatuspageIncidents("GitHub", []byte(body))
	if !ok || len(h.Incidents) != 2 || h.Since.Year() != 1970 {
		t.Fatalf("%+v", h)
	}
	a, b := h.Incidents[0], h.Incidents[1]
	if a.Created != time.Date(2026, 10, 20, 18, 31, 43, 123e6, time.UTC) || a.Resolved.IsZero() || a.URL != "https://stspg.io/abc" ||
		!b.Resolved.IsZero() || !b.Created.Equal(time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)) || b.Impact != "major" {
		t.Fatalf("%+v %+v", a, b)
	}
	// a full page (50) covers only back to its oldest incident
	var items []string
	for i := 0; i < 50; i++ {
		items = append(items, fmt.Sprintf(`{"id":"i%d","name":"n","impact":"minor","created_at":"2026-10-%02dT00:00:00Z"}`, i, 1+i%28))
	}
	h, _ = ParseStatuspageIncidents("GitHub", []byte(`{"incidents":[`+strings.Join(items, ",")+`]}`))
	if !h.Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(h.Since)
	}
	for _, bad := range []string{`{"status":{}}`, `not json`, `[]`} {
		if _, ok := ParseStatuspageIncidents("GitHub", []byte(bad)); ok {
			t.Errorf("%s: parsed", bad)
		}
	}
}

func TestParseSlackHistory(t *testing.T) {
	body := `[{"id":1234,"date_created":"2026-10-20T10:12:00-07:00","date_updated":"2026-10-20T11:00:00-07:00","title":"Messages delayed","type":"incident","status":"resolved","url":"https://slack-status.com/2026-10/abc"},
	 {"id":1235,"date_created":"2026-10-22T09:00:00-07:00","date_updated":"2026-10-22T09:10:00-07:00","title":"Login issues","type":"outage","status":"active","url":"https://slack-status.com/2026-10/def"}]`
	h, ok := ParseSlackHistory("Slack", []byte(body))
	if !ok || len(h.Incidents) != 2 || h.Incidents[0].ID != "1234" || h.Incidents[0].Resolved.IsZero() || !h.Incidents[1].Resolved.IsZero() ||
		!h.Since.Equal(h.Incidents[0].Created) {
		t.Fatalf("%+v", h)
	}
	if _, ok := ParseSlackHistory("Slack", []byte(`{"status":"ok"}`)); ok {
		t.Fatal("not a history")
	}
}

func TestHistoryURL(t *testing.T) {
	for _, s := range Sources {
		u := s.historyURL()
		if !strings.HasPrefix(u, "https://") || !(strings.HasSuffix(u, "/api/v2/incidents.json") || strings.HasSuffix(u, "/history")) {
			t.Errorf("%s: %s", s.provider, u)
		}
	}
}
