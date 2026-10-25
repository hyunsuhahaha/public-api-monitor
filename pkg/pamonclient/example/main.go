// example: call an API through a pamon-aware breaker and print what happened to each call.
//
//	go run ./pkg/pamonclient/example -target github-api -url https://api.github.com/zen
//
// Game day against the fake API (no real service is harmed):
//
//	go run ./cmd/fakeapi -addr :9000 -ban-after 30 &
//	curl -X POST 'localhost:9000/admin/mode?m=down'
//	go run ./pkg/pamonclient/example -naive -url 'http://localhost:9000/api' -n 200   # banned
//	curl -X DELETE localhost:9000/admin/ban
//	go run ./pkg/pamonclient/example -url 'http://localhost:9000/api' -n 200          # backs off
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/pkg/pamonclient"
)

func main() {
	var (
		base   = flag.String("pamon", pamonclient.DefaultBaseURL, "pamon base URL")
		target = flag.String("target", "fake", "pamon target id of the API (as in /s/{id}.html)")
		u      = flag.String("url", "", "the API URL to call")
		n      = flag.Int("n", 20, "calls to make")
		every  = flag.Duration("every", 100*time.Millisecond, "pause between calls")
		naive  = flag.Bool("naive", false, "retry each call until it works (up to 50 times), no pamon, no breaker")
	)
	flag.Parse()
	if *u == "" {
		flag.Usage()
		os.Exit(2)
	}
	hc := &http.Client{Timeout: 10 * time.Second}
	sent := 0
	call := func(ctx context.Context) error {
		sent++
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, *u, nil)
		if err != nil {
			return pamonclient.Permanent(err)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return err
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
			return fmt.Errorf("HTTP %d", resp.StatusCode)
		case resp.StatusCode >= 400:
			return pamonclient.Permanent(fmt.Errorf("HTTP %d", resp.StatusCode))
		}
		return nil
	}
	ctx := context.Background()
	b := pamonclient.New(*base).Breaker(*target, pamonclient.BreakerOptions{Cooldown: 10 * time.Second})
	for i := 1; i <= *n; i++ {
		var err error
		if *naive {
			for try := 0; try < 50; try++ {
				if err = call(ctx); err == nil {
					break
				}
			}
		} else {
			err = b.Do(ctx, call)
		}
		outcome := "ok"
		switch {
		case errors.Is(err, pamonclient.ErrMaintenance):
			outcome = "skipped: announced maintenance (" + err.Error() + ")"
		case errors.Is(err, pamonclient.ErrProviderDown):
			outcome = "skipped: pamon sees it failing, no retry"
		case errors.Is(err, pamonclient.ErrOpen):
			outcome = "stopped: breaker open"
		case err != nil:
			outcome = "failed: " + err.Error()
		}
		fmt.Printf("call %3d  requests sent so far %4d  %s\n", i, sent, outcome)
		time.Sleep(*every)
	}
}
