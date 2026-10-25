package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestRegistryTokenValidation(t *testing.T) {
	for _, c := range []struct {
		body  string
		valid bool
	}{
		{`{"token":"opaque-test-token","expires_in":300}`, true},
		{`{"access_token":"opaque-test-token"}`, true},
		{`{"token":""}`, false},
		{`{"token":"secret\r\nInjected"}`, false},
		{`{"token":"x","expires_in":0}`, false},
		{`{"token":"x","expires_in":-1}`, false},
		{`{"token":123}`, false},
		{`{"token":"x"} trailing`, false},
		{strings.Repeat(" ", 65537), false},
	} {
		_, err := parseRegistryToken([]byte(c.body))
		if (err == nil) != c.valid {
			t.Errorf("token validity mismatch (length %d)", len(c.body))
		}
		v := classifyTarget(Observation{HTTPStatus: 200, Body: []byte(c.body)}, &config.Target{Classifier: "docker_token"})
		if v.Status.Healthy() != c.valid || strings.Contains(v.Reason, "opaque-test-token") {
			t.Fatal(v)
		}
	}
}

func TestManifestValidation(t *testing.T) {
	valid := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:` + strings.Repeat("a", 64) + `","size":100}]}`
	for _, c := range []struct {
		body   string
		status Status
	}{
		{valid, StatusOK},
		{strings.Replace(valid, `"schemaVersion":2`, `"schemaVersion":1`, 1), StatusError},
		{strings.Replace(valid, `"size":100`, `"size":0`, 1), StatusError},
		{strings.Replace(valid, "sha256:", "invalid:", 1), StatusError},
		{`{"schemaVersion":2,"manifests":[]}`, StatusError},
		{`<html>login</html>`, StatusError},
	} {
		v := classifyManifest(Observation{HTTPStatus: 200, Body: []byte(c.body)}, time.Second)
		if v.Status != c.status {
			t.Fatal(v)
		}
	}
}

func TestDockerAuthPrerequisiteAndTokenSecrecy(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(previous)
	var tokenCode atomic.Int32
	tokenCode.Store(200)
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.WriteHeader(int(tokenCode.Load()))
			fmt.Fprint(w, `{"token":"private-runtime-token","expires_in":300}`)
			return
		}
		hits.Add(1)
		if r.Header.Get("Authorization") != "Bearer private-runtime-token" || r.Header.Get("Accept") != "application/vnd.oci.image.index.v1+json" {
			t.Error("registry request missing auth/Accept")
		}
		fmt.Fprint(w, `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"digest":"sha256:`+strings.Repeat("a", 64)+`","size":100}]}`)
	}))
	defer srv.Close()
	tg := target(t, "targets:\n  - id: manifest\n    auth: none\n    classifier: docker_manifest\n    accept: application/vnd.oci.image.index.v1+json\n    registry_token_url: "+srv.URL+"/token\n    url: "+srv.URL+"/manifest\n")
	p := New("test")
	p.Client.Transport = srv.Client().Transport
	if r := p.Check(context.Background(), tg); r.Status != StatusOK || strings.Contains(r.Reason, "private-runtime-token") {
		t.Fatal(r)
	}
	for _, code := range []int32{429, 503} {
		tokenCode.Store(code)
		before := hits.Load()
		r := p.Check(context.Background(), tg)
		if r.Status != StatusClient || !strings.HasPrefix(r.Reason, "prerequisite:") || hits.Load() != before || strings.Contains(r.Reason, "private-runtime-token") {
			t.Fatal("manifest incorrectly checked or auth token leaked", r)
		}
	}
	for _, span := range rec.Ended() {
		if strings.Contains(span.Name(), "private-runtime-token") {
			t.Fatal("token in span name")
		}
		for _, attr := range span.Attributes() {
			if strings.Contains(attr.Value.Emit(), "private-runtime-token") {
				t.Fatal("token in tracing attribute")
			}
		}
	}
}

func TestGitSmartHTTPAdvertisement(t *testing.T) {
	pkt := func(s string) string { return fmt.Sprintf("%04x%s", len(s)+4, s) }
	ref := strings.Repeat("a", 40) + " HEAD\x00multi_ack thin-pack\n"
	valid := pkt("# service=git-upload-pack\n") + "0000" + pkt(ref) + "0000"
	for _, c := range []struct {
		name, body, contentType string
		want                    Status
	}{
		{"valid", valid, "application/x-git-upload-pack-advertisement", StatusOK},
		{"HTML page", `<html>login</html>`, "text/html", StatusError},
		{"wrong content type", valid, "application/json", StatusError},
		{"truncated", valid[:len(valid)-1], "application/x-git-upload-pack-advertisement", StatusError},
		{"no HEAD", strings.Replace(valid, "HEAD", "REFS", 1), "application/x-git-upload-pack-advertisement", StatusError},
		{"invalid packet length", "zzzz" + valid, "application/x-git-upload-pack-advertisement", StatusError},
		{"invalid object id", strings.Replace(valid, strings.Repeat("a", 40), strings.Repeat("z", 40), 1), "application/x-git-upload-pack-advertisement", StatusError},
		{"trailing data", valid + "garbage", "application/x-git-upload-pack-advertisement", StatusError},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := classifyGit(Observation{HTTPStatus: 200, Body: []byte(c.body), ContentType: c.contentType}, time.Second)
			if v.Status != c.want {
				t.Fatal(v)
			}
		})
	}
}
