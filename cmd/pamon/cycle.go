package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/monitor"
	"github.com/hyunsuhahaha/public-api-monitor/internal/notify"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/quorum"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
	"github.com/hyunsuhahaha/public-api-monitor/internal/web"
)

// runCycle is one beat of a central driven by an outside scheduler (GitHub Actions cron):
// every target checked once into the database, incidents opened and closed as a server
// would, then optionally the pages written out as static files.
func runCycle(log *slog.Logger, cfg *config.Config, prober *probe.Prober, dbPath, location string, keep time.Duration, exportDir string) error {
	ctx := context.Background()
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	c := &monitor.Central{
		Cfg: cfg, Store: st, Log: log, Location: location,
		Board:   quorum.NewBoard(),
		Tracker: incident.NewTracker(2, 2),
	}
	if hook := os.Getenv("PAMON_WEBHOOK_URL"); hook != "" {
		c.Notifier = notify.NewWebhook(hook, os.Getenv("PAMON_PUBLIC_URL"), log)
	}
	// A server keeps each target's run of bad and good verdicts in memory; a process per
	// beat replays the last hour of verdicts first, so "2 bad in a row" spans two runs.
	replayVerdicts(ctx, st, cfg, c.Tracker)
	if err := c.Start(ctx); err != nil {
		return err
	}
	warmBaseline(ctx, st, cfg, prober, location)
	m := &monitor.Monitor{Cfg: cfg, Prober: prober, Sink: c, Log: log, MaxParallel: 4}
	started := time.Now()
	m.Round(ctx)
	log.Info("cycle done", "targets", len(cfg.Targets), "took", time.Since(started).Round(time.Millisecond))
	if n, err := st.Prune(ctx, time.Now().Add(-keep)); err == nil && n > 0 {
		log.Info("pruned old rows", "rows", n)
	}
	if exportDir == "" {
		return nil
	}
	srv := &web.Server{Cfg: cfg, Store: st, LastCheck: m.LastCheck, SlowLimit: prober.Baseline.Limit,
		PublicURL: os.Getenv("PAMON_PUBLIC_URL"), Version: version, StartedAt: started}
	return exportSite(srv.Handler(), cfg, exportDir)
}

func replayVerdicts(ctx context.Context, st *store.Store, cfg *config.Config, tr *incident.Tracker) {
	now := time.Now()
	for _, t := range cfg.Targets {
		vs, err := st.VerdictsBetween(ctx, t.ID, now.Add(-time.Hour), now, 100)
		if err != nil {
			continue
		}
		for _, v := range vs {
			tr.Observe(probe.Result{TargetID: v.TargetID, At: v.At, Status: v.Status, Reason: v.Reason})
		}
	}
}

