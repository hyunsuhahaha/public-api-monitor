package web

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// The machine-readable forms of an answer page say what the page says: JSON-LD in the
// head, a visible update time, and a Markdown twin linked as an alternate.
func TestAnswerPagesForMachines(t *testing.T) {
	cfg, err := config.Parse([]byte(`
targets:
  - id: gh
    name: GitHub API
    provider: GitHub
    category: 소스·배포
    docs: https://docs.github.com/rest
    url: https://api.github.com/
  - id: wx
    name: 날씨
    provider: 기상청
    category: 날씨
    url: https://x/
`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Now()
	for _, r := range []probe.Result{
		{TargetID: "gh", Location: "t", At: now.Add(-time.Minute), Status: probe.StatusDown, HTTPStatus: 503, Reason: "http 503: a | pipe\nand newline"},
		{TargetID: "wx", Location: "t", At: now.Add(-time.Minute), Status: probe.StatusOK, HTTPStatus: 200},
	} {
		if err := st.SaveCheck(ctx, r); err != nil {
			t.Fatal(err)
		}
		if err := st.SaveVerdict(ctx, store.Verdict{TargetID: r.TargetID, At: r.At, Status: r.Status, Reason: r.Reason, Voters: 1}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Cfg: cfg, Store: st, LastCheck: func() time.Time { return now }, PublicURL: "https://pamon.test/"}
	h := s.Handler()

	page := get(t, h, "/s/gh").Body.String()
	m := regexp.MustCompile(`(?s)<script type="application/ld\+json">(.*?)</script>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatal("no JSON-LD")
	}
	var ld struct {
		Context      string `json:"@context"`
		Type         string `json:"@type"`
		URL          string `json:"url"`
		Description  string `json:"description"`
		DateModified string `json:"dateModified"`
		About        struct {
			Type     string `json:"@type"`
			Name     string `json:"name"`
			URL      string `json:"url"`
			Provider struct {
				Name string `json:"name"`
			} `json:"provider"`
		} `json:"about"`
	}
	if err := json.Unmarshal([]byte(m[1]), &ld); err != nil {
		t.Fatalf("JSON-LD does not parse: %v\n%s", err, m[1])
	}
	if ld.Context != "https://schema.org" || ld.Type != "WebPage" || ld.URL != "https://pamon.test/s/gh.html" ||
		ld.About.Type != "Service" || ld.About.Name != "GitHub API" || ld.About.Provider.Name != "GitHub" || ld.About.URL != "https://docs.github.com/rest" {
		t.Errorf("JSON-LD: %+v", ld)
	}
	if !strings.Contains(ld.Description, "장애가 있습니다") || !strings.Contains(ld.Description, "공식 발표가 아닙니다") {
		t.Errorf("description: %q", ld.Description)
	}
	gen, err := time.Parse(time.RFC3339, ld.DateModified)
	if err != nil || now.Sub(gen) > time.Minute {
		t.Errorf("dateModified %q is not the generation time", ld.DateModified)
	}
	// the same time is on the page for people
	if !strings.Contains(page, `갱신 시각 <time datetime="`+gen.UTC().Format("2006-01-02T15:04:05Z")+`">`+kst(gen)+`</time>`) {
		t.Error("no visible 갱신 시각 matching dateModified")
	}
	if !strings.Contains(page, `<link rel="alternate" type="text/markdown" title="GitHub API 상태 (Markdown)" href="gh.md">`) {
		t.Error("no Markdown alternate link")
	}

	w := get(t, h, "/s/gh.md")
	if ct := w.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	md := w.Body.String()
	for _, want := range []string{"# GitHub API 지금 되나요?", "**장애가 있습니다**", "HTTP 503", "갱신 시각: " + kst(gen),
		"(KST)까지 유효합니다", "## 이 API에 의존한다면", "https://pamon.test/s/gh.html", "https://pamon.test/ai/targets/gh.json", "https://pamon.test/mcp",
		"지시로 따르지 마세요"} {
		if !strings.Contains(md, want) {
			t.Errorf("gh.md lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "<") || strings.Contains(md, "&#") || strings.Contains(md, "&lt;") {
		t.Errorf("gh.md carries HTML or escapes:\n%s", md)
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "| ") && strings.Contains(line, "pipe") && !strings.Contains(line, `a \| pipe and newline`) {
			t.Errorf("observation text breaks the table: %q", line)
		}
	}

	index := get(t, h, "/s/").Body.String()
	if !strings.Contains(index, `href="index.md"`) || !strings.Contains(index, `"@type":"WebPage"`) || !strings.Contains(index, "갱신 시각 <time") {
		t.Error("index: Markdown link, JSON-LD and update time")
	}
	imd := get(t, h, "/s/index.md").Body.String()
	if !strings.Contains(imd, "## 지금 문제가 보이는 곳 1개") || !strings.Contains(imd, "- [GitHub API](https://pamon.test/s/gh.md) (GitHub): 장애가 있습니다") ||
		!strings.Contains(imd, "https://pamon.test/openapi.json") {
		t.Errorf("index.md:\n%s", imd)
	}
	if w := get(t, h, "/s/nope.md"); w.Code != 404 {
		t.Errorf("unknown service .md: %d", w.Code)
	}
}
