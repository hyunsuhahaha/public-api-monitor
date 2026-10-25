package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/monitor"
	"github.com/hyunsuhahaha/public-api-monitor/internal/notify"
	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
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
	// providers' own status pages, read every beat so the lead-time comparison knows when
	// each one changed (and when pamon was not reading it)
	off := official.Fetch(ctx, prober.Client)
	recordOfficial(ctx, log, st, off, cfg.Interval.Duration)
	if exportDir == "" {
		return nil
	}
	srv := &web.Server{Cfg: cfg, Store: st, LastCheck: m.LastCheck, SlowLimit: prober.Baseline.Limit,
		Official:  off,
		PublicURL: os.Getenv("PAMON_PUBLIC_URL"), Version: version, StartedAt: started}
	start := time.Now()
	spent, err := exportSite(srv.Handler(), cfg, exportDir)
	attrs := []any{"took", time.Since(start).Round(time.Millisecond)}
	for _, k := range slices.Sorted(maps.Keys(spent)) {
		attrs = append(attrs, k, spent[k].Round(time.Millisecond))
	}
	log.Info("export done", attrs...)
	return err
}

func recordOfficial(ctx context.Context, log *slog.Logger, st *store.Store, off map[string]official.Status, every time.Duration) {
	for _, p := range slices.Sorted(maps.Keys(off)) {
		o := off[p]
		// a run continues across one missed beat, not across a longer hole
		if err := st.RecordOfficial(ctx, p, o.Indicator, o.Description, o.FetchedAt, 3*every); err != nil {
			log.Error("record official status", "provider", p, "err", err)
		}
	}
}

