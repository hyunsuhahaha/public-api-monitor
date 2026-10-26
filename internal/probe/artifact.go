package probe

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// HTTP targets have no public-API result-code convention. A registry's own
// JSON fields or the bytes inside a wheel/tarball must not be interpreted as one.
func classifyHTTP(o Observation, slowAfter time.Duration) Verdict {
	if o.Err != nil || o.Timeout || o.HTTPStatus >= 400 {
		o.Body = nil
		return Classify(o, slowAfter)
	}
	if o.HTTPStatus >= 300 && o.HTTPStatus < 400 {
		return Classify(o, slowAfter) // names where it redirected
	}
	if o.HTTPStatus != 200 {
		return Verdict{Status: StatusError, Reason: fmt.Sprintf("http %d: expected complete 200 response", o.HTTPStatus)}
	}
	if len(o.Body) == 0 {
		return Verdict{Status: StatusError, Reason: "empty HTTP response"}
	}
	if slowAfter > 0 && o.Latency > slowAfter {
		return Verdict{Status: StatusSlow, Reason: "slow: " + o.Latency.Round(time.Millisecond).String()}
	}
	return Verdict{Status: StatusOK}
}

// classifyExpected is classifyHTTP with the target's expected status and text: an API
// asked without credentials must answer its own 401 (not a CDN's), a page must still be
// the page.
func classifyExpected(o Observation, t *config.Target) Verdict {
	want := t.ExpectStatus
	if want == 0 || want == 200 || o.Err != nil || o.Timeout || o.HTTPStatus >= 500 {
		v := classifyHTTP(o, t.SlowAfter.Duration)
		if v.Status.Healthy() {
			if reason := missingText(o.Body, t.Contains); reason != "" {
				return Verdict{Status: StatusError, Reason: reason}
			}
		}
		return v
	}
	if o.HTTPStatus != want {
		return Verdict{Status: StatusError, Reason: fmt.Sprintf("http %d: expected %d from the service itself", o.HTTPStatus, want)}
	}
	if len(o.Body) == 0 {
		return Verdict{Status: StatusError, Reason: fmt.Sprintf("http %d without a body: not the service's own answer", o.HTTPStatus)}
	}
	if reason := missingText(o.Body, t.Contains); reason != "" {
		return Verdict{Status: StatusError, Reason: reason}
	}
	if t.SlowAfter.Duration > 0 && o.Latency > t.SlowAfter.Duration {
		return Verdict{Status: StatusSlow, Reason: "slow: " + o.Latency.Round(time.Millisecond).String()}
	}
	return Verdict{Status: StatusOK}
}

func missingText(body []byte, want []string) string {
	for _, w := range want {
		if !bytes.Contains(body, []byte(w)) {
			return fmt.Sprintf("answer lacks %q: an error or block page, not the service", w)
		}
	}
	return ""
}

func checkArtifact(body []byte, expected *config.Artifact) string {
	size := int64(len(body))
	if size < expected.MinBytes {
		return "artifact: incomplete or undersized download"
	}
	if size > expected.MaxBytes {
		return "artifact: download exceeds byte limit"
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expected.SHA256 {
		return "artifact: SHA256 mismatch"
	}
	return ""
}
