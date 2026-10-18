// Package probe calls public APIs and decides whether they are healthy.
//
// Korean public APIs (data.go.kr and the city portals) usually answer HTTP 200 even when
// they fail, with the real outcome in a result code inside the XML or JSON body. A plain
// HTTP check calls those failures "up", so the body is read and classified here.
package probe

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Status of one check, from best to worst. Client means the problem is on our side
// (bad or unregistered key, quota used up, wrong parameters): it says nothing about the
// provider and is left out of uptime.
type Status string

const (
	StatusOK     Status = "ok"
	StatusSlow   Status = "slow"   // answered correctly, but slower than the target's limit
	StatusStale  Status = "stale"  // answered correctly, but the data has stopped updating
	StatusError  Status = "error"  // answered, with an error from the provider
	StatusDown   Status = "down"   // no usable answer: timeout, connection error, 5xx
	StatusClient Status = "client" // our key, quota or request is the problem
	StatusMaint  Status = "maint"  // verdict inside an announced maintenance window
)

// Severity orders statuses for incidents: 0 healthy, 1 degraded, 2 outage.
func (s Status) Severity() int {
	switch s {
	case StatusSlow, StatusStale:
		return 1
	case StatusError, StatusDown:
		return 2
	}
	return 0
}

// Counts reports whether the status says anything about the provider (uptime math).
func (s Status) Counts() bool { return s != StatusClient && s != StatusMaint }

// Healthy is true for statuses that count as "up" in uptime.
func (s Status) Healthy() bool { return s == StatusOK || s == StatusSlow || s == StatusStale }

// Result codes. data.go.kr gateway errors come as
// <OpenAPI_ServiceResponse><cmmMsgHeader><returnReasonCode>30</...>, provider answers as
// <response><header><resultCode>00</...> or the same in JSON; Seoul's open API uses
// {"RESULT":{"CODE":"INFO-000"}} or "INFO-000" inside the payload.
var (
	codePatterns = []codePattern{
		{re: regexp.MustCompile(`<returnReasonCode>\s*([^<\s]+)\s*</returnReasonCode>`)},
		{re: regexp.MustCompile(`<resultCode>\s*([^<\s]+)\s*</resultCode>`)},
		{re: regexp.MustCompile(`"resultCode"\s*:\s*"?([^",}\s]+)`)},
		{re: regexp.MustCompile(`<CODE>\s*([^<\s]+)\s*</CODE>`)},
		{re: regexp.MustCompile(`"CODE"\s*:\s*"([^"]+)"`)},
		{re: regexp.MustCompile(`"RESULT\.CODE"\s*:\s*"([^"]+)"`)}, // 서울 실시간 도시데이터
		{re: regexp.MustCompile(`"code"\s*:\s*"((?:INFO|ERROR)-\d+)"`)},
		// 서울 버스(ws.bus.go.kr): its own table, so its codes get a prefix.
		{re: regexp.MustCompile(`<headerCd>\s*(\d+)\s*</headerCd>`), prefix: "BUS-"},
	}
	msgPatterns = []*regexp.Regexp{
		regexp.MustCompile(`<returnAuthMsg>\s*([^<]+?)\s*</returnAuthMsg>`),
		regexp.MustCompile(`<resultMsg>\s*([^<]+?)\s*</resultMsg>`),
		regexp.MustCompile(`"resultMsg"\s*:\s*"([^"]*)"`),
		regexp.MustCompile(`<MESSAGE>\s*([^<]+?)\s*</MESSAGE>`),
		regexp.MustCompile(`"MESSAGE"\s*:\s*"([^"]*)"`),
		regexp.MustCompile(`"RESULT\.MESSAGE"\s*:\s*"([^"]*)"`),
		regexp.MustCompile(`"message"\s*:\s*"([^"]*)"`),
		regexp.MustCompile(`<headerMsg>\s*([^<]+?)\s*</headerMsg>`),
	}
)

// okCodes: a normal answer. 03 / INFO-200 are "no data for this query", which is still a
// working API.
var okCodes = map[string]bool{
	"00": true, "0": true, "0000": true, "000": true, "03": true, "INFO-000": true, "INFO-200": true,
	// 서울 버스: 0 normal, 4 no result, 21-23 no route / no arrival information
	"BUS-0": true, "BUS-4": true, "BUS-21": true, "BUS-22": true, "BUS-23": true,
}

