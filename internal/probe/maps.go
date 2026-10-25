package probe

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// classifyTarget keeps vendor codes separate: Kakao -10 and the public gateway's
// 10 have different meanings. HTTP 400 can mean a provider outage or our quota.
func classifyTarget(o Observation, t *config.Target) Verdict {
	switch t.Classifier {
	case "git":
		return classifyGit(o, t.SlowAfter.Duration)
	case "docker_token":
		v := classifyHTTP(o, t.SlowAfter.Duration)
		if v.Status.Healthy() {
			if _, err := parseRegistryToken(o.Body); err != nil {
				return Verdict{Status: StatusError, Reason: "registry auth: invalid token response"}
			}
		}
		return v
	case "docker_manifest":
		return classifyManifest(o, t.SlowAfter.Duration)
	}
	if t.Classifier == "http" {
		return classifyExpected(o, t)
	}
	if t.Classifier == "" || t.Classifier == "public" || o.Err != nil || o.Timeout {
		return Classify(o, t.SlowAfter.Duration)
	}
	v := Classify(o, t.SlowAfter.Duration)
	var body map[string]json.RawMessage
	err := json.Unmarshal(o.Body, &body)
	if o.HTTPStatus >= 500 {
		return v
	}
	if err != nil || body == nil {
		if o.HTTPStatus == 401 || o.HTTPStatus == 403 || o.HTTPStatus == 429 {
			return v
		}
		return Verdict{Status: StatusError, Reason: "invalid JSON API response"}
	}
	code, reason := "", ""
	client, failed := false, false
	switch t.Classifier {
	case "kakao":
		if raw, exists := body["code"]; exists {
			var n int
			if json.Unmarshal(raw, &n) != nil {
				return Verdict{Status: StatusError, Reason: "invalid Kakao result code"}
			}
			code, failed = "KAKAO:"+strconv.Itoa(n), true
			switch n {
			case -10:
				client, reason = true, "요청 쿼터 초과"
			case -11:
				client, reason = true, "유료 API 금액 한도 초과"
			case -401:
				client, reason = true, "인증키 확인"
			case -2, -8:
				client, reason = true, "요청값 또는 헤더 확인"
			case -3, -4, -5, -6, -12, -13, -903:
				client, reason = true, "앱 권한·활성화·접근 제한 확인"
			case -9:
				client, reason = true, "종료된 API: 대상 설정 변경 필요"
			default:
				reason = "카카오 API 처리 오류"
			}
		}
	case "naver_maps":
		if raw, exists := body["error"]; exists {
			var e struct {
				Code string `json:"errorCode"`
			}
			if json.Unmarshal(raw, &e) != nil {
				return Verdict{Status: StatusError, Reason: "invalid NAVER Maps error response"}
			}
			code, failed = "NAVER:"+e.Code, true
			switch e.Code {
			case "200", "210":
				client, reason = true, "인증키·API 권한 확인"
			case "400":
				client, reason = true, "쿼터 또는 사용 API 선택 확인"
			case "410", "420":
				client, reason = true, "호출 속도 제한"
			case "100", "300", "430":
				client, reason = true, "요청값·API 주소 확인"
			default:
				reason = "네이버 Maps 게이트웨이 오류"
			}
		} else if raw, exists := body["status"]; exists {
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return Verdict{Status: StatusError, Reason: "invalid NAVER Maps status"}
			}
			if s != "OK" {
				code, failed, client = "NAVER:"+s, true, s == "INVALID_REQUEST"
				reason = "네이버 Maps 처리 오류"
			}
		} else if raw, exists := body["code"]; exists {
			var n int
			if json.Unmarshal(raw, &n) != nil {
				return Verdict{Status: StatusError, Reason: "invalid NAVER Maps result code"}
			}
			if n != 0 {
				code, failed, reason = "NAVER:"+strconv.Itoa(n), true, "네이버 Maps 경로 생성 실패"
				client = n == 1 || n == 2 || n == 4 || n == 5
			}
		}
	}
	if failed {
		status := StatusError
		if client {
			status = StatusClient
		}
		return Verdict{Status: status, Code: code, Reason: "code " + code + ": " + reason}
	}
	if o.HTTPStatus < 200 || o.HTTPStatus >= 300 {
		if v.Status.Healthy() {
			return Verdict{Status: StatusError, Reason: fmt.Sprintf("http %d: unexpected API response", o.HTTPStatus)}
		}
		return v
	}
	// Vendor JSON was decoded, but schema/content checks below must still pass.
	if t.SlowAfter.Duration > 0 && o.Latency > t.SlowAfter.Duration {
		return Verdict{Status: StatusSlow, Reason: "slow: " + o.Latency.Round(time.Millisecond).String()}
	}
	return Verdict{Status: StatusOK}
}

func jsonValue(root any, path string) (any, bool) {
	v := root
	for _, part := range strings.Split(path, ".") {
		switch x := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = x[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			v = x[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// checkJSON rejects malformed payloads and evaluates each required field. Missing
// or out-of-range data is degraded, as freshness/expect is for public APIs.
func checkJSON(body []byte, rules []config.JSONExpect) (Status, string) {
	var root any
	if json.Unmarshal(body, &root) != nil {
		return StatusError, "invalid JSON API response"
	}
	for _, rule := range rules {
		value, ok := jsonValue(root, rule.Path)
		why := ""
		switch {
		case !ok || value == nil:
			why = "missing"
		case rule.Equals != nil:
			s, valid := value.(string)
			if !valid || s != *rule.Equals {
				why = "unexpected value"
			}
		}
		if why == "" && rule.MinItems != nil {
			items, valid := value.([]any)
			if !valid || len(items) < *rule.MinItems {
				why = "insufficient items"
			}
		}
		if why == "" && (rule.Min != nil || rule.Max != nil) {
			n, valid := value.(float64)
			if s, isString := value.(string); isString {
				var err error
				n, err = strconv.ParseFloat(s, 64)
				valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0)
			}
			if !valid || rule.Min != nil && n < *rule.Min || rule.Max != nil && n > *rule.Max {
				why = "out of range or not numeric"
			}
		}
		if why != "" {
			return StatusStale, "bad data: " + rule.Path + " " + why
		}
	}
	return StatusOK, ""
}
