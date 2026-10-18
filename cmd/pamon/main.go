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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	"github.com/hyunsuhahaha/public-api-monitor/internal/web"
)

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
		addr     = flag.String("addr", env("PAMON_ADDR", ":8080"), "HTTP listen address")
		location = flag.String("location", env("PAMON_LOCATION", "local"), "name of this probe location")
		central  = flag.String("central", env("PAMON_CENTRAL_URL", ""), "central base URL (probe)")
		once     = flag.Bool("once", false, "check every target once, print the results and exit")
		month    = flag.String("report", "", "print the Markdown availability report for YYYY-MM from -db and exit")
		paths    = flag.Bool("paths", false, "with -once: fail only on endpoints that no longer exist (code 12, 404); for CI with a dummy key")
		keep     = flag.Duration("keep", 100*24*time.Hour, "how long to keep raw checks (central)")
	)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).With("location", *location)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	prober := probe.New(*location)
	if *month != "" {
		os.Exit(printReport(cfg, *dbPath, *month))
	}
	if *once {
		os.Exit(runOnce(cfg, prober, *paths))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		c.Notifier = notify.NewWebhook(hook, os.Getenv("PAMON_PUBLIC_URL"), log)
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	// the adaptive slow limit picks up where it left off (this location's last 24 h)
	if lat, err := st.Latencies(ctx, time.Now().Add(-24*time.Hour), location); err == nil {
		for id, ls := range lat {
			for _, l := range ls {
				prober.Baseline.Add(id, time.Duration(l)*time.Millisecond)
			}
		}
	}
	m := &monitor.Monitor{Cfg: cfg, Prober: prober, Sink: c, Log: log, MaxParallel: 4}
	token := os.Getenv("PAMON_INGEST_TOKEN")
	srv := &http.Server{
		Addr: addr,
		Handler: (&web.Server{Cfg: cfg, Store: st, LastCheck: m.LastCheck,
			Ingest: c.Ingest, IngestToken: token, PublicURL: os.Getenv("PAMON_PUBLIC_URL")}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	serve(ctx, log, srv, "central", len(cfg.Targets), token != "")
	go c.Prune(ctx, keep)
	go c.Schema(ctx)
	m.Run(ctx)
	return shutdown(srv)
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

func serve(ctx context.Context, log *slog.Logger, srv *http.Server, role string, targets int, ingest bool) {
	go func() {
		log.Info("listening", "addr", srv.Addr, "role", role, "targets", targets, "ingest", ingest)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http", "err", err)
			os.Exit(1)
		}
	}()
}

func shutdown(srv *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
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
		gone := r.Code == "12" || r.HTTPStatus == http.StatusNotFound
		if (!pathsOnly && r.Status != probe.StatusOK) || (pathsOnly && gone) {
			code = 1
		}
	}
	return code
}
