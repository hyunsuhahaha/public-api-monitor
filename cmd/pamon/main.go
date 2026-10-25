// pamon: public API monitor. Checks Korean public APIs on a schedule and serves a status
// page. Run one central (stores, combines locations, raises incidents, serves the page)
// and optionally remote probes in other places that push their results to it:
//
//	pamon -role central -location oci-chuncheon      (PAMON_INGEST_TOKEN to accept probes)
//	pamon -role probe -location home-seoul -central https://status.example.com
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/monitor"
	"github.com/hyunsuhahaha/public-api-monitor/internal/notify"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/push"
	"github.com/hyunsuhahaha/public-api-monitor/internal/quorum"
	"github.com/hyunsuhahaha/public-api-monitor/internal/report"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
	"github.com/hyunsuhahaha/public-api-monitor/internal/telemetry"
	"github.com/hyunsuhahaha/public-api-monitor/internal/web"
)

// version is the commit this binary was built from (-ldflags "-X main.version=..."):
// what is running is the first question after any deploy.
var version = "dev"

var startedAt = time.Now()

var mFollowUps = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "pamon_timeout_followups_total",
	Help: "Timed-out checks re-sent once with a longer wait: retried (answered at once), late (answered after the timeout) or hung (still nothing).",
}, []string{"target", "outcome"})

