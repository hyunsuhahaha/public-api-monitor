package official

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchReadsStatuspageAndSlack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sp":
			w.Write([]byte(`{"status":{"indicator":"major","description":"Partial System Outage"}}`))
		case "/slack":
			w.Write([]byte(`{"status":"active","active_incidents":[{"title":"Messages delayed","type":"outage"}]}`))
		case "/broken":
			w.Write([]byte(`<html>`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	saved := Sources
	defer func() { Sources = saved }()
	Sources = []source{{provider: "A", api: srv.URL + "/sp"}, {provider: "S", api: srv.URL + "/slack", slack: true},
		{provider: "B", api: srv.URL + "/broken"}, {provider: "C", api: srv.URL + "/down"}}
	got := Fetch(context.Background(), srv.Client())
	if a := got["A"]; a.Indicator != "major" || a.Description != "Partial System Outage" || a.Operational() {
		t.Fatalf("statuspage: %+v", a)
	}
	if s := got["S"]; s.Indicator != "major" || s.Description != "Messages delayed" {
		t.Fatalf("slack: %+v", s)
	}
	if _, ok := got["B"]; ok {
		t.Fatal("an unreadable page must give no announcement, not a guess")
	}
	if _, ok := got["C"]; ok {
		t.Fatal("a failing page must give no announcement")
	}
}
