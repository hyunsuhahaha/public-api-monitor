package probe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

func TestArtifactFullBodyValidation(t *testing.T) {
	body := []byte("immutable package bytes\x00\xff")
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name   string
		body   []byte
		status int
		want   Status
		reason string
	}{
		{"valid", body, 200, StatusOK, ""},
		{"truncated", body[:len(body)-1], 200, StatusError, "undersized"},
		{"oversized with valid prefix", append(append([]byte{}, body...), 'x'), 200, StatusError, "byte limit"},
		{"wrong content same length", []byte(strings.Repeat("x", len(body))), 200, StatusError, "SHA256"},
		{"HTML 200", []byte(strings.Repeat("<html>", len(body))), 200, StatusError, "byte limit"},
		{"empty", nil, 200, StatusError, "empty"},
		{"partial HTTP response", body, 206, StatusError, "complete 200"},
		{"missing artifact", body, 404, StatusError, "404"},
		{"rate limited", body, 429, StatusClient, "429"},
		{"server outage", body, 503, StatusDown, "503"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(c.status); w.Write(c.body) }))
			defer srv.Close()
			tg := target(t, fmt.Sprintf("targets:\n  - id: file\n    auth: none\n    classifier: http\n    url: %s\n    artifact: {sha256: %s, min_bytes: %d, max_bytes: %d}\n", srv.URL, digest, len(body), len(body)))
			r := New("test").Check(context.Background(), tg)
			if r.Status != c.want || !strings.Contains(r.Reason, c.reason) {
				t.Fatalf("%+v", r)
			}
			if r.Shape != nil {
				t.Fatal("binary artifact learned as API schema")
			}
		})
	}
}

// An API asked without credentials answers its own 401; a CDN's bare 403, a 5xx or a
// page without the expected text is not that service answering.
func TestExpectedStatusAndText(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   Status
		reason string
	}{
		{"own 401 with its error envelope", 401, `{"resultcode":"024","message":"Authentication failed"}`, StatusOK, ""},
		{"bare 403 from a CDN", 403, "", StatusError, "expected 401"},
		{"401 without a body", 401, "", StatusError, "without a body"},
		{"401 without the envelope", 401, "<html>blocked</html>", StatusError, "lacks"},
		{"outage", 503, `{"resultcode":"024"}`, StatusDown, "503"},
		{"200 instead of 401", 200, `{"resultcode":"024"}`, StatusError, "expected 401"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(c.status); w.Write([]byte(c.body)) }))
			defer srv.Close()
			tg := target(t, fmt.Sprintf("targets:\n  - id: api\n    auth: none\n    classifier: http\n    expect_status: 401\n    contains: ['\"resultcode\"']\n    url: %s\n", srv.URL))
			r := New("test").Check(context.Background(), tg)
			if r.Status != c.want || !strings.Contains(r.Reason, c.reason) {
				t.Fatalf("%+v", r)
			}
		})
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>점검 중입니다</html>")) }))
	defer page.Close()
	tg := target(t, fmt.Sprintf("targets:\n  - id: page\n    auth: none\n    classifier: http\n    contains: ['로그인']\n    url: %s\n", page.URL))
	if r := New("test").Check(context.Background(), tg); r.Status != StatusError || !strings.Contains(r.Reason, "로그인") {
		t.Fatalf("maintenance page taken for the login page: %+v", r)
	}
}

func TestHTTPClassifierDoesNotUsePublicResultCodes(t *testing.T) {
	for _, body := range []string{`{"code":"ERROR-500"}`, `{"resultCode":"30"}`, "<html>file content</html>", "binary\x00\xff"} {
		v := classifyHTTP(Observation{HTTPStatus: 200, Body: []byte(body)}, time.Second)
		if v.Status != StatusOK || v.Code != "" {
			t.Fatal(v)
		}
	}
	if v := classifyHTTP(Observation{HTTPStatus: 200, Body: []byte("x"), Latency: 2 * time.Second}, time.Second); v.Status != StatusSlow {
		t.Fatal(v)
	}
	if v := classifyHTTP(Observation{Timeout: true}, time.Second); v.Status != StatusDown {
		t.Fatal(v)
	}
}

func TestRegistryMetadataChecksAndArtifactAreIndependent(t *testing.T) {
	t.Setenv("DATA_GO_KR_KEY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metadata" {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "package")
	}))
	defer srv.Close()
	sum := sha256.Sum256([]byte("package"))
	file := target(t, fmt.Sprintf("targets:\n  - id: file\n    auth: none\n    classifier: http\n    url: %s/file\n    artifact: {sha256: %x, min_bytes: 7, max_bytes: 7}\n", srv.URL, sum))
	metadata := target(t, "targets:\n  - id: metadata\n    auth: none\n    classifier: http\n    url: "+srv.URL+"/metadata\n    json_expect: [{path: name, equals: canary}]\n")
	p := New("test")
	if r := p.Check(context.Background(), metadata); r.Status != StatusDown {
		t.Fatal(r)
	}
	if r := p.Check(context.Background(), file); r.Status != StatusOK {
		t.Fatal("metadata failure suppressed download", r)
	}
}

func TestArtifactReaderIsBounded(t *testing.T) {
	// A successful response with an endless body must be rejected after max_bytes+1,
	// without waiting for EOF or accepting a correct prefix.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 1024))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	tg := target(t, "targets:\n  - id: file\n    auth: none\n    classifier: http\n    timeout: 2s\n    url: "+srv.URL+"\n    artifact: {sha256: "+strings.Repeat("0", 64)+", min_bytes: 1, max_bytes: 10}\n")
	start := time.Now()
	r := New("test").Check(context.Background(), tg)
	if r.Status != StatusError || !strings.Contains(r.Reason, "byte limit") || time.Since(start) > time.Second {
		t.Fatalf("unbounded body read: %+v", r)
	}
}

func TestRegistryCanaryConfiguration(t *testing.T) {
	c, err := config.Load("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tg := range c.Targets {
		if tg.Provider != "npm" && tg.Provider != "PyPI" {
			continue
		}
		count++
		if tg.Auth != "none" || tg.Classifier != "http" {
			t.Fatalf("registry needs no API key: %s", tg.ID)
		}
		if strings.HasSuffix(tg.ID, "download") {
			if tg.Artifact == nil || tg.Artifact.MaxBytes > 8192 {
				t.Fatalf("unbounded canary %s", tg.ID)
			}
		} else if len(tg.JSONExpect) < 3 {
			t.Fatalf("metadata lacks validation %s", tg.ID)
		}
	}
	if count != 4 {
		t.Fatalf("got %d registry targets", count)
	}
}
