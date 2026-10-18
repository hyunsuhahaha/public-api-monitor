// pamon: public API monitor. Checks Korean public APIs on a schedule, records results
// and incidents, and serves a status page.
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

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/monitor"
	"github.com/hyunsuhahaha/public-api-monitor/internal/notify"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
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
		dbPath   = flag.String("db", env("PAMON_DB", "pamon.db"), "SQLite database file")
		addr     = flag.String("addr", env("PAMON_ADDR", ":8080"), "HTTP listen address")
		location = flag.String("location", env("PAMON_LOCATION", "local"), "name of this probe location")
		once     = flag.Bool("once", false, "check every target once, print the results and exit")
		keep     = flag.Duration("keep", 100*24*time.Hour, "how long to keep raw checks")
	)
	flag.Parse()
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	prober := probe.New(*location)

	if *once {
		os.Exit(runOnce(cfg, prober))
	}

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("db", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := &monitor.Monitor{
		Cfg: cfg, Prober: prober, Store: st, Log: log,
		Tracker:     incident.NewTracker(2, 2),
		MaxParallel: 4,
	}
	if hook := os.Getenv("PAMON_WEBHOOK_URL"); hook != "" {
		m.Notifier = notify.NewWebhook(hook, os.Getenv("PAMON_PUBLIC_URL"), log)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&web.Server{Cfg: cfg, Store: st, LastCheck: m.LastCheck}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
	}
	go func() {
		log.Info("listening", "addr", *addr, "targets", len(cfg.Targets), "location", *location)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http", "err", err)
			stop()
		}
	}()
	go m.Prune(ctx, *keep)
	m.Run(ctx)

	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	log.Info("stopped")
}

// runOnce is for trying a targets file: one check each, a table on stdout, exit 1 if any
// target is not ok.
func runOnce(cfg *config.Config, p *probe.Prober) int {
	ctx := context.Background()
	code := 0
	for i := range cfg.Targets {
		t := &cfg.Targets[i]
		r := p.Check(ctx, t)
		line := string(r.Status) + "\t" + t.ID + "\t" + time.Duration(r.LatencyMS*int64(time.Millisecond)).String()
		if r.Code != "" {
			line += "\tcode=" + r.Code
		}
		if r.Reason != "" {
			line += "\t" + r.Reason
		}
		os.Stdout.WriteString(line + "\n")
		if r.Status != probe.StatusOK {
			code = 1
		}
	}
	return code
}
