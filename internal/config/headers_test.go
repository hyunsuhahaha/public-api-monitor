package config

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHeaderConfigValidation(t *testing.T) {
	for _, extra := range []string{
		"auth: typo",
		"classifier: typo",
		"auth: headers",
		"headers: {'Bad Header': {env: KEY}}",
		"headers: {Host: {env: KEY}}",
		"headers: {Authorization: {env: KEY}, authorization: {env: OTHER}}",
		"headers: {Authorization: {prefix: 'KakaoAK '}}",
		`headers: {Authorization: {env: KEY, prefix: "bad\r\n"}}`,
		"json_expect: [{path: a}]",
		"json_expect: [{path: a..b, min: 1}]",
		"json_expect: [{path: a, min: 2, max: 1}]",
		"json_expect: [{path: a, min_items: -1}]",
		"json_expect: [{path: a, min: .nan}]",
		"artifact: {sha256: bad, min_bytes: 1, max_bytes: 100}",
		"classifier: http\n    artifact: {sha256: '" + strings.Repeat("a", 64) + "', min_bytes: 1, max_bytes: 1048577}",
		"classifier: http\n    artifact: {sha256: '" + strings.Repeat("a", 64) + "', min_bytes: 0, max_bytes: 100}",
		"classifier: http\n    artifact: {sha256: '" + strings.Repeat("a", 64) + "', min_bytes: 10, max_bytes: 1}",
		"classifier: http\n    json_expect: [{path: a, min: 1}]\n    artifact: {sha256: '" + strings.Repeat("a", 64) + "', min_bytes: 1, max_bytes: 100}",
	} {
		if _, err := Parse([]byte("targets:\n  - id: a\n    url: https://example.com\n    " + extra + "\n")); err == nil {
			t.Errorf("accepted %s", extra)
		}
	}
}

func TestHeaderCredentialsAndRedaction(t *testing.T) {
	t.Setenv("DATA_GO_KR_KEY", "")
	t.Setenv("CLIENT_ID", "client-id")
	t.Setenv("CLIENT_SECRET", "private+/=secret")
	c, err := Parse([]byte(`targets:
  - id: a
    auth: headers
    url: https://example.com/
    headers:
      X-Client-ID: {env: CLIENT_ID}
      Authorization: {env: CLIENT_SECRET, prefix: "Bearer "}
`))
	if err != nil {
		t.Fatal(err)
	}
	tg := c.Targets[0]
	if !tg.HasKey() {
		t.Fatal("header target depends on unrelated default key")
	}
	req, _ := http.NewRequest("GET", "https://example.com", nil)
	if err := tg.ApplyHeaders(req); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "Bearer private+/=secret" || req.Header.Get("X-Client-ID") != "client-id" {
		t.Fatal("credentials changed")
	}
	redacted := tg.Redact("client-id private+/=secret " + url.QueryEscape("private+/=secret"))
	if strings.Contains(redacted, "private") || strings.Contains(redacted, "client-id") {
		t.Fatal("header credential leaked")
	}
	t.Setenv("CLIENT_ID", "")
	if tg.HasKey() || tg.MissingCredential() != "CLIENT_ID" {
		t.Fatal("missing ID not detected")
	}
}

func TestMapTargetsUseDedicatedCredentials(t *testing.T) {
	c, err := Load("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"kakao-address": "dapi.kakao.com", "kakao-place": "dapi.kakao.com", "naver-geocode": "maps.apigw.ntruss.com", "naver-directions": "maps.apigw.ntruss.com"}
	for _, tg := range c.Targets {
		host, ok := want[tg.ID]
		if !ok {
			continue
		}
		if tg.Auth != "headers" || len(tg.JSONExpect) < 3 {
			t.Fatalf("unverified map target %s", tg.ID)
		}
		u, err := tg.Render(time.Now())
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host != host || parsed.Query().Has("serviceKey") {
			t.Fatalf("bad map URL for %s", tg.ID)
		}
		delete(want, tg.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing map targets: %v", want)
	}
}

func TestOverlappingCredentialsAreFullyRedacted(t *testing.T) {
	t.Setenv("SHORT_SECRET", "prefix")
	t.Setenv("LONG_SECRET", "prefix-private")
	tg := Target{Auth: "headers", Headers: map[string]Header{"X-Id": {Env: "SHORT_SECRET"}, "X-Key": {Env: "LONG_SECRET"}}}
	if got := tg.Redact("prefix-private prefix"); got != "REDACTED REDACTED" {
		t.Fatal(got)
	}
}
