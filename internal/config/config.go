// Package config loads the list of monitored APIs (targets.yaml).
package config

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// KST: every Korean public API takes its date parameters in Korean time.
var KST = time.FixedZone("KST", 9*60*60)

// Duration is a time.Duration written as "5m", "800ms" in YAML.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

// Freshness: the API answers but its data must also be recent. Pattern is a regexp whose
// first group captures a timestamp in the body, Layout its Go time layout (read in KST).
type Freshness struct {
	Pattern string   `yaml:"pattern"`
	Layout  string   `yaml:"layout"`
	MaxAge  Duration `yaml:"max_age"`

	Re *regexp.Regexp `yaml:"-"` // Pattern, compiled by Parse
}

// Target is one monitored API call.
type Target struct {
	ID        string     `yaml:"id"`
	Name      string     `yaml:"name"`
	Provider  string     `yaml:"provider"`
	Category  string     `yaml:"category"`
	Docs      string     `yaml:"docs"`
	URL       string     `yaml:"url"` // text/template, see Render
	KeyEnv    string     `yaml:"key_env"`
	Timeout   Duration   `yaml:"timeout"`
	SlowAfter Duration   `yaml:"slow_after"`
	Interval  Duration   `yaml:"interval"`
	Freshness *Freshness `yaml:"freshness"`
	SLO       float64    `yaml:"slo"` // availability objective, e.g. 0.999

	tmpl *template.Template
}

// Config is the whole file.
type Config struct {
	Interval  Duration `yaml:"interval"`
	Timeout   Duration `yaml:"timeout"`
	SlowAfter Duration `yaml:"slow_after"`
	KeyEnv    string   `yaml:"key_env"`
	SLO       float64  `yaml:"slo"` // default availability objective (0.99 if unset)
	Targets   []Target `yaml:"targets"`
}

// Load reads and validates a config file, filling per-target defaults.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(raw)
}

// Parse is Load without the file.
func Parse(raw []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if c.Interval.Duration == 0 {
		c.Interval.Duration = 5 * time.Minute
	}
	if c.Timeout.Duration == 0 {
		c.Timeout.Duration = 10 * time.Second
	}
	if c.SlowAfter.Duration == 0 {
		c.SlowAfter.Duration = 3 * time.Second
	}
	if c.KeyEnv == "" {
		c.KeyEnv = "DATA_GO_KR_KEY"
	}
	if c.SLO <= 0 || c.SLO >= 1 {
		c.SLO = 0.99 // public APIs: two nines is already a high bar for most of them
	}
	seen := map[string]bool{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if t.ID == "" || t.URL == "" {
			return nil, fmt.Errorf("target %d: id and url are required", i)
		}
		if seen[t.ID] {
			return nil, fmt.Errorf("target %q: duplicate id", t.ID)
		}
		seen[t.ID] = true
		if t.Name == "" {
			t.Name = t.ID
		}
		if t.KeyEnv == "" {
			t.KeyEnv = c.KeyEnv
		}
		if t.Timeout.Duration == 0 {
			t.Timeout = c.Timeout
		}
		if t.SlowAfter.Duration == 0 {
			t.SlowAfter = c.SlowAfter
		}
		if t.Interval.Duration == 0 {
			t.Interval = c.Interval
		}
		if t.SLO <= 0 || t.SLO >= 1 {
			t.SLO = c.SLO
		}
		tmpl, err := template.New(t.ID).Option("missingkey=error").Funcs(funcs).Parse(t.URL)
		if err != nil {
			return nil, fmt.Errorf("target %q url: %w", t.ID, err)
		}
		t.tmpl = tmpl
		if f := t.Freshness; f != nil {
			if f.Re, err = regexp.Compile(f.Pattern); err != nil || f.Re.NumSubexp() < 1 {
				return nil, fmt.Errorf("target %q freshness.pattern: needs one capture group (%v)", t.ID, err)
			}
			if f.Layout == "" || f.MaxAge.Duration == 0 {
				return nil, fmt.Errorf("target %q freshness: layout and max_age are required", t.ID)
			}
		}
	}
	return &c, nil
}

// Templates see .Key (the service key, query-escaped) and .Now (KST), plus helpers:
//
//	{{date .Now "20060102"}}           today
//	{{date (addHours .Now -1) "15"}}   last hour
//	{{baseTime .Now 40}}               the latest HH00 issued at least 40 minutes ago
//	{{yearMonth (addMonths .Now -1)}}  last month as YYYYMM
//	{{esc "서울"}}                      query-escape a literal
var funcs = template.FuncMap{
	"date":      func(t time.Time, layout string) string { return t.Format(layout) },
	"addHours":  func(t time.Time, h int) time.Time { return t.Add(time.Duration(h) * time.Hour) },
	"addDays":   func(t time.Time, d int) time.Time { return t.AddDate(0, 0, d) },
	"addMonths": func(t time.Time, m int) time.Time { return t.AddDate(0, m, 0) },
	"yearMonth": func(t time.Time) string { return t.Format("200601") },
	"esc":       url.QueryEscape,
	// baseTime: 기상청 초단기실황 is issued on the hour and available ~40 minutes later.
	"baseTime": func(t time.Time, delayMin int) string {
		return t.Add(-time.Duration(delayMin) * time.Minute).Format("1504")[:2] + "00"
	},
	"baseDate": func(t time.Time, delayMin int) string {
		return t.Add(-time.Duration(delayMin) * time.Minute).Format("20060102")
	},
	// 기상청 단기예보: issued at 02, 05, ..., 23 h, available ~10 minutes later.
	"vilageBaseDate": func(t time.Time) string { return vilageBase(t).Format("20060102") },
	"vilageBaseTime": func(t time.Time) string { return vilageBase(t).Format("1504") },
	// 기상청 중기예보 tmFc: 06:00 and 18:00 issues as YYYYMMDDHHMM.
	"midTmFc": func(t time.Time) string {
		switch {
		case t.Hour() >= 18:
			return t.Format("20060102") + "1800"
		case t.Hour() >= 6:
			return t.Format("20060102") + "0600"
		}
		return t.AddDate(0, 0, -1).Format("20060102") + "1800"
	},
}

func vilageBase(t time.Time) time.Time {
	t = t.Add(-10 * time.Minute)
	h := t.Hour()
	if h < 2 {
		y := t.AddDate(0, 0, -1)
		return time.Date(y.Year(), y.Month(), y.Day(), 23, 0, 0, 0, t.Location())
	}
	h -= (h - 2) % 3
	return time.Date(t.Year(), t.Month(), t.Day(), h, 0, 0, 0, t.Location())
}

// Render builds the request URL for now.
func (t *Target) Render(now time.Time) (string, error) {
	var b bytes.Buffer
	err := t.tmpl.Execute(&b, map[string]any{
		"Key": url.QueryEscape(strings.TrimSpace(os.Getenv(t.KeyEnv))),
		"Now": now.In(KST),
	})
	return b.String(), err
}

// HasKey reports whether the target's key variable is set.
func (t *Target) HasKey() bool { return strings.TrimSpace(os.Getenv(t.KeyEnv)) != "" }

// Redact hides the key in a rendered URL for logs and the public page.
func (t *Target) Redact(u string) string {
	k := url.QueryEscape(strings.TrimSpace(os.Getenv(t.KeyEnv)))
	if k == "" {
		return u
	}
	return strings.ReplaceAll(u, k, "REDACTED")
}
