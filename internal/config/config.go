// Package config loads the list of monitored APIs (targets.yaml).
package config

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
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

// Expect is a sanity rule on the values an API answers with: Pattern's first group
// captures a number (every match is checked), which must lie in [Min, Max]. An API that
// answers 200 with a temperature of 300 °C is not healthy. Values that don't look like a
// number (AirKorea's "-" for no reading) are not matched and so not judged.
type Expect struct {
	Name       string   `yaml:"name"`
	Pattern    string   `yaml:"pattern"`
	Min        *float64 `yaml:"min"`
	Max        *float64 `yaml:"max"`
	MinMatches int      `yaml:"min_matches"` // fewer matches than this is a failure too (default 1)

	Re *regexp.Regexp `yaml:"-"`
}

// Header keeps credentials in the environment, never in the exported target config.
type Header struct {
	Env    string `yaml:"env"`
	Prefix string `yaml:"prefix"`
}

// JSONExpect uses dot-separated object keys and array indexes, e.g. documents.0.x.
type JSONExpect struct {
	Path     string   `yaml:"path"`
	Equals   *string  `yaml:"equals"`
	Min      *float64 `yaml:"min"`
	Max      *float64 `yaml:"max"`
	MinItems *int     `yaml:"min_items"`
}

// Artifact pins a small immutable download; the full body must match SHA256.
type Artifact struct {
	SHA256   string `yaml:"sha256"`
	MinBytes int64  `yaml:"min_bytes"`
	MaxBytes int64  `yaml:"max_bytes"`
}

// Target is one monitored API call.
type Target struct {
	ID         string            `yaml:"id"`
	Name       string            `yaml:"name"`
	Provider   string            `yaml:"provider"`
	Category   string            `yaml:"category"`
	Docs       string            `yaml:"docs"`
	URL        string            `yaml:"url"` // text/template, see Render
	KeyEnv     string            `yaml:"key_env"`
	Auth       string            `yaml:"auth"` // key (legacy query key), headers, or none
	Headers    map[string]Header `yaml:"headers"`
	Classifier string            `yaml:"classifier"` // public (default), kakao, naver_maps, http
	JSONExpect []JSONExpect      `yaml:"json_expect"`
	Artifact   *Artifact         `yaml:"artifact"`
	Timeout    Duration          `yaml:"timeout"`
	SlowAfter  Duration          `yaml:"slow_after"`
	Interval   Duration          `yaml:"interval"`
	Freshness  *Freshness        `yaml:"freshness"`
	Expect     []Expect          `yaml:"expect"`
	SLO        float64           `yaml:"slo"` // availability objective, e.g. 0.999

	tmpl *template.Template
}

// Maintenance is an announced window (data.go.kr and the agencies post them as notices):
// verdicts inside it are kept but left out of uptime and SLOs, and open no incidents.
type Maintenance struct {
	Match []string  `yaml:"match"` // target ids or provider names
	Start time.Time `yaml:"start"` // RFC 3339, e.g. 2026-10-20T02:00:00+09:00
	End   time.Time `yaml:"end"`
	Note  string    `yaml:"note"`
}

// Covers reports whether the window applies to a target at a time.
func (m *Maintenance) Covers(t *Target, at time.Time) bool {
	if at.Before(m.Start) || !at.Before(m.End) {
		return false
	}
	for _, x := range m.Match {
		if x == t.ID || x == t.Provider {
			return true
		}
	}
	return false
}

// MaintenanceAt is the window covering a target at a time, or nil.
func (c *Config) MaintenanceAt(t *Target, at time.Time) *Maintenance {
	for i := range c.Maintenance {
		if c.Maintenance[i].Covers(t, at) {
			return &c.Maintenance[i]
		}
	}
	return nil
}

