package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/contract"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

// trySpacing is the pause between samples (tests shorten it).
var trySpacing = 2 * time.Second

// tryTarget is `pamon -try <url | target id>`: check an API a few times the way pamon
// would, say what it saw in plain words, propose json_expect rules from the answers
// (catching a 200 with empty or wrong data), optionally compare the answers with the
// API's OpenAPI document, and for a new URL print the targets.yaml entry to monitor it.
// A configured target's URL is never printed: it may carry a key.
func tryTarget(cfg *config.Config, arg string, samples int, specPath string, w io.Writer) int {
	var t *config.Target
	isURL := strings.HasPrefix(arg, "https://") || strings.HasPrefix(arg, "http://")
	if isURL {
		u, err := url.Parse(arg)
		if err != nil || u.Host == "" {
			fmt.Fprintln(w, "주소를 읽을 수 없습니다:", arg)
			return 2
		}
		y := fmt.Sprintf("targets:\n  - id: try\n    name: %s\n    provider: %s\n    auth: none\n    classifier: http\n    url: '%s'\n",
			u.Hostname(), u.Hostname(), strings.ReplaceAll(arg, "'", "''"))
		c, err := config.Parse([]byte(y))
		if err != nil {
			fmt.Fprintln(w, "설정으로 만들 수 없습니다:", err)
			return 2
		}
		t = &c.Targets[0]
	} else {
		for i := range cfg.Targets {
			if cfg.Targets[i].ID == arg {
				t = &cfg.Targets[i]
			}
		}
		if t == nil {
			fmt.Fprintln(w, "targets.yaml에 없는 id입니다:", arg)
			return 2
		}
	}
	samples = max(samples, 1)
	p := probe.New("try")
	fmt.Fprintf(w, "%s 확인 %d번 (%s 간격)\n\n", t.Name, samples, trySpacing)
	var bodies [][]byte
	var types []map[string]string
	var last probe.Result
	for i := 0; i < samples; i++ {
		if i > 0 {
			time.Sleep(trySpacing)
		}
		last = p.Check(context.Background(), t)
		ph := last.Phases
		fmt.Fprintf(w, "#%d %-6s HTTP %d  %dms (리다이렉트 %d · DNS %d · TCP %d · TLS %d · 서버 대기 %d)", i+1, last.Status, last.HTTPStatus, last.LatencyMS, ph.RedirectMs, ph.DNSMs, ph.ConnectMs, ph.TLSMs, ph.WaitMs)
		if last.Reason != "" {
			fmt.Fprintf(w, "  %s", last.Reason)
		}
		fmt.Fprintln(w)
		if t.Classifier == "docker_token" || t.Artifact != nil {
			continue
		}
		if _, _, body, _, err := fetchBody(t, probe.MaxBody); err == nil && probe.Shape(body) != nil {
			bodies = append(bodies, body)
			if k := probe.ShapeTypes(body); k != nil {
				types = append(types, k)
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "판정:", plainVerdict(last))
	rules := probe.SuggestRules(bodies)
	if len(bodies) > 0 {
		fmt.Fprintf(w, "응답 필드 %d개를 읽었습니다.\n", len(probe.Shape(bodies[len(bodies)-1])))
	}
	if len(rules) > 0 {
		fmt.Fprintln(w, "\n200인데 데이터가 비었거나 틀린 경우를 잡는 규칙 제안 (샘플에서 늘 같았던 값입니다. 계약인지 읽고 쓰세요):")
		if len(t.JSONExpect) > 0 {
			fmt.Fprintf(w, "(이미 규칙 %d개가 있습니다. 없는 것만 골라 더하세요.)\n", len(t.JSONExpect))
		}
		fmt.Fprintln(w, "    json_expect:")
		for _, r := range rules {
			fmt.Fprintln(w, "      - "+probe.FormatRule(r))
		}
	} else if len(bodies) == 0 {
		fmt.Fprintln(w, "JSON 응답이 아니거나 데이터가 없어 규칙을 제안하지 못했습니다.")
	}
	if specPath != "" {
		compareSpec(w, t, specPath, types)
	}
	if isURL {
		fmt.Fprintf(w, "\ntargets.yaml에 넣을 항목 (id·이름·분류는 고쳐 쓰세요):\n  - id: %s\n    name: %s\n    provider: %s\n    category: 기타\n    auth: none\n    classifier: http\n    url: '%s'\n",
			slug(t.Name), t.Name, t.Provider, strings.ReplaceAll(arg, "'", "''"))
		if len(rules) > 0 {
			fmt.Fprintln(w, "    json_expect:")
			for _, r := range rules {
				fmt.Fprintln(w, "      - "+probe.FormatRule(r))
			}
		}
	}
	if !last.Status.Healthy() {
		return 1
	}
	return 0
}

func plainVerdict(r probe.Result) string {
	switch r.Status {
	case probe.StatusOK:
		return "정상입니다."
	case probe.StatusSlow:
		return "응답은 정상이지만 느립니다."
	case probe.StatusStale:
		return "응답은 오지만 데이터가 오래됐거나 기대와 다릅니다."
	case probe.StatusClient:
		return "키·호출 한도·요청 쪽 문제로 보입니다 (서비스 장애로 보지 않습니다)."
	}
	return "실패입니다: " + r.Reason
}

func compareSpec(w io.Writer, t *config.Target, path string, seen []map[string]string) {
	fmt.Fprintln(w, "\nOpenAPI 문서와 비교:")
	spec, err := contract.Load(path)
	if err != nil {
		fmt.Fprintln(w, " ", err)
		return
	}
	// the path only: the query may carry a key
	u, err := url.Parse(strings.SplitN(t.URL, "?", 2)[0])
	if err != nil {
		fmt.Fprintln(w, "  주소의 경로를 읽을 수 없습니다")
		return
	}
	doc, tmpl, err := spec.Expected(u.Path)
	if err != nil {
		fmt.Fprintln(w, " ", err)
		return
	}
	if len(seen) == 0 {
		fmt.Fprintln(w, "  비교할 JSON 응답이 없습니다")
		return
	}
	d := contract.Compare(doc, seen)
	fmt.Fprintf(w, "  문서의 GET %s, 필드 %d개\n", tmpl, len(doc))
	if d.Empty() {
		fmt.Fprintln(w, "  문서와 실제 응답이 맞습니다.")
	}
	for _, x := range []struct {
		title string
		list  []string
	}{{"문서는 필수라는데 오지 않은 필드", d.Missing}, {"형식이 다른 필드", d.Mismatch}, {"문서에 없는 필드", d.Undocumented}, {"문서의 선택 필드 중 보지 못한 것 (보통 괜찮음)", d.Optional}} {
		if len(x.list) == 0 {
			continue
		}
		fmt.Fprintf(w, "  %s %d개:\n", x.title, len(x.list))
		for i, p := range x.list {
			if i == 20 {
				fmt.Fprintf(w, "    … 외 %d개\n", len(x.list)-20)
				break
			}
			fmt.Fprintln(w, "    "+p)
		}
	}
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
