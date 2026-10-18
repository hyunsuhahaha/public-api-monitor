// Package web serves the public status page, its JSON API, badges and health endpoints.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

//go:embed static
var static embed.FS

type Server struct {
	Cfg       *config.Config
	Store     *store.Store
	LastCheck func() time.Time

	mu       sync.Mutex
	cached   []byte
	cachedAt time.Time
}

// CacheFor: the status JSON is rebuilt at most this often (the page polls it).
const CacheFor = 30 * time.Second

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /", http.FileServerFS(sub))
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /api/v1/incidents", s.incidents)
	mux.HandleFunc("GET /badge/{id}", s.badge)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("GET /metrics", promhttp.Handler())
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}

// ready: the database answers and a check finished within two intervals.
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		http.Error(w, "db: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	if age := time.Since(s.LastCheck()); age > 2*s.Cfg.Interval.Duration {
		http.Error(w, "no check for "+age.Round(time.Second).String(), http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready"))
}

// TargetView is one API on the status page.
type TargetView struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Provider    string             `json:"provider"`
	Category    string             `json:"category"`
	Docs        string             `json:"docs,omitempty"`
	Status      probe.Status       `json:"status"`
	Reason      string             `json:"reason,omitempty"`
	Code        string             `json:"code,omitempty"`
	CheckedAt   *time.Time         `json:"checkedAt,omitempty"`
	LatencyMs   int64              `json:"latencyMs"`
	P50Ms       int64              `json:"p50Ms"`
	P95Ms       int64              `json:"p95Ms"`
	UptimeToday float64            `json:"uptimeToday"` // -1: no data
	Uptime30d   float64            `json:"uptime30d"`
	Uptime90d   float64            `json:"uptime90d"`
	Days        []store.Day        `json:"days"`
	Incident    *incident.Incident `json:"incident,omitempty"`
}

type StatusView struct {
	GeneratedAt time.Time      `json:"generatedAt"`
	Interval    string         `json:"interval"`
	Summary     map[string]int `json:"summary"`
	Targets     []TargetView   `json:"targets"`
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached == nil || time.Since(s.cachedAt) > CacheFor {
		v, err := s.build(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.cached, _ = json.Marshal(v)
		s.cachedAt = time.Now()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write(s.cached)
}

func (s *Server) build(ctx context.Context) (*StatusView, error) {
	now := time.Now()
	latest, err := s.Store.Latest(ctx)
	if err != nil {
		return nil, err
	}
	days, err := s.Store.Days(ctx, now.AddDate(0, 0, -90))
	if err != nil {
		return nil, err
	}
	lat, err := s.Store.Latencies(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	open, err := s.Store.OpenIncidents(ctx)
	if err != nil {
		return nil, err
	}
	openBy := map[string]*incident.Incident{}
	for _, in := range open {
		openBy[in.TargetID] = in
	}
	v := &StatusView{GeneratedAt: now, Interval: s.Cfg.Interval.String(), Summary: map[string]int{}}
	today := now.In(config.KST).Format("2006-01-02")
	for _, t := range s.Cfg.Targets {
		tv := TargetView{ID: t.ID, Name: t.Name, Provider: t.Provider, Category: t.Category, Docs: t.Docs, Status: "unknown"}
		if r, ok := latest[t.ID]; ok {
			at := r.At
			tv.Status, tv.Reason, tv.Code, tv.CheckedAt, tv.LatencyMs = r.Status, r.Reason, r.Code, &at, r.LatencyMS
		}
		tv.Days = days[t.ID]
		tv.UptimeToday = uptimeSince(tv.Days, today, 1)
		tv.Uptime30d = uptimeSince(tv.Days, today, 30)
		tv.Uptime90d = uptimeSince(tv.Days, today, 90)
		tv.P50Ms, tv.P95Ms = percentile(lat[t.ID], 0.5), percentile(lat[t.ID], 0.95)
		tv.Incident = openBy[t.ID]
		v.Summary[string(tv.Status)]++
		v.Targets = append(v.Targets, tv)
	}
	return v, nil
}

// uptimeSince sums the last n KST days (today counts as one).
func uptimeSince(days []store.Day, today string, n int) float64 {
	t, _ := time.ParseInLocation("2006-01-02", today, config.KST)
	from := t.AddDate(0, 0, -(n - 1)).Format("2006-01-02")
	var c, h int
	for _, d := range days {
		if d.Date >= from {
			c += d.Counted
			h += d.Healthy
		}
	}
	if c == 0 {
		return -1
	}
	return float64(h) / float64(c)
}

func percentile(v []int64, p float64) int64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]int64(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(p*float64(len(s)-1) + 0.5)
	return s[i]
}

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("target")
	list, err := s.Store.Incidents(r.Context(), target, time.Now().AddDate(0, 0, -90), 200)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []*incident.Incident{}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(list)
}

// badge: a shields-style SVG with the target's 30-day uptime, coloured by current status.
func (s *Server) badge(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".svg")
	var found bool
	for _, t := range s.Cfg.Targets {
		if t.ID == id {
			found = true
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	if s.cached == nil || time.Since(s.cachedAt) > CacheFor {
		if v, err := s.build(r.Context()); err == nil {
			s.cached, _ = json.Marshal(v)
			s.cachedAt = time.Now()
		}
	}
	var v StatusView
	json.Unmarshal(s.cached, &v)
	s.mu.Unlock()
	value, colour := "unknown", "#9f9f9f"
	for _, t := range v.Targets {
		if t.ID != id {
			continue
		}
		if t.Uptime30d >= 0 {
			value = fmt.Sprintf("%.2f%%", t.Uptime30d*100)
		}
		switch t.Status.Severity() {
		case 0:
			if t.Status == probe.StatusOK {
				colour = "#3fb950"
			}
		case 1:
			colour = "#d29922"
		case 2:
			colour = "#f85149"
		}
	}
	label := "uptime 30d"
	lw, vw := 7*len(label)+10, 7*len(value)+10
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">`+
		`<rect width="%d" height="20" fill="#555"/><rect x="%d" width="%d" height="20" fill="%s"/>`+
		`<g fill="#fff" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11" text-anchor="middle">`+
		`<text x="%d" y="14">%s</text><text x="%d" y="14">%s</text></g></svg>`,
		lw+vw, label, value, lw, lw, vw, colour, lw/2, label, lw+vw/2, value)
}
