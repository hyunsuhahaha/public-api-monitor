package main

import (
	"strings"
	"testing"
)

// dataFile and the pages' fetch shim must name a URL's snapshot file the same way.
func TestDataFileMatchesShim(t *testing.T) {
	for u, want := range map[string]string{
		"/api/v1/status": "data/api/v1/status.json",
		"/version":       "data/version.json",
		"/api/v1/series?target=kma-typhoon&range=24h&shift=1d": "data/api/v1/series_target_kma-typhoon_range_24h_shift_1d.json",
		"/api/v1/incidents/42":                                 "data/api/v1/incidents/42.json",
	} {
		if got := dataFile(u); got != want {
			t.Errorf("dataFile(%q) = %q, want %q", u, got, want)
		}
	}
	if !strings.Contains(fetchShim, `q.replace(/[^A-Za-z0-9._-]/g, "_")`) {
		t.Error("the shim's escaping no longer matches unsafeChars")
	}
}
