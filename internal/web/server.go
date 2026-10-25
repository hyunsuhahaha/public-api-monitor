// Package web serves the public status page, its JSON API, badges and health endpoints.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/push"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

//go:embed static
var static embed.FS

type Server struct {
	Cfg       *config.Config
	Store     *store.Store
	LastCheck func() time.Time
	// Ingest takes remote probes' results; nil or an empty IngestToken disables the endpoint.
	Ingest      func(ctx context.Context, results []probe.Result) error
	IngestToken string
	PublicURL   string // for absolute links in the feed (else taken from the request)
	// AdminToken, if set, lets operators add notes to incidents (Bearer token).
	AdminToken string
	// OnNote, if set, is told each stored note or ack (the webhook announces it).
	OnNote func(ctx context.Context, in *incident.Incident, n store.Note)
	// Version and StartedAt describe the running build (GET /version, the page footer).
	Version   string
	StartedAt time.Time
	// SlowLimit, if set, reports the learned slow limit of a target (probe.Baseline.Limit).
	SlowLimit func(id string, ceil time.Duration) (limit, usual time.Duration, ok bool)

	cache    respCache
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
	// the public page for people who use the APIs (the root is the operator's dashboard)
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) { http.ServeFileFS(w, r, sub, "status.html") })
	mux.HandleFunc("GET /api/v1/status", s.status)
	mux.HandleFunc("GET /ai/diagnostics.json", s.diagnostics)
	mux.HandleFunc("GET /ai/targets/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !strings.HasSuffix(id, ".json") {
			http.NotFound(w, r)
			return
		}
		r.SetPathValue("id", strings.TrimSuffix(id, ".json"))
		s.diagnostics(w, r)
	})
	mux.HandleFunc("GET /api/v1/incidents", s.incidents)
	mux.HandleFunc("POST /api/v1/ingest", s.ingest)
	mux.HandleFunc("GET /api/v1/overview", s.cache.wrap(s.overview))
	mux.HandleFunc("GET /api/v1/series", s.cache.wrap(s.series))
	mux.HandleFunc("GET /api/v1/schema-events", s.schemaEvents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", s.incidentDetail)
	mux.HandleFunc("POST /api/v1/incidents/{id}/notes", s.addNote)
	mux.HandleFunc("GET /api/v1/silences", s.listSilences)
	mux.HandleFunc("POST /api/v1/silences", s.addSilence)
	mux.HandleFunc("DELETE /api/v1/silences/{id}", s.endSilence)
	s.reportRoutes(mux)
	mux.HandleFunc("GET /badge/{id}", s.badge)
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": s.Version, "go": runtime.Version(), "startedAt": s.StartedAt,
			"uptimeSec": int64(time.Since(s.StartedAt).Seconds())})
	})
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

// ingest: POST a JSON array of push.Wire with "Authorization: Bearer <token>".
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	if s.Ingest == nil || s.IngestToken == "" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.IngestToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var batch []push.Wire
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&batch); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	results := make([]probe.Result, 0, len(batch))
	for _, b := range batch {
		if b.Location == "" || len(b.Location) > 40 || b.Location == "quorum" || !validStatus[b.Status] {
			http.Error(w, "bad location or status", http.StatusBadRequest)
			return
		}
		results = append(results, b.Result())
	}
	ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
	if err := s.Ingest(ctx, results); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var validStatus = map[probe.Status]bool{
	probe.StatusOK: true, probe.StatusSlow: true, probe.StatusStale: true,
	probe.StatusError: true, probe.StatusDown: true, probe.StatusClient: true,
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
	Locations   []LocationView     `json:"locations"`
}

// LocationView is one probe location's latest check of a target.
type LocationView struct {
	Location  string       `json:"location"`
	Status    probe.Status `json:"status"`
	Reason    string       `json:"reason,omitempty"`
	LatencyMs int64        `json:"latencyMs"`
	CheckedAt time.Time    `json:"checkedAt"`
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
	verdicts, err := s.Store.LatestVerdicts(ctx)
	if err != nil {
		return nil, err
	}
	byLoc, err := s.Store.LatestByLocation(ctx, now.Add(-time.Hour))
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
		if v, ok := verdicts[t.ID]; ok {
			at := v.At
			tv.Status, tv.Reason, tv.CheckedAt = v.Status, v.Reason, &at
		}
		tv.Locations = []LocationView{}
		for _, r := range byLoc[t.ID] {
			tv.Locations = append(tv.Locations, LocationView{r.Location, r.Status, r.Reason, r.LatencyMS, r.At})
			if r.Code != "" {
				tv.Code = r.Code
			}
			if tv.LatencyMs == 0 || r.LatencyMS < tv.LatencyMs {
				tv.LatencyMs = r.LatencyMS
			}
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
	sums, err := s.Store.NoteSummaries(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// each incident plus who took it and how many notes it has
	type row struct {
		*incident.Incident
		AckBy string     `json:"ackBy,omitempty"`
		AckAt *time.Time `json:"ackAt,omitempty"`
		Notes int        `json:"notes,omitempty"`
	}
	out := make([]row, 0, len(list))
	for _, in := range list {
		x := row{Incident: in}
		if sm, ok := sums[in.ID]; ok {
			x.AckBy, x.Notes = sm.AckBy, sm.Notes
			if sm.AckBy != "" {
				at := sm.AckAt
				x.AckAt = &at
			}
		}
		out = append(out, x)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(out)
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
