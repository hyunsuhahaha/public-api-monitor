package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

func TestDiagnosticFreshnessAndScope(t *testing.T) {
	now := time.Now().UTC()
	tg := config.Target{ID: "map", URL: "https://user:secret@example.com/private?key=secret", Interval: config.Duration{Duration: 5 * time.Minute}}
	for _, tc := range []struct {
		status probe.Status
		age    time.Duration
		want   string
	}{
		{probe.StatusOK, time.Minute, "no_failure_observed"},
		{probe.StatusOK, 10 * time.Minute, "unknown"},
		{probe.StatusDown, time.Minute, "failure_observed"},
		{probe.StatusClient, time.Minute, "monitor_configuration_issue"},
		{probe.StatusStale, time.Minute, "failure_observed"},
		{probe.StatusSlow, time.Minute, "degraded_observed"},
		{probe.StatusOK, -time.Minute, "unknown"},
	} {
		at := now.Add(-tc.age)
		d := diagnosticTarget(tg, TargetView{Status: tc.status, CheckedAt: &at}, now)
		if d.Assessment != tc.want {
			t.Fatalf("%s age %s: %+v", tc.status, tc.age, d)
		}
		b, _ := json.Marshal(d)
		if d.Host != "example.com" || strings.Contains(string(b), "secret") || strings.Contains(string(b), "private?") {
			t.Fatalf("exposed URL: %s", b)
		}
	}
	if d := diagnosticTarget(tg, TargetView{Status: probe.StatusOK}, now); d.Assessment != "unknown" || d.FreshUntil != nil {
		t.Fatalf("missing check: %+v", d)
	}
}

func TestDiagnosticRoutesAndEvidence(t *testing.T) {
	_, h := setup(t)
	for _, base := range []string{"https://example.test/", "https://example.test/public-api-monitor/"} {
		for _, path := range []string{"ai/diagnostics.json", "ai/targets/a.json"} {
			w := get(t, h, "/"+path)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var v DiagnosticView
			if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
				t.Fatal(err)
			}
			if v.SchemaVersion != "1.0" || len(v.Targets) == 0 {
				t.Fatalf("bad response: %+v", v)
			}
			if len(v.Targets[0].RecentIncidents) != 1 {
				t.Fatalf("missing recovered incident: %+v", v.Targets[0])
			}
			u, _ := url.Parse(base + path)
			e, _ := url.Parse(v.Targets[0].EvidenceURL)
			if got := u.ResolveReference(e).String(); got != base+"#t=a" {
				t.Fatal(got)
			}
			g, _ := url.Parse(v.Guide)
			if got := u.ResolveReference(g).String(); got != base+"ai/index.html" {
				t.Fatal(got)
			}
			j, _ := url.Parse(v.Targets[0].JSONURL)
			if got := u.ResolveReference(j).String(); got != base+"ai/targets/a.json" {
				t.Fatal(got)
			}
		}
	}
	for _, path := range []string{"/ai/targets/missing.json", "/ai/targets/a"} {
		if w := get(t, h, path); w.Code != 404 {
			t.Fatal(path, w.Code)
		}
	}
	for _, path := range []string{"/llms.txt", "/ai/"} {
		if w := get(t, h, path); w.Code != 200 {
			t.Fatal(path, w.Code)
		}
	}
}