// clientCodes: data.go.kr's own error table (10 bad parameter, 11 missing parameter,
// 20 access denied, 22 daily quota exceeded, 30 key not registered, 31 key expired,
// 32 unregistered IP, 33 unsigned call) and Seoul's key / request errors.
var clientCodes = map[string]bool{
	"10": true, "11": true, "20": true, "22": true, "30": true, "31": true, "32": true, "33": true,
	"INFO-100": true, "ERROR-300": true, "ERROR-301": true, "ERROR-310": true, "ERROR-331": true,
	"ERROR-332": true, "ERROR-333": true, "ERROR-334": true, "ERROR-335": true, "ERROR-336": true,
	// 서울 버스: 2/3 missing or bad parameter, 5 no key, 6 unregistered key, 7 unusable key, 8 quota
	"BUS-2": true, "BUS-3": true, "BUS-5": true, "BUS-6": true, "BUS-7": true, "BUS-8": true,
}

type codePattern struct {
	re     *regexp.Regexp
	prefix string
}

func findCode(s string) string {
	for _, p := range codePatterns {
		if m := p.re.FindStringSubmatch(s); m != nil {
			return p.prefix + strings.TrimSpace(m[1])
		}
	}
	return ""
}

// Observation is what one HTTP call produced.
type Observation struct {
	Err        error
	Timeout    bool
	HTTPStatus int
	Body       []byte
	Latency    time.Duration
}

// Verdict is the classification of an observation.
type Verdict struct {
	Status Status
	Code   string // result code found in the body, if any
	Reason string // short human reason
}

// Classify turns an observation into a verdict. slowAfter of 0 disables the slow check.
func Classify(o Observation, slowAfter time.Duration) Verdict {
	if o.Timeout {
		return Verdict{Status: StatusDown, Reason: "timeout"}
	}
	if o.Err != nil {
		return Verdict{Status: StatusDown, Reason: "request failed: " + shorten(o.Err.Error(), 120)}
	}
	body := string(o.Body)
	code := findCode(body)
	msg := findFirst(msgPatterns, body)
	switch {
	case o.HTTPStatus >= 500:
		return Verdict{Status: StatusDown, Code: code, Reason: httpReason(o.HTTPStatus, msg)}
	case o.HTTPStatus == 401 || o.HTTPStatus == 403 || o.HTTPStatus == 429:
		return Verdict{Status: StatusClient, Code: code, Reason: httpReason(o.HTTPStatus, msg)}
	case o.HTTPStatus >= 400:
		return Verdict{Status: StatusError, Code: code, Reason: httpReason(o.HTTPStatus, msg)}
	}
	if strings.TrimSpace(body) == "" {
		return Verdict{Status: StatusError, Reason: "empty body"}
	}
	if code == "" {
		// A gateway error page or a plain-text failure instead of the API's XML/JSON.
		lower := strings.ToLower(body)
		if strings.Contains(lower, "<html") {
			return Verdict{Status: StatusError, Reason: "html page instead of api response"}
		}
		if strings.Contains(lower, "unexpected errors") || strings.Contains(lower, "service time out") {
			return Verdict{Status: StatusError, Reason: shorten(strings.TrimSpace(body), 120)}
		}
		// No code at all but a body: treat as answered (some APIs have no header).
	} else if !okCodes[code] {
		if clientCodes[code] {
			return Verdict{Status: StatusClient, Code: code, Reason: codeReason(code, msg)}
		}
		return Verdict{Status: StatusError, Code: code, Reason: codeReason(code, msg)}
	}
	if slowAfter > 0 && o.Latency > slowAfter {
		return Verdict{Status: StatusSlow, Code: code, Reason: "slow: " + o.Latency.Round(time.Millisecond).String()}
	}
	return Verdict{Status: StatusOK, Code: code}
}

func findFirst(patterns []*regexp.Regexp, s string) string {
	for _, p := range patterns {
		if m := p.FindStringSubmatch(s); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

func httpReason(status int, msg string) string {
	r := "http " + strconv.Itoa(status)
	if msg != "" {
		r += ": " + shorten(msg, 100)
	}
	return r
}

func codeReason(code, msg string) string {
	r := "code " + code
	if msg != "" {
		r += ": " + shorten(msg, 100)
	}
	return r
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
