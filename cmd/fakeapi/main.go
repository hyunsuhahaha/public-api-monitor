// fakeapi: a stand-in for a data.go.kr API whose failure mode can be switched at run time,
// for game days (deploy/gameday). It answers like a real 공공데이터 API:
//
//	GET  /api?serviceKey=...      {"response":{"header":{"resultCode":"00",...},"body":{...}}}
//	POST /admin/mode?m=MODE[&ms=N] switch the failure mode (GET shows the current one)
//
// Modes: ok, down (HTTP 503), error05 (HTTP 200 with resultCode 05, the failure a plain
// HTTP check misses), html (HTTP 200 with an error page), slow (adds ms milliseconds),
// hang (never answers), stale (data timestamp 6 hours old), nokey (code 30).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

var (
	mu    sync.Mutex
	mode  = "ok"
	delay time.Duration
	kst   = time.FixedZone("KST", 9*3600)
)

func current() (string, time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	return mode, delay
}

func api(w http.ResponseWriter, r *http.Request) {
	m, d := current()
	now := time.Now().In(kst)
	switch m {
	case "hang":
		<-r.Context().Done()
		return
	case "slow":
		time.Sleep(d)
	case "down":
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
		return
	case "html":
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<!DOCTYPE html><html><body><h1>점검 중입니다</h1></body></html>")
		return
	case "nokey":
		fmt.Fprint(w, `<OpenAPI_ServiceResponse><cmmMsgHeader><errMsg>SERVICE ERROR</errMsg><returnAuthMsg>SERVICE_KEY_IS_NOT_REGISTERED_ERROR</returnAuthMsg><returnReasonCode>30</returnReasonCode></cmmMsgHeader></OpenAPI_ServiceResponse>`)
		return
	case "error05":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"05","resultMsg":"SERVICETIMEOUT_ERROR"}}}`)
		return
	case "stale":
		now = now.Add(-6 * time.Hour)
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"response":{"header":{"resultCode":"00","resultMsg":"NORMAL SERVICE."},"body":{"items":{"item":[{"dataTime":"%s","value":"42"}]},"totalCount":1}}}`,
		now.Format("2006-01-02 15:04"))
}

func admin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		m := r.URL.Query().Get("m")
		switch m {
		case "ok", "down", "error05", "html", "slow", "hang", "stale", "nokey":
		default:
			http.Error(w, "unknown mode", http.StatusBadRequest)
			return
		}
		ms, _ := strconv.Atoi(r.URL.Query().Get("ms"))
		mu.Lock()
		mode, delay = m, time.Duration(ms)*time.Millisecond
		mu.Unlock()
		log.Printf("mode=%s delay=%s", m, delay)
	}
	m, d := current()
	json.NewEncoder(w).Encode(map[string]any{"mode": m, "delayMs": d.Milliseconds()})
}

func main() {
	addr := flag.String("addr", ":9000", "listen address")
	flag.Parse()
	http.HandleFunc("/api", api)
	http.HandleFunc("/admin/mode", admin)
	log.Printf("fakeapi on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
