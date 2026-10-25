// Package push sends a remote probe's results to the central instance.
//
// Results queue in memory and go out in batches; when the central is unreachable they
// wait (up to MaxQueue, oldest dropped first) and are retried with backoff, so a short
// central outage loses nothing.
package push

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

var (
	mQueue = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "pamon_push_queue", Help: "Results waiting to be sent to the central.",
	})
	mSent = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_push_sent_total", Help: "Results the central accepted.",
	})
	mErrors = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_push_errors_total", Help: "Failed pushes to the central.",
	})
	mDropped = promauto.NewCounter(prometheus.CounterOpts{
		Name: "pamon_push_dropped_total", Help: "Results dropped because the queue was full.",
	})
)

// Wire is a result as sent over HTTP (also what the central's ingest endpoint decodes).
type Wire struct {
	Target     string       `json:"target"`
	Location   string       `json:"location"`
	At         time.Time    `json:"at"`
	Status     probe.Status `json:"status"`
	LatencyMS  int64        `json:"latencyMs"`
	HTTPStatus int          `json:"httpStatus"`
	Code       string       `json:"code,omitempty"`
	Reason     string       `json:"reason,omitempty"`
}

func ToWire(r probe.Result) Wire {
	return Wire{r.TargetID, r.Location, r.At, r.Status, r.LatencyMS, r.HTTPStatus, r.Code, r.Reason}
}

func (w Wire) Result() probe.Result {
	return probe.Result{TargetID: w.Target, Location: w.Location, At: w.At, Status: w.Status,
		LatencyMS: w.LatencyMS, HTTPStatus: w.HTTPStatus, Code: w.Code, Reason: w.Reason}
}

type Pusher struct {
	URL      string // central base URL, e.g. https://status.example.com
	Token    string
	Client   *http.Client
	Log      *slog.Logger
	Every    time.Duration
	MaxQueue int

	mu    sync.Mutex
	queue []Wire
	kick  chan struct{}
}

func New(url, token string, log *slog.Logger) *Pusher {
	return &Pusher{
		URL: strings.TrimRight(url, "/"), Token: token, Log: log,
		Client: &http.Client{Timeout: 15 * time.Second}, Every: 10 * time.Second, MaxQueue: 5000,
		kick: make(chan struct{}, 1),
	}
}

// Record queues a result (monitor.Sink).
func (p *Pusher) Record(_ context.Context, _ *config.Target, r probe.Result) {
	p.mu.Lock()
	p.queue = append(p.queue, ToWire(r))
	if over := len(p.queue) - p.MaxQueue; over > 0 {
		p.queue = p.queue[over:]
		mDropped.Add(float64(over))
	}
	mQueue.Set(float64(len(p.queue)))
	p.mu.Unlock()
}

// Run sends the queue every Every until ctx ends, then tries one last flush.
func (p *Pusher) Run(ctx context.Context) {
	backoff := p.Every
	timer := time.NewTimer(p.Every)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			p.Flush(flush)
			cancel()
			return
		case <-timer.C:
		}
		if err := p.Flush(ctx); err != nil {
			mErrors.Inc()
			p.Log.Warn("push failed", "err", err, "retry_in", backoff)
			timer.Reset(backoff)
			backoff = min(backoff*2, 5*time.Minute)
			continue
		}
		backoff = p.Every
		timer.Reset(p.Every)
	}
}

// Flush sends everything queued in batches of up to 500.
func (p *Pusher) Flush(ctx context.Context) error {
	for {
		p.mu.Lock()
		n := min(len(p.queue), 500)
		batch := append([]Wire(nil), p.queue[:n]...)
		p.mu.Unlock()
		if n == 0 {
			return nil
		}
		sctx, span := otel.Tracer("github.com/hyunsuhahaha/public-api-monitor").Start(ctx, "push")
		err := p.send(sctx, batch)
		span.End()
		if err != nil {
			return err
		}
		p.mu.Lock()
		p.queue = p.queue[n:]
		mQueue.Set(float64(len(p.queue)))
		p.mu.Unlock()
		mSent.Add(float64(n))
	}
}

func (p *Pusher) send(ctx context.Context, batch []Wire) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL+"/api/v1/ingest", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header)) // the central's ingest span joins this trace
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		err := fmt.Errorf("central answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
		if resp.StatusCode == http.StatusBadRequest {
			// the central kept what it could and will never take the rest (e.g. a target
			// it doesn't know): move on rather than block everything behind it
			p.Log.Error("push partly rejected, not resending", "err", err, "results", len(batch))
			mDropped.Add(float64(len(batch)))
			return nil
		}
		return err
	}
	return nil
}
