package web

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func maintServer(t *testing.T) *Server {
	t.Helper()
	now := time.Now().In(config.KST)
	f := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cfg, err := config.Parse([]byte(fmt.Sprintf(`
maintenance:
  - match: [한국환경공단]
    start: %s
    end: %s
    note: 에어코리아 정기 점검, 서버 교체; 긴 설명이 들어가도 캘린더 줄이 75바이트에서 접혀야 합니다
    url: https://www.data.go.kr/notice/1
  - match: [b]
    start: %s
    end: %s
    note: 지금 점검 중
  - match: [b]
    start: %s
    end: %s
    note: 오래전에 끝남
targets:
  - id: air
    name: 에어코리아 대기오염
    provider: 한국환경공단
    url: http://x
  - id: b
    name: API B
    url: http://y
`, f(24*time.Hour), f(26*time.Hour), f(-time.Hour), f(time.Hour), f(-60*24*time.Hour), f(-59*24*time.Hour))))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Server{Cfg: cfg, Store: st, LastCheck: time.Now, PublicURL: "https://pamon.example"}
}

func TestMaintenanceFeeds(t *testing.T) {
	h := maintServer(t).Handler()
	var feed MaintFeed
	if err := json.Unmarshal(get(t, h, "/maintenance.json").Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if len(feed.Windows) != 2 || feed.Windows[0].State != "active" || feed.Windows[1].State != "upcoming" ||
		feed.Windows[1].Targets[0].ID != "air" || feed.Windows[1].URL == "" || feed.Windows[0].ID == feed.Windows[1].ID {
		t.Fatalf("windows: %+v", feed.Windows)
	}
	w := get(t, h, "/maintenance.ics")
	ics := w.Body.String()
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/calendar") || strings.Count(ics, "BEGIN:VEVENT") != 2 ||
		!strings.Contains(ics, "UID:"+feed.Windows[1].ID+"@pamon") || !strings.Contains(strings.ReplaceAll(ics, "\r\n ", ""), `점검\, 서버 교체\; 긴`) {
		t.Fatalf("ics:\n%s", ics)
	}
	for _, l := range strings.Split(strings.TrimSuffix(ics, "\r\n"), "\r\n") {
		if len(l) > 75 || strings.Contains(l, "\n") {
			t.Fatalf("line not folded: %q", l)
		}
	}
	if page := get(t, h, "/maintenance.html").Body.String(); !strings.Contains(page, "지금 점검 중") || !strings.Contains(page, `href="s/air.html"`) {
		t.Fatalf("page: %s", page)
	}
	ans := get(t, h, "/s/air").Body.String()
	if !strings.Contains(ans, "공지된 점검") || !strings.Contains(ans, "에어코리아 정기 점검") || !strings.Contains(ans, `href="../maintenance.ics"`) {
		t.Fatalf("answer page lacks the window")
	}
	if strings.Contains(get(t, h, "/s/b").Body.String(), "오래전에 끝남") {
		t.Fatal("a window that ended 59 days ago is still shown")
	}
}
