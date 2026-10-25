package web

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestBadgesAndWidget(t *testing.T) {
	_, h := setup(t)
	for path, want := range map[string]string{
		"/badge/a.svg":          "30일 가용률",
		"/badge/a-status.svg":   ">정상<",
		"/badge/b-status.svg":   "확인 불가", // never checked
		"/badge/summary.svg":    "1개 모두 정상",
		"/badge/a-status.svg?x": "API A",
	} {
		w := get(t, h, path)
		body := w.Body.String()
		if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/svg+xml") || !strings.Contains(body, want) {
			t.Errorf("%s: %d lacks %q: %s", path, w.Code, want, body)
		}
		if err := xml.Unmarshal(w.Body.Bytes(), new(struct{})); err != nil {
			t.Errorf("%s: not well-formed SVG: %v", path, err)
		}
	}
	if w := get(t, h, "/badge/a-status.svg?label=<script>"); strings.Contains(w.Body.String(), "<script>") {
		t.Error("label not escaped")
	}
	for _, p := range []string{"/badge/nope.svg", "/badge/nope-status.svg", "/w/nope.html", "/badge/a"} {
		if w := get(t, h, p); w.Code != 404 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	card := get(t, h, "/w/a.html").Body.String()
	for _, want := range []string{"API A", "정상입니다", "30일 가용률 75.00%", `href="https://pamon.hyunsuhahaha.workers.dev/s/a.html"`, `target="_blank"`} {
		if !strings.Contains(card, want) {
			t.Errorf("widget lacks %q", want)
		}
	}
	ans := get(t, h, "/s/a").Body.String()
	for _, want := range []string{"내 README·블로그에 붙이기", "/badge/a-status.svg)](https://pamon.hyunsuhahaha.workers.dev/s/a.html)", `&lt;iframe src="https://pamon.hyunsuhahaha.workers.dev/w/a.html"`} {
		if !strings.Contains(ans, want) {
			t.Errorf("answer page lacks %q", want)
		}
	}
}
