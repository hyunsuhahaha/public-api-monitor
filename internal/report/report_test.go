package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func kst(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, config.KST)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBuild(t *testing.T) {
	cfg, _ := config.Parse([]byte(`
targets:
  - {id: good, name: 좋은 API, provider: 기관A, url: http://x}
  - {id: bad, name: 나쁜 API, provider: 기관B, url: http://y}
  - {id: none, name: 기록 없음, provider: 기관B, url: http://z}
`))
	st, err := store.Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := kst("2026-10-10 12:00")
	for i := 0; i < 10; i++ {
		st.SaveVerdict(ctx, store.Verdict{TargetID: "good", At: at.Add(time.Duration(i) * time.Minute), Status: probe.StatusOK})
		s := probe.StatusOK
		if i < 3 {
			s = probe.StatusDown
		}
		st.SaveVerdict(ctx, store.Verdict{TargetID: "bad", At: at.Add(time.Duration(i) * time.Minute), Status: s})
	}
	// a verdict from the previous month must not count
	st.SaveVerdict(ctx, store.Verdict{TargetID: "good", At: kst("2026-09-30 23:00"), Status: probe.StatusDown})
	// an outage that began in September counts only from 10/1 00:00
	st.SaveIncident(ctx, &incident.Incident{TargetID: "bad", Started: kst("2026-09-30 23:00"), Ended: kst("2026-10-01 01:30"), Severity: 2, Status: probe.StatusDown, Reason: "timeout"})
	st.SaveIncident(ctx, &incident.Incident{TargetID: "bad", Started: kst("2026-10-10 12:00"), Ended: kst("2026-10-10 12:20"), Severity: 2, Status: probe.StatusDown, Reason: "code 05"})
	// a degradation is an incident but not downtime
	st.SaveIncident(ctx, &incident.Incident{TargetID: "good", Started: kst("2026-10-11 08:00"), Ended: kst("2026-10-11 09:00"), Severity: 1, Status: probe.StatusSlow, Reason: "slow"})

	r, err := Build(ctx, st, cfg, "2026-10", kst("2026-11-05 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Partial || r.WithData != 2 || r.Incidents != 3 {
		t.Fatalf("summary: %+v", r)
	}
	if r.Rows[0].ID != "bad" || r.Rows[0].Availability != 0.7 || r.Rows[1].Availability != 1 || r.Rows[2].ID != "none" {
		t.Fatalf("rows: %+v", r.Rows)
	}
	if r.Rows[0].DowntimeMin != 90+20 || r.Rows[0].LongestMin != 90 || r.Rows[1].DowntimeMin != 0 || r.Rows[1].Incidents != 1 {
		t.Fatalf("downtime: %+v", r.Rows[:2])
	}
	if r.Providers[0].Name != "기관B" || r.Providers[0].Availability != 0.7 {
		t.Fatalf("providers: %+v", r.Providers)
	}
	md := r.Markdown("https://status.example")
	for _, want := range []string{"# 2026년 10월 공공 API 가용성 리포트", "| 1 | 나쁜 API | 기관B | 70.000% | 1시간 50분 | 1시간 30분 |", "https://status.example"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}

	if _, err := Build(ctx, st, cfg, "2026/10", time.Now()); err == nil {
		t.Fatal("bad month accepted")
	}
	r, _ = Build(ctx, st, cfg, "2026-10", kst("2026-10-15 00:00"))
	if !r.Partial {
		t.Fatal("current month is partial")
	}
}

func TestDur(t *testing.T) {
	for m, want := range map[int]string{0: "0분", 5: "5분", 60: "1시간", 125: "2시간 5분"} {
		if got := Dur(m); got != want {
			t.Errorf("%d: %s", m, got)
		}
	}
}

func TestBuildTimeoutFollowUps(t *testing.T) {
	cfg, _ := config.Parse([]byte("targets:\n  - {id: bad, name: 나쁜 API, url: http://y}\n"))
	st, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	at := kst("2026-10-19 11:43")
	for _, ms := range []int64{79, 122, 134} {
		st.SaveFollowUp(ctx, "bad", probe.FollowUp{At: at, Answered: true, AfterMs: ms, HTTP: 200, WaitMs: 60000, Timeout: 10000})
	}
	st.SaveFollowUp(ctx, "bad", probe.FollowUp{At: at, AfterMs: 60000, WaitMs: 60000, Timeout: 10000})
	r, err := Build(ctx, st, cfg, "2026-10", kst("2026-11-05 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Timeouts) != 1 {
		t.Fatalf("%+v", r.Timeouts)
	}
	x := r.Timeouts[0]
	if x.Name != "나쁜 API" || x.Checked != 4 || x.Retried != 3 || x.Hung != 1 || x.MedianMs != 122 {
		t.Fatalf("%+v", x)
	}
	if md := r.Markdown(""); !strings.Contains(md, "| 나쁜 API | 4 | 3 | 0 | 1 | 122ms |") {
		t.Fatalf("markdown: %s", md)
	}
}
