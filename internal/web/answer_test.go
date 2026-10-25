package web

import (
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/official"
	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

func TestExplainSaysWhereItBrokeAndWhoseSide(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Minute)
	for _, c := range []struct {
		status probe.Status
		reason string
		tone   string
		detail string
		side   string
	}{
		{probe.StatusOK, "", "ok", "정상 응답", "이용하는 쪽"},
		{probe.StatusDown, "timeout: 서버 응답 대기 중 (연결은 됨)", "bad", "서버가 제때 답하지", "당신만의 문제가 아닙니다"},
		{probe.StatusDown, "timeout: DNS 조회 중", "bad", "DNS", "당신만의"},
		{probe.StatusDown, "http 503: Service Unavailable", "bad", "HTTP 503", "당신만의"},
		{probe.StatusError, "answer lacks \"홈택스\": an error or block page, not the service", "bad", "점검 안내나 차단", ""},
		{probe.StatusSlow, "slow: 4.2s", "warn", "4.2s", "느리게"},
		{probe.StatusClient, "http 401: key not registered", "unknown", "감시 쪽 설정", "장애로 보지 않습니다"},
	} {
		v := Explain(c.status, c.reason, &fresh, now, 5*time.Minute)
		if v.Tone != c.tone || !strings.Contains(v.Detail, c.detail) || !strings.Contains(v.YourSide, c.side) {
			t.Errorf("%s %q: %+v", c.status, c.reason, v)
		}
	}
	for _, c := range []struct {
		indicator, ours, want string
	}{
		{"none", "bad", "아직 정상이라고"}, {"major", "bad", "인정했습니다"}, {"minor", "ok", "다른 기능"}, {"none", "ok", "공식 발표도 정상"},
	} {
		if v := compareOfficial(official.Status{Indicator: c.indicator}, c.ours); !strings.Contains(v.Compare, c.want) {
			t.Errorf("%s vs ours %s: %q", c.indicator, c.ours, v.Compare)
		}
	}
	old := now.Add(-time.Hour)
	if v := Explain(probe.StatusOK, "", &old, now, 5*time.Minute); v.Tone != "unknown" {
		t.Errorf("an hour-old ok is not a current answer: %+v", v)
	}
	if v := Explain(probe.StatusOK, "", nil, now, 5*time.Minute); v.Tone != "unknown" {
		t.Errorf("never checked: %+v", v)
	}
}