// ranges and comparison shifts the dashboard asks for (app.js RANGES and its shift map)
var (
	exportRanges = []string{"1h", "6h", "24h", "7d", "30d"}
	exportShifts = map[string]string{"1h": "1d", "6h": "1d", "24h": "1d", "7d": "7d"}
	unsafeChars  = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// dataFile is where a GET of u is stored; the fetch shim in the pages maps the same way.
func dataFile(u string) string {
	p, q, _ := strings.Cut(strings.TrimPrefix(u, "/"), "?")
	if q != "" {
		p += "_" + unsafeChars.ReplaceAllString(q, "_")
	}
	return "data/" + p + ".json"
}

// fetchShim sends the pages' GETs of /api/... and /version to the exported files.
const fetchShim = `<script>/* static export: GETs read the snapshot files */
(() => { const f = window.fetch;
  window.fetch = (u, o) => {
    if (typeof u === "string" && u.startsWith("/") && (!o || !o.method || o.method === "GET")) {
      let [p, q] = u.slice(1).split("?");
      if (q) p += "_" + q.replace(/[^A-Za-z0-9._-]/g, "_");
      // Pages caches files for 10 minutes; a per-minute query keeps polls current
      u = "data/" + p + ".json?m=" + Math.floor(Date.now() / 60000);
    }
    return f(u, o);
  };
})();</script>
`

var linkFixes = strings.NewReplacer(
	`href="/"`, `href="./"`, `href="/status"`, `href="status.html"`, `href="/report"`, `href="report.html"`,
	`href="/feed.xml"`, `href="feed.xml"`, `href="/api/v1/overview"`, `href="data/api/v1/overview.json"`,
	`location.href = "/status"`, `location.href = "status.html"`, `location.href = "/report"`, `location.href = "report.html"`,
	`location.href = "/feed.xml"`, `location.href = "feed.xml"`, "${location.origin}/#i=", "${location.origin}${location.pathname}#i=",
)

func exportSite(h http.Handler, cfg *config.Config, dir string) error {
	get := func(u string) ([]byte, error) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		if rec.Code != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %d", u, rec.Code)
		}
		return rec.Body.Bytes(), nil
	}
	put := func(name string, b []byte) error {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, b, 0o644)
	}
	page := func(u, name string) error {
		b, err := get(u)
		if err != nil {
			return err
		}
		s := linkFixes.Replace(string(b))
		if i := strings.Index(s, "<script"); i >= 0 {
			s = s[:i] + fetchShim + s[i:]
		}
		return put(name, []byte(s))
	}
	if err := page("/", "index.html"); err != nil {
		return err
	}
	if err := page("/status", "status.html"); err != nil {
		return err
	}
	// Keep agent-facing URLs identical on the live server and GitHub Pages.
	for _, path := range []string{"llms.txt", "ai/index.html", "ai/diagnostics.json"} {
		requestPath := "/" + path
		if path == "ai/index.html" {
			requestPath = "/ai/"
		}
		b, err := get(requestPath)
		if err != nil {
			return err
		}
		if err := put(path, b); err != nil {
			return err
		}
	}
	for _, t := range cfg.Targets {
		path := "ai/targets/" + t.ID + ".json"
		b, err := get("/" + path)
		if err != nil {
			return err
		}
		if err := put(path, b); err != nil {
			return err
		}
	}
	for _, f := range []string{"app.css", "app.js", "ops.js", "status.js", "vendor/uPlot.min.css", "vendor/uPlot.iife.min.js", "vendor/uPlot.LICENSE"} {
		b, err := get("/" + f)
		if err != nil {
			return err
		}
		if strings.HasSuffix(f, ".js") {
			b = []byte(linkFixes.Replace(string(b)))
		}
		if err := put(f, b); err != nil {
			return err
		}
	}
	month := time.Now().In(config.KST)
	for i, m := range []string{month.Format("2006-01"), month.AddDate(0, -1, 0).Format("2006-01")} {
		b, err := get("/report/" + m)
		if err != nil {
			continue // a month with no data yet
		}
		s := strings.ReplaceAll(linkFixes.Replace(string(b)), `href="/report/`, `href="../report/`)
		s = regexp.MustCompile(`href="\.\./report/(\d{4}-\d{2})"`).ReplaceAllString(s, `href="$1.html"`)
		s = strings.ReplaceAll(s, `href="./"`, `href="../"`)
		if err := put("report/"+m+".html", []byte(s)); err != nil {
			return err
		}
		if i == 0 {
			put("report.html", []byte(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0; url=report/`+m+`.html">`))
		}
	}
	if b, err := get("/feed.xml"); err == nil {
		put("feed.xml", b)
	}
	urls := []string{"/api/v1/status", "/api/v1/incidents", "/api/v1/schema-events", "/api/v1/silences", "/api/v1/overview", "/version"}
	for _, r := range exportRanges {
		urls = append(urls, "/api/v1/overview?range="+r)
		for _, t := range cfg.Targets {
			urls = append(urls, "/api/v1/series?target="+t.ID+"&range="+r)
			if s := exportShifts[r]; s != "" {
				urls = append(urls, "/api/v1/series?target="+t.ID+"&range="+r+"&shift="+s)
			}
		}
	}
	for _, u := range urls {
		b, err := get(u)
		if err != nil {
			return err
		}
		if err := put(dataFile(u), b); err != nil {
			return err
		}
		if u == "/api/v1/incidents" {
			var list []struct {
				ID int64 `json:"id"`
			}
			if json.Unmarshal(b, &list) == nil {
				for i, in := range list {
					if i == 50 { // the drawer opens recent ones; older stay in the list
						break
					}
					d := fmt.Sprintf("/api/v1/incidents/%d", in.ID)
					if b, err := get(d); err == nil {
						put(dataFile(d), b)
					}
				}
			}
		}
	}
	return put(".nojekyll", nil)
}
