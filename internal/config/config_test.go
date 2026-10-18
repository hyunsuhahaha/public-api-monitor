package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestTargetsFileLoads(t *testing.T) {
	c, err := Load("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Targets) == 0 {
		t.Fatal("no targets")
	}
	t.Setenv("DATA_GO_KR_KEY", "k")
	t.Setenv("SEOUL_OPENAPI_KEY", "k")
	t.Setenv("SEOUL_SUBWAY_KEY", "k")
	for i := range c.Targets {
		if _, err := c.Targets[i].Render(time.Now()); err != nil {
			t.Errorf("%s: %v", c.Targets[i].ID, err)
		}
	}
}

func TestRender(t *testing.T) {
	c, err := Parse([]byte(`
targets:
  - id: a
    url: https://x/y?serviceKey={{.Key}}&d={{baseDate .Now 40}}&t={{baseTime .Now 40}}&m={{yearMonth (addMonths .Now -1)}}&s={{esc "서울"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_GO_KR_KEY", "a+b/c==")
	// 00:20 KST: the 00:00 observation is not out yet (40 min), so yesterday 23:00
	now := time.Date(2026, 10, 1, 0, 20, 0, 0, KST)
	u, err := c.Targets[0].Render(now)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://x/y?serviceKey=a%2Bb%2Fc%3D%3D&d=20260930&t=2300&m=202609&s=%EC%84%9C%EC%9A%B8"
	if u != want {
		t.Fatalf("got  %s\nwant %s", u, want)
	}
	if r := c.Targets[0].Redact(u); strings.Contains(r, "a%2Bb") || !strings.Contains(r, "REDACTED") {
		t.Fatalf("key not redacted: %s", r)
	}
}

func TestDefaultsAndValidation(t *testing.T) {
	c, err := Parse([]byte("targets:\n  - id: a\n    url: http://x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Targets[0].Interval.Duration != 5*time.Minute || c.Targets[0].KeyEnv != "DATA_GO_KR_KEY" {
		t.Fatalf("defaults: %+v", c.Targets[0])
	}
	for _, bad := range []string{
		"targets:\n  - id: a\n",
		"targets:\n  - id: a\n    url: x\n  - id: a\n    url: y\n",
		"targets:\n  - id: a\n    url: '{{.Nope'\n",
		"targets:\n  - id: a\n    url: x\n    freshness: {pattern: 'no group', layout: '2006', max_age: 1h}\n",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestHasKey(t *testing.T) {
	os.Unsetenv("PAMON_TEST_KEY")
	tg := Target{KeyEnv: "PAMON_TEST_KEY"}
	if tg.HasKey() {
		t.Fatal("unset key")
	}
	t.Setenv("PAMON_TEST_KEY", " k ")
	if !tg.HasKey() {
		t.Fatal("set key")
	}
}

func TestForecastIssueTimes(t *testing.T) {
	cases := []struct {
		at                    string
		vDate, vTime, midTmFc string
	}{
		{"2026-10-18 00:05", "20261017", "2300", "202610171800"},
		{"2026-10-18 02:09", "20261017", "2300", "202610171800"},
		{"2026-10-18 02:10", "20261018", "0200", "202610171800"},
		{"2026-10-18 06:30", "20261018", "0500", "202610180600"},
		{"2026-10-18 23:59", "20261018", "2300", "202610181800"},
	}
	for _, c := range cases {
		now, _ := time.ParseInLocation("2006-01-02 15:04", c.at, KST)
		d := funcs["vilageBaseDate"].(func(time.Time) string)(now)
		h := funcs["vilageBaseTime"].(func(time.Time) string)(now)
		m := funcs["midTmFc"].(func(time.Time) string)(now)
		if d != c.vDate || h != c.vTime || m != c.midTmFc {
			t.Errorf("%s: got %s %s %s, want %s %s %s", c.at, d, h, m, c.vDate, c.vTime, c.midTmFc)
		}
	}
}

func TestMaintenance(t *testing.T) {
	c, err := Parse([]byte(`
targets:
  - {id: a, provider: 기관A, url: "http://x"}
  - {id: b, provider: 기관B, url: "http://y"}
maintenance:
  - match: [기관A]
    start: 2026-10-20T02:00:00+09:00
    end: 2026-10-20T04:00:00+09:00
    note: 정기 점검
`))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 3, 0, 0, 0, KST)
	if c.MaintenanceAt(&c.Targets[0], at) == nil || c.MaintenanceAt(&c.Targets[1], at) != nil {
		t.Fatal("provider match")
	}
	if c.MaintenanceAt(&c.Targets[0], at.Add(time.Hour)) != nil {
		t.Fatal("end is exclusive")
	}
	for _, bad := range []string{
		"maintenance: [{match: [nope], start: 2026-10-20T02:00:00Z, end: 2026-10-20T03:00:00Z}]",
		"maintenance: [{match: [a], start: 2026-10-20T02:00:00Z, end: 2026-10-20T01:00:00Z}]",
	} {
		if _, err := Parse([]byte("targets:\n  - {id: a, url: \"http://x\"}\n" + bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
