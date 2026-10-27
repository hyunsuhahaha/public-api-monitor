package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// announce tells IndexNow (Bing, and the AI search built on it; Naver; Yandex) about
// pages that did not exist before this export, so a new outage page is crawled within
// minutes instead of at the gateway's daily sitemap submission. Failures are logged and
// otherwise ignored: the daily submission still covers the page.
var announce = func(base string, paths []string) {
	u, err := url.Parse(base)
	if err != nil {
		return
	}
	list := make([]string, 0, len(paths))
	for _, p := range paths {
		list = append(list, base+p)
	}
	body, _ := json.Marshal(map[string]any{"host": u.Host, "key": indexNowKey, "keyLocation": base + indexNowKey + ".txt", "urlList": list})
	client := &http.Client{Timeout: 10 * time.Second}
	for _, endpoint := range []string{"https://api.indexnow.org/indexnow", "https://searchadvisor.naver.com/indexnow"} {
		res, err := client.Post(endpoint, "application/json; charset=utf-8", bytes.NewReader(body))
		if err != nil {
			slog.Warn("indexnow", "endpoint", endpoint, "err", err)
			continue
		}
		res.Body.Close()
		slog.Info("indexnow", "endpoint", endpoint, "status", res.StatusCode, "urls", len(list))
	}
}