// exportKind groups an exported URL for the timing log: the path, plus the range for series.
func exportKind(u string) string {
	p, q, _ := strings.Cut(u, "?")
	switch {
	case strings.HasPrefix(p, "/api/v1/series"):
		v, _ := url.ParseQuery(q)
		k := "series_" + v.Get("range")
		if v.Get("shift") != "" {
			k += "_shift"
		}
		return k
	case strings.HasPrefix(p, "/api/v1/incidents/"):
		return "incident"
	case strings.HasPrefix(p, "/ai/targets/"):
		return "ai_target"
	case strings.HasPrefix(p, "/s/"):
		return "answer"
	case strings.HasPrefix(p, "/report/"):
		return "report"
	case p == "/":
		return "dashboard"
	}
	return strings.Trim(strings.ReplaceAll(p, "/", "_"), "_")
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

// decorate adds what only the static site carries: a canonical link to the
// address handed out (PAMON_SITE_URL), search-engine ownership tags
// (PAMON_GOOGLE_VERIFY, PAMON_NAVER_VERIFY) and the Cloudflare Web Analytics
// beacon (PAMON_CF_BEACON). The server's own pages keep their strict CSP.
func decorate(s, name string) string {
	token := regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
	var head string
	if base := siteURL(); base != "" {
		head += `<link rel="canonical" href="` + base + strings.TrimSuffix(name, "index.html") + `">` + "\n"
	}
	for _, m := range []struct{ meta, env string }{{"google-site-verification", "PAMON_GOOGLE_VERIFY"}, {"naver-site-verification", "PAMON_NAVER_VERIFY"}} {
		if v := os.Getenv(m.env); token.MatchString(v) {
			head += `<meta name="` + m.meta + `" content="` + v + `">` + "\n"
		}
	}
	if i := strings.Index(s, "</head>"); i >= 0 && head != "" {
		s = s[:i] + head + s[i:]
	}
	if tok := os.Getenv("PAMON_CF_BEACON"); regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(tok) {
		tag := `<script defer src="https://static.cloudflareinsights.com/beacon.min.js" data-cf-beacon='{"token": "` + tok + `"}'></script>` + "\n"
		if i := strings.LastIndex(s, "</body>"); i >= 0 {
			s = s[:i] + tag + s[i:]
		} else {
			s += tag
		}
	}
	return s
}

// siteURL is the public address with a trailing slash, or "" when unset.
func siteURL() string {
	u := os.Getenv("PAMON_SITE_URL")
	if !strings.HasPrefix(u, "https://") {
		return ""
	}
	return strings.TrimSuffix(u, "/") + "/"
}

// sitemap lists the pages a search engine should index.
func sitemap(base string, pages []string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	day := time.Now().UTC().Format("2006-01-02")
	for _, p := range pages {
		b.WriteString("  <url><loc>" + base + strings.TrimSuffix(p, "index.html") + "</loc><lastmod>" + day + "</lastmod></url>\n")
	}
	b.WriteString("</urlset>\n")
	return []byte(b.String())
}

// exportSite returns the time spent per kind of URL (exportKind), for the cycle log.
func exportSite(h http.Handler, cfg *config.Config, dir string) (map[string]time.Duration, error) {
	spent := map[string]time.Duration{}
	get := func(u string) ([]byte, error) {
		rec := httptest.NewRecorder()
		t0 := time.Now()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		spent[exportKind(u)] += time.Since(t0)
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
		return put(name, []byte(decorate(s, name)))
	}
	if err := page("/", "index.html"); err != nil {
		return spent, err
	}
	if err := page("/status", "status.html"); err != nil {
		return spent, err
	}
	// Keep agent-facing URLs identical on the live server and GitHub Pages.
	for _, path := range []string{"llms.txt", "ai/index.html", "ai/diagnostics.json"} {
		requestPath := "/" + path
		if path == "ai/index.html" {
			requestPath = "/ai/"
		}
		b, err := get(requestPath)
		if err != nil {
			return spent, err
		}
		if path == "ai/index.html" {
			b = []byte(decorate(string(b), path))
		}
		if err := put(path, b); err != nil {
			return spent, err
		}
	}
	for _, t := range cfg.Targets {
		path := "ai/targets/" + t.ID + ".json"
		b, err := get("/" + path)
		if err != nil {
			return spent, err
		}
		if err := put(path, b); err != nil {
			return spent, err
		}
	}
	for _, f := range []string{"app.css", "app.js", "ops.js", "status.js", "report.js", "vendor/uPlot.min.css", "vendor/uPlot.iife.min.js", "vendor/uPlot.LICENSE"} {
		b, err := get("/" + f)
		if err != nil {
			return spent, err
		}
		if strings.HasSuffix(f, ".js") {
			b = []byte(linkFixes.Replace(string(b)))
		}
		if err := put(f, b); err != nil {
			return spent, err
		}
	}
	indexed := []string{"index.html", "status.html", "ai/index.html", "llms.txt", "s/index.html"}
	// answer pages, the ones people land on from "is X down?"
	if err := page("/s/", "s/index.html"); err != nil {
		return spent, err
	}
	for _, t := range cfg.Targets {
		if err := page("/s/"+t.ID, "s/"+t.ID+".html"); err != nil {
			return spent, err
		}
		indexed = append(indexed, "s/"+t.ID+".html")
	}
	if err := page("/lead-time", "lead-time.html"); err != nil {
		return spent, err
	}
	indexed = append(indexed, "lead-time.html")
	month := time.Now().In(config.KST)
	for i, m := range []string{month.Format("2006-01"), month.AddDate(0, -1, 0).Format("2006-01")} {
		b, err := get("/report/" + m)
		if err != nil {
			continue // a month with no data yet
		}
		s := strings.ReplaceAll(linkFixes.Replace(string(b)), `href="/report/`, `href="../report/`)
		s = regexp.MustCompile(`href="\.\./report/(\d{4}-\d{2})"`).ReplaceAllString(s, `href="$1.html"`)
		s = strings.ReplaceAll(s, `href="./"`, `href="../"`)
		if err := put("report/"+m+".html", []byte(decorate(s, "report/"+m+".html"))); err != nil {
			return spent, err
		}
		indexed = append(indexed, "report/"+m+".html")
		if i == 0 {
			put("report.html", []byte(`<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="0; url=report/`+m+`.html">`))
		}
	}
	if b, err := get("/feed.xml"); err == nil {
		put("feed.xml", b)
	}
	urls := []string{"/api/v1/status", "/api/v1/incidents", "/api/v1/schema-events", "/api/v1/silences", "/api/v1/overview", "/api/v1/lead-time", "/version"}
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
			return spent, err
		}
		if err := put(dataFile(u), b); err != nil {
			return spent, err
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
	if base := siteURL(); base != "" {
		if err := put("sitemap.xml", sitemap(base, indexed)); err != nil {
			return spent, err
		}
	}
	return spent, put(".nojekyll", nil)
}