func init() {
	promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pamon_build_info", Help: "Always 1; the labels say which build is running.",
		ConstLabels: prometheus.Labels{"version": version, "goversion": runtime.Version()},
	}).Set(1)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	var (
		cfgPath  = flag.String("config", env("PAMON_CONFIG", "targets.yaml"), "targets file")
		role     = flag.String("role", env("PAMON_ROLE", "central"), "central or probe")
		dbPath   = flag.String("db", env("PAMON_DB", "pamon.db"), "SQLite database file (central)")
		addr     = flag.String("addr", env("PAMON_ADDR", ":8080"), "HTTP listen address(es), comma-separated")
		location = flag.String("location", env("PAMON_LOCATION", "local"), "name of this probe location")
		central  = flag.String("central", env("PAMON_CENTRAL_URL", ""), "central base URL (probe)")
		once     = flag.Bool("once", false, "check every target once, print the results and exit")
		month    = flag.String("report", "", "print the Markdown availability report for YYYY-MM from -db and exit")
		paths    = flag.Bool("paths", false, "with -once: fail only on endpoints that no longer exist (code 12, 404); for CI with a dummy key")
		dump     = flag.String("dump", "", "call one target once and print the start of its response body (to write freshness/expect rules)")
		check    = flag.Bool("check", false, "validate the config, print what it monitors and its maintenance windows, and exit")
		keep     = flag.Duration("keep", 100*24*time.Hour, "how long to keep raw checks (central)")
		cycle    = flag.Bool("cycle", false, "check every target once into -db (incidents included) and exit; for a cron job instead of a server")
		export   = flag.String("export", "", "with -cycle: write the dashboard and status page with their data as static files into this directory")
		snapshot = flag.String("snapshot", "", "check -db for corruption, write a consistent compacted copy to this file and exit (for backups)")
	)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("location", *location)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	if *dump != "" {
		os.Exit(dumpTarget(cfg, *dump))
	}
	if *check {
		printCheck(cfg, time.Now())
		return
	}
	prober := probe.New(*location)
	if *month != "" {
		os.Exit(printReport(cfg, *dbPath, *month))
	}
	if *snapshot != "" {
		if err := writeSnapshot(*dbPath, *snapshot); err != nil {
			log.Error("snapshot", "err", err)
			os.Exit(1)
		}
		return
	}
	if *once {
		os.Exit(runOnce(cfg, prober, *paths))
	}
	if *cycle {
		if err := runCycle(log, cfg, prober, *dbPath, *location, *keep, *export); err != nil {
			log.Error("cycle", "err", err)
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := telemetry.Setup(ctx, *role, *location)
	if err != nil {
		log.Error("tracing", "err", err)
		os.Exit(1)
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownTracing(flush)
	}()
	if telemetry.Enabled() {
		log.Info("tracing to OTLP", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	}

	switch *role {
	case "central":
		err = runCentral(ctx, log, cfg, prober, *dbPath, *addr, *location, *keep)
	case "probe":
		err = runProbe(ctx, log, cfg, prober, *addr, *central)
	default:
		err = errors.New("unknown -role " + *role)
	}
	if err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
	log.Info("stopped")
}

func runCentral(ctx context.Context, log *slog.Logger, cfg *config.Config, prober *probe.Prober, dbPath, addr, location string, keep time.Duration) error {
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
		// an agency's APIs failing within 30 s of each other are one page
		c.Notifier = notify.NewGrouper(notify.NewWebhook(hook, os.Getenv("PAMON_PUBLIC_URL"), log), 30*time.Second)
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	warmBaseline(ctx, st, cfg, prober, location)
	prober.FollowUps = probe.NewFollowUps()
	for _, t := range cfg.Targets { // keep the once-per-30-minutes limit across restarts
		if list, err := st.FollowUps(ctx, t.ID, time.Now().Add(-prober.FollowUps.Every)); err == nil && len(list) > 0 {
			prober.FollowUps.Remember(t.ID, list[0])
		}
	}
	prober.FollowUps.Done = func(id string, f probe.FollowUp) {
		mFollowUps.WithLabelValues(id, f.Outcome()).Inc()
		if err := st.SaveFollowUp(context.Background(), id, f); err != nil {
			log.Error("save follow-up", "err", err)
		}
		log.Info("timeout follow-up", "target", id, "answered", f.Answered, "after_ms", f.AfterMs, "stalled", f.Stalled)
	}
	m := &monitor.Monitor{Cfg: cfg, Prober: prober, Sink: c, Log: log, MaxParallel: 4}
	token := os.Getenv("PAMON_INGEST_TOKEN")
	srv := &http.Server{
		Addr: addr,
		Handler: (&web.Server{Cfg: cfg, Store: st, LastCheck: m.LastCheck,
			Ingest: c.Ingest, IngestToken: token, PublicURL: os.Getenv("PAMON_PUBLIC_URL"), AdminToken: os.Getenv("PAMON_ADMIN_TOKEN"),
			OnNote: func(ctx context.Context, in *incident.Incident, n store.Note) {
				nn, ok := c.Notifier.(interface {
					NotifyNote(context.Context, *incident.Incident, *config.Target, store.Note)
				})
				for i := range cfg.Targets {
					if ok && cfg.Targets[i].ID == in.TargetID {
						nn.NotifyNote(context.WithoutCancel(ctx), in, &cfg.Targets[i], n)
					}
				}
			},
			SlowLimit: prober.Baseline.Limit, Version: version, StartedAt: startedAt}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	serve(ctx, log, srv, "central", len(cfg.Targets), token != "")
	go c.Prune(ctx, keep)
	go c.Schema(ctx)
	m.Run(ctx)
	return shutdown(srv)
}

// warmBaseline: the adaptive slow limit picks up where it left off (this location's last
// 24 h). As live: answers past slow_after are not learned, slow-judged ones only move the median.
func warmBaseline(ctx context.Context, st *store.Store, cfg *config.Config, prober *probe.Prober, location string) {
	lat, err := st.LatencySamples(ctx, time.Now().Add(-24*time.Hour), location)
	if err != nil {
		return
	}
	for _, t := range cfg.Targets {
		for _, l := range lat[t.ID] {
			if d := time.Duration(l.Ms) * time.Millisecond; d <= t.SlowAfter.Duration {
				prober.Baseline.Observe(t.ID, d, l.Slow)
			}
		}
	}
}

func runProbe(ctx context.Context, log *slog.Logger, cfg *config.Config, prober *probe.Prober, addr, central string) error {
	token := os.Getenv("PAMON_INGEST_TOKEN")
	if central == "" || token == "" {
		return errors.New("probe needs -central (PAMON_CENTRAL_URL) and PAMON_INGEST_TOKEN")
	}
	p := push.New(central, token, log)
	m := &monitor.Monitor{Cfg: cfg, Prober: prober, Sink: p, Log: log, MaxParallel: 4}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.Handle("GET /metrics", promhttp.Handler())
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serve(ctx, log, srv, "probe", len(cfg.Targets), true)
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	m.Run(ctx)
	<-done
	return shutdown(srv)
}

// serve listens on every address in srv.Addr (comma-separated): on Windows "localhost"
// tries ::1 first, and a server on 127.0.0.1 alone cost each request ~200 ms of fallback,
// so the WSL runner listens on both loopbacks.
func serve(ctx context.Context, log *slog.Logger, srv *http.Server, role string, targets int, ingest bool) {
	for _, addr := range strings.Split(srv.Addr, ",") {
		ln, err := net.Listen("tcp", strings.TrimSpace(addr))
		if err != nil {
			log.Error("http", "addr", addr, "err", err)
			os.Exit(1)
		}
		log.Info("listening", "addr", ln.Addr().String(), "role", role, "targets", targets, "ingest", ingest)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("http", "err", err)
				os.Exit(1)
			}
		}()
	}
}

func shutdown(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}

// writeSnapshot copies dbPath to out through Store.Snapshot; a missing dbPath is an
// error rather than a new empty database.
func writeSnapshot(dbPath, out string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return err
	}
	os.Remove(out) // VACUUM INTO refuses an existing file
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	return st.Snapshot(context.Background(), out)
}

// printReport writes the month's report as Markdown (for a blog post or GeekNews).
func printReport(cfg *config.Config, dbPath, month string) int {
	st, err := store.Open(dbPath)
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		return 1
	}
	defer st.Close()
	rep, err := report.Build(context.Background(), st, cfg, month, time.Now())
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		return 1
	}
	os.Stdout.WriteString(rep.Markdown(os.Getenv("PAMON_PUBLIC_URL")))
	return 0
}