// Config is the whole file.
type Config struct {
	Interval  Duration `yaml:"interval"`
	Timeout   Duration `yaml:"timeout"`
	SlowAfter Duration `yaml:"slow_after"`
	KeyEnv    string   `yaml:"key_env"`
	SLO       float64  `yaml:"slo"` // default availability objective (0.99 if unset)
	Targets   []Target `yaml:"targets"`
	// SchemaWarmup: how long an API's response fields are learned before new ones count
	// as a change (default 24h: many APIs answer with different fields by time of day)
	SchemaWarmup Duration `yaml:"schema_warmup"`

	Maintenance []Maintenance `yaml:"maintenance"`
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
	if c.SchemaWarmup.Duration == 0 {
		c.SchemaWarmup.Duration = 24 * time.Hour
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
		if t.Auth == "" {
			t.Auth = "key"
		}
		if t.Auth != "key" && t.Auth != "headers" && t.Auth != "none" {
			return nil, fmt.Errorf("target %q: invalid auth", t.ID)
		}
		if t.Classifier == "" {
			t.Classifier = "public"
		}
		if t.Classifier != "public" && t.Classifier != "kakao" && t.Classifier != "naver_maps" && t.Classifier != "http" {
			return nil, fmt.Errorf("target %q: invalid classifier", t.ID)
		}
		if t.Auth == "headers" && len(t.Headers) == 0 {
			return nil, fmt.Errorf("target %q: headers auth needs headers", t.ID)
		}
		if a := t.Artifact; a != nil {
			digest, err := hex.DecodeString(a.SHA256)
			if err != nil || len(digest) != 32 || a.MinBytes < 1 || a.MaxBytes < a.MinBytes || a.MaxBytes > 1<<20 || t.Classifier != "http" || len(t.JSONExpect) > 0 || len(t.Expect) > 0 || t.Freshness != nil {
				return nil, fmt.Errorf("target %q: artifact needs HTTP, SHA256 and byte limits (1..1048576), without JSON/freshness rules", t.ID)
			}
			a.SHA256 = strings.ToLower(a.SHA256)
		}
		seenHeaders := map[string]bool{}
		for name, h := range t.Headers {
			canonical := http.CanonicalHeaderKey(name)
			if !validHeaderName(name) || seenHeaders[canonical] || h.Env == "" || invalidHeaderValue(h.Prefix) {
				return nil, fmt.Errorf("target %q: invalid or duplicate header", t.ID)
			}
			switch canonical {
			case "Host", "Connection", "Content-Length", "Transfer-Encoding":
				return nil, fmt.Errorf("target %q: unsupported header", t.ID)
			}
			seenHeaders[canonical] = true
		}
		for _, e := range t.JSONExpect {
			if e.Path == "" || strings.Contains(e.Path, "..") || strings.HasPrefix(e.Path, ".") || strings.HasSuffix(e.Path, ".") ||
				(e.Equals == nil && e.Min == nil && e.Max == nil && e.MinItems == nil) ||
				(e.Min != nil && e.Max != nil && *e.Min > *e.Max) || (e.MinItems != nil && *e.MinItems < 0) {
				return nil, fmt.Errorf("target %q: invalid json_expect", t.ID)
			}
			for _, bound := range []*float64{e.Min, e.Max} {
				if bound != nil && (math.IsNaN(*bound) || math.IsInf(*bound, 0)) {
					return nil, fmt.Errorf("target %q: non-finite json_expect bound", t.ID)
				}
			}
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
		for j := range t.Expect {
			e := &t.Expect[j]
			if e.Re, err = regexp.Compile(e.Pattern); err != nil || e.Re.NumSubexp() < 1 {
				return nil, fmt.Errorf("target %q expect %d: pattern needs one capture group (%v)", t.ID, j, err)
			}
			if e.Min == nil && e.Max == nil {
				return nil, fmt.Errorf("target %q expect %d: min or max is required", t.ID, j)
			}
			if e.Name == "" {
				e.Name = e.Pattern
			}
			if e.MinMatches == 0 {
				e.MinMatches = 1
			}
		}
		if f := t.Freshness; f != nil {
			if f.Re, err = regexp.Compile(f.Pattern); err != nil || f.Re.NumSubexp() < 1 {
				return nil, fmt.Errorf("target %q freshness.pattern: needs one capture group (%v)", t.ID, err)
			}
			if f.Layout == "" || f.MaxAge.Duration == 0 {
				return nil, fmt.Errorf("target %q freshness: layout and max_age are required", t.ID)
			}
		}
	}
	for i, m := range c.Maintenance {
		if !m.End.After(m.Start) || len(m.Match) == 0 {
			return nil, fmt.Errorf("maintenance %d: needs match and an end after its start", i)
		}
		for _, x := range m.Match {
			hit := false
			for j := range c.Targets {
				hit = hit || c.Targets[j].ID == x || c.Targets[j].Provider == x
			}
			if !hit {
				return nil, fmt.Errorf("maintenance %d: %q is no target id or provider", i, x)
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

// HasKey reports whether every required credential is set; none needs no key.
func (t *Target) HasKey() bool { return t.MissingCredential() == "" }

func (t *Target) MissingCredential() string {
	if t.Auth != "none" && t.Auth != "headers" && t.KeyEnv == "" {
		return "key_env"
	}
	if t.Auth != "none" && t.Auth != "headers" && strings.TrimSpace(os.Getenv(t.KeyEnv)) == "" {
		return t.KeyEnv
	}
	for _, h := range t.Headers {
		if strings.TrimSpace(os.Getenv(h.Env)) == "" {
			return h.Env
		}
	}
	return ""
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	return true
}

func invalidHeaderValue(value string) bool {
	return strings.IndexFunc(value, func(c rune) bool { return c < 32 || c == 127 }) >= 0
}

// ApplyHeaders never returns credential values in an error.
func (t *Target) ApplyHeaders(req *http.Request) error {
	if missing := t.MissingCredential(); missing != "" {
		return fmt.Errorf("credential not set (%s)", missing)
	}
	for name, h := range t.Headers {
		value := h.Prefix + strings.TrimSpace(os.Getenv(h.Env))
		if invalidHeaderValue(value) {
			return fmt.Errorf("invalid credential header (%s)", name)
		}
		req.Header.Set(name, value)
	}
	return nil
}

// Redact hides the key in a rendered URL for logs and the public page.
func (t *Target) Redact(u string) string {
	envs := []string{}
	if t.Auth != "headers" && t.Auth != "none" {
		envs = append(envs, t.KeyEnv)
	}
	for _, h := range t.Headers {
		envs = append(envs, h.Env)
	}
	secrets := []string{}
	for _, env := range envs {
		if k := strings.TrimSpace(os.Getenv(env)); k != "" {
			secrets = append(secrets, k, url.QueryEscape(k))
		}
	}
	// Redact longer overlapping credentials before their shorter prefixes.
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		u = strings.ReplaceAll(u, secret, "REDACTED")
	}
	return u
}
