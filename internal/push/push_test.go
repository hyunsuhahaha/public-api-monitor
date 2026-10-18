package push

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

func TestPushRetriesAndKeepsOrder(t *testing.T) {
	var mu sync.Mutex
	var got []Wire
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != "/api/v1/ingest" {
			http.Error(w, "no", 401)
			return
		}
		if fail {
			http.Error(w, "down", 503)
			return
		}
		var b []Wire
		json.NewDecoder(r.Body).Decode(&b)
		got = append(got, b...)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	p := New(srv.URL+"/", "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		p.Record(ctx, nil, probe.Result{TargetID: "a", Location: "home", At: time.Unix(int64(i), 0), Status: probe.StatusOK})
	}
	if err := p.Flush(ctx); err == nil {
		t.Fatal("central down: flush must fail and keep the queue")
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	if err := p.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 3 || got[0].At.Unix() != 0 || got[2].At.Unix() != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestQueueBound(t *testing.T) {
	p := New("http://127.0.0.1:1", "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.MaxQueue = 2
	for i := 0; i < 5; i++ {
		p.Record(context.Background(), nil, probe.Result{TargetID: "a", At: time.Unix(int64(i), 0)})
	}
	if len(p.queue) != 2 || p.queue[0].At.Unix() != 3 {
		t.Fatalf("oldest must be dropped: %+v", p.queue)
	}
}

func TestBadRequestBatchDropped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unknown target", 400)
	}))
	defer srv.Close()
	p := New(srv.URL, "tok", slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Record(context.Background(), nil, probe.Result{TargetID: "gone"})
	if err := p.Flush(context.Background()); err != nil || len(p.queue) != 0 {
		t.Fatalf("a rejected batch must not block the queue: %v %d", err, len(p.queue))
	}
}
