package web

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

func TestRetryAdviceAccuracy(t *testing.T) {
	st, h := setup(t)
	ctx := context.Background()
	// b: blips (a failure, then fine) four times
	t0 := time.Now().Add(-3 * time.Hour)
	for i := 0; i < 16; i++ {
		s := probe.StatusOK
		if i%4 == 1 {
			s = probe.StatusDown
		}
		st.SaveVerdict(ctx, store.Verdict{TargetID: "b", At: t0.Add(time.Duration(i) * 5 * time.Minute), Status: s, Voters: 1})
	}
	var v AccuracyView
	if err := json.Unmarshal(get(t, h, "/api/v1/retry-advice").Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	b := v.Targets["b"]
	if b.Wait != 4 || b.WaitRight != 0 || b.Retry != 11 || b.RetryRight != 7 {
		t.Fatalf("%+v", b)
	}
	page := get(t, h, "/retry-advice.html").Body.String()
	for _, want := range []string{"pamon의 재시도 조언은 맞았나", `href="s/b.html"`, "0.0% (4회)", "짧게 몇 번 재시도하는 편이 낫습니다"} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if ans := get(t, h, "/s/b").Body.String(); !strings.Contains(ans, "다음 확인도 실패한 비율: 0.0% (4회)") {
		t.Error("answer page lacks the accuracy line")
	}
}
