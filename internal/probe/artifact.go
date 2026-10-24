package probe

import (
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