// runOnce is for trying a targets file: one check each, a table on stdout, exit 1 if any
// target is not ok. pathsOnly: exit 1 only for endpoints the gateway says are gone (a
// dummy key gets "not registered" from every live data.go.kr path, "no such service" from
// a retired or mistyped one).
func runOnce(cfg *config.Config, p *probe.Prober, pathsOnly bool) int {
	ctx := context.Background()
	code := 0
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		r := p.Check(ctx, t)
		line := string(r.Status) + "\t" + t.ID + "\t" + (time.Duration(r.LatencyMS) * time.Millisecond).String()
		if r.Code != "" {
			line += "\tcode=" + r.Code
		}
		if r.Reason != "" {
			line += "\t" + r.Reason
		}
		os.Stdout.WriteString(line + "\n")
		if (!pathsOnly && r.Status != probe.StatusOK) || (pathsOnly && endpointGone(t, r)) {
			code = 1
		}
	}
	return code
}

// endpointGone: the gateway says "no such service" (code 12) or the path answers 404. A
// target whose healthy answer is itself a 404 (expect_status: 404, the service's own "no
// such bucket") is gone only when the answer stops being that.
func endpointGone(t *config.Target, r probe.Result) bool {
	if r.Code == "12" {
		return true
	}
	if r.HTTPStatus != http.StatusNotFound {
		return false
	}
	return t.ExpectStatus != http.StatusNotFound || !r.Status.Healthy()
}

// printCheck: the config parsed (validation is in config.Parse); say what it monitors and
// which maintenance windows are on, coming or already over (stale entries to remove).
func printCheck(cfg *config.Config, now time.Time) {
	providers := map[string]bool{}
	for _, t := range cfg.Targets {
		providers[t.Provider] = true
	}
	fmt.Printf("ok: %d targets, %d providers, every %s, default SLO %.4g\n",
		len(cfg.Targets), len(providers), cfg.Interval.Duration, cfg.SLO)
	for _, m := range cfg.Maintenance {
		state := "upcoming"
		switch {
		case !now.Before(m.End):
			state = "over (remove it)"
		case !now.Before(m.Start):
			state = "ACTIVE"
		}
		fmt.Printf("maintenance %s ~ %s %v %q: %s\n", m.Start.In(config.KST).Format("2006-01-02 15:04"),
			m.End.In(config.KST).Format("2006-01-02 15:04"), m.Match, m.Note, state)
	}
}

// dumpTarget prints the first 3 KB of one target's answer. The URL is not printed: it
// carries the service key.
func dumpTarget(cfg *config.Config, id string) int {
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		if t.ID != id {
			continue
		}
		if t.Classifier == "docker_token" {
			r := probe.New("dump").Check(context.Background(), t)
			fmt.Printf("%s HTTP %d %s\nregistry token body hidden\n", r.Status, r.HTTPStatus, r.Reason)
			if !r.Status.Healthy() {
				return 1
			}
			return 0
		}
		u, err := t.Render(time.Now())
		if err != nil {
			fmt.Fprintln(os.Stderr, "url:", err)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), t.Timeout.Duration)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, "request: invalid URL")
			return 1
		}
		if err := t.ApplyHeaders(req); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		p := probe.New("dump")
		if t.Accept != "" {
			req.Header.Set("Accept", t.Accept)
		}
		if err := p.ApplyRegistryAuth(req, t.RegistryTokenURL); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		resp, err := p.Client.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "request:", t.Redact(err.Error()))
			return 1
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 3<<10))
		text := t.Redact(string(body))
		if token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "); token != "" {
			text = strings.ReplaceAll(text, token, "REDACTED")
		}
		fmt.Printf("HTTP %d %s\n%s\n", resp.StatusCode, resp.Header.Get("Content-Type"), text)
		return 0
	}
	fmt.Fprintln(os.Stderr, "no target", id)
	return 1
}
