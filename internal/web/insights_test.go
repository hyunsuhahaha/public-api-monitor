package web

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAnswerInsightsAndRetryAdvice(t *testing.T) {
	_, h := setup(t)
	page := get(t, h, "/s/a").Body.String()
	for _, want := range []string{"이 API의 장애는 보통 어떻게 가나", "최근 30일 장애·저하 1건", "시간대별 실패·느림"} {
		if !strings.Contains(page, want) {
			t.Errorf("answer page lacks %q", want)
		}
	}
	var d struct {
		Targets []struct {
			RetryAdvice struct {
				Action string `json:"action"`
			} `json:"retryAdvice"`
			IncidentPattern struct {
				Incidents int `json:"incidents"`
			} `json:"incidentPattern"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(get(t, h, "/ai/targets/a.json").Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Targets) != 1 || d.Targets[0].RetryAdvice.Action != "retry" || d.Targets[0].IncidentPattern.Incidents != 1 {
		t.Fatalf("%+v", d.Targets)
	}
}
