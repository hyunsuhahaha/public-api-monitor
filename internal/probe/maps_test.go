package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

func TestVendorClassification(t *testing.T) {
	for _, c := range []struct {
		name, vendor, body string
		http               int
		want               Status
		code               string
	}{
		{"kakao ok", "kakao", `{"documents":[{"x":"126.978","y":"37.566"}]}`, 200, StatusOK, ""},
		{"kakao quota is not outage", "kakao", `{"code":-10,"msg":"quota"}`, 400, StatusClient, "KAKAO:-10"},
		{"kakao spending cap", "kakao", `{"code":-11}`, 400, StatusClient, "KAKAO:-11"},
		{"kakao key", "kakao", `{"code":-401}`, 401, StatusClient, "KAKAO:-401"},
		{"kakao activation", "kakao", `{"code":-3}`, 403, StatusClient, "KAKAO:-3"},
		{"kakao request", "kakao", `{"code":-2}`, 400, StatusClient, "KAKAO:-2"},
		{"kakao internal error inside 400", "kakao", `{"code":-1}`, 400, StatusError, "KAKAO:-1"},
		{"kakao maintenance", "kakao", `{"code":-7}`, 400, StatusError, "KAKAO:-7"},
		{"kakao timeout", "kakao", `{"code":-603}`, 400, StatusError, "KAKAO:-603"},
		{"kakao retired", "kakao", `{"code":-9}`, 400, StatusClient, "KAKAO:-9"},
		{"naver ok", "naver_maps", `{"status":"OK","addresses":[]}`, 200, StatusOK, ""},
		{"naver route", "naver_maps", `{"code":0,"route":{}}`, 200, StatusOK, ""},
		{"naver key", "naver_maps", `{"error":{"errorCode":"200"}}`, 401, StatusClient, "NAVER:200"},
		{"naver quota or disabled product", "naver_maps", `{"error":{"errorCode":"400"}}`, 429, StatusClient, "NAVER:400"},
		{"naver throttle", "naver_maps", `{"error":{"errorCode":"410"}}`, 429, StatusClient, "NAVER:410"},
		{"naver bad query", "naver_maps", `{"status":"INVALID_REQUEST"}`, 400, StatusClient, "NAVER:INVALID_REQUEST"},
		{"naver provider failure inside 200", "naver_maps", `{"status":"SYSTEM_ERROR"}`, 200, StatusError, "NAVER:SYSTEM_ERROR"},
		{"naver no road is config problem", "naver_maps", `{"code":2}`, 400, StatusClient, "NAVER:2"},
		{"naver routing unavailable", "naver_maps", `{"code":3}`, 400, StatusError, "NAVER:3"},
		{"invalid JSON", "kakao", `{"documents":`, 200, StatusError, ""},
		{"JSON null", "naver_maps", `null`, 200, StatusError, ""},
		{"HTML 200", "kakao", `<html>maintenance</html>`, 200, StatusError, ""},
		{"unexpected redirect", "kakao", `{}`, 302, StatusError, ""},
		{"gateway outage", "naver_maps", `bad gateway`, 502, StatusDown, ""},
		{"public codes unchanged", "public", `{"resultCode":"30"}`, 200, StatusClient, "30"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := classifyTarget(Observation{HTTPStatus: c.http, Body: []byte(c.body)}, &config.Target{Classifier: c.vendor})
			if v.Status != c.want || v.Code != c.code {
				t.Fatalf("got %+v, want %s/%s", v, c.want, c.code)
			}
		})
	}
}

func TestMapCanariesAgainstHTTP(t *testing.T) {
	cfg, err := config.Load("../../targets.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_GO_KR_KEY", "")
	t.Setenv("KAKAO_REST_API_KEY", "kakao-secret+/=")
	t.Setenv("NAVER_MAPS_CLIENT_ID", "naver-id")
	t.Setenv("NAVER_MAPS_CLIENT_SECRET", "naver-secret")
	fixtures := map[string]string{
		"kakao-address":    `{"documents":[{"x":"126.978","y":"37.566"}]}`,
		"kakao-place":      `{"documents":[{"x":"126.9706","y":"37.5547"}]}`,
		"naver-geocode":    `{"status":"OK","addresses":[{"x":"126.978","y":"37.566"}]}`,
		"naver-directions": `{"code":0,"route":{"traoptimal":[{"summary":{"distance":2500,"duration":600000}}]}}`,
	}
	for _, tg := range cfg.Targets {
		body, ok := fixtures[tg.ID]
		if !ok {
			continue
		}
		t.Run(tg.ID, func(t *testing.T) {
			hits := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				if r.URL.RawQuery != "" {
					t.Error("credentials must not enter URL")
				}
				if tg.Classifier == "kakao" {
					if r.Header.Get("Authorization") != "KakaoAK kakao-secret+/=" {
						t.Error("wrong Kakao auth")
					}
				} else if r.Header.Get("x-ncp-apigw-api-key-id") != "naver-id" || r.Header.Get("x-ncp-apigw-api-key") != "naver-secret" {
					t.Error("wrong NCP auth")
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			tg.URL = srv.URL
			// Reparse to exercise the same compiled templates as a real target.
			c, err := config.Parse([]byte("targets:\n  - id: fixture\n    auth: none\n    url: " + srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			local := c.Targets[0]
			local.Auth, local.Headers, local.Classifier, local.JSONExpect = tg.Auth, tg.Headers, tg.Classifier, tg.JSONExpect
			p := New("test")
			if r := p.Check(context.Background(), &local); r.Status != StatusOK {
				t.Fatalf("valid canary: %+v", r)
			}
			body = `{}`
			if r := p.Check(context.Background(), &local); r.Status != StatusStale {
				t.Fatalf("missing payload: %+v", r)
			}
			body = `{"documents":[{"x":"128.0","y":"37.566"}],"status":"OK","addresses":[],"code":0,"route":{}}`
			if tg.Classifier == "kakao" {
				body = `{"documents":[{"x":"128.0","y":"37.566"}]}`
			}
			if r := p.Check(context.Background(), &local); r.Status != StatusStale {
				t.Fatalf("bad data: %+v", r)
			}
			before := hits
			t.Setenv("KAKAO_REST_API_KEY", "")
			t.Setenv("NAVER_MAPS_CLIENT_SECRET", "")
			if r := p.Check(context.Background(), &local); r.Status != StatusClient || hits != before {
				t.Fatalf("missing credential sent a request: %+v", r)
			}
		})
	}
}

func TestJSONAssertions(t *testing.T) {
	rules := target(t, `targets:
  - id: a
    auth: none
    url: https://example.com
    json_expect:
      - {path: status, equals: OK}
      - {path: documents, min_items: 1}
      - {path: documents.0.x, min: 126, max: 127}
`).JSONExpect
	for _, c := range []struct {
		body string
		want Status
	}{
		{`{"status":"OK","documents":[{"x":"126.978"}]}`, StatusOK},
		{`{"status":"OK","documents":[{"x":126.978}]}`, StatusOK},
		{`{"status":"OK","documents":[]}`, StatusStale},
		{`{"status":"OK","documents":[{"x":"NaN"}]}`, StatusStale},
		{`{"status":"OK","documents":[{"x":"+Inf"}]}`, StatusStale},
		{`{"status":"OK","documents":[{"x":true}]}`, StatusStale},
		{`{"status":"OK","documents":[{"x":null}]}`, StatusStale},
		{`{"status":"OK","documents":[{"x":128}]}`, StatusStale},
		{`{"status":"CHANGED","documents":[{"x":126}]}`, StatusStale},
		{`{"status":"OK"}`, StatusStale},
		{`{"status":"OK"} trailing`, StatusError},
	} {
		if got, reason := checkJSON([]byte(c.body), rules); got != c.want {
			t.Errorf("%s: %s (%s), want %s", c.body, got, reason, c.want)
		}
	}
}

func TestHeaderSecretRedactionAndPublicTarget(t *testing.T) {
	t.Setenv("DATA_GO_KR_KEY", "")
	t.Setenv("HEADER_SECRET", "private+secret/=")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"resultCode":"99","resultMsg":"private+secret/="}`)
	}))
	defer srv.Close()
	tg := target(t, "targets:\n  - id: a\n    auth: headers\n    headers:\n      X-Api-Key: {env: HEADER_SECRET}\n    url: "+srv.URL)
	r := New("test").Check(context.Background(), tg)
	if strings.Contains(r.Reason, "private") || !strings.Contains(r.Reason, "REDACTED") {
		t.Fatalf("leaked response: %+v", r)
	}
	public := target(t, "targets:\n  - id: b\n    auth: none\n    url: "+srv.URL)
	if r := New("test").Check(context.Background(), public); r.HTTPStatus != 200 {
		t.Fatalf("public request blocked: %+v", r)
	}
	t.Setenv("HEADER_SECRET", "secret\r\nInjected: bad")
	if r := New("test").Check(context.Background(), tg); r.Status != StatusClient || strings.Contains(r.Reason, "secret") {
		t.Fatalf("unsafe header: %+v", r)
	}
}

func TestHeaderCredentialsStayOnOrigin(t *testing.T) {
	client := New("test").Client
	origin, _ := http.NewRequest("GET", "https://example.com", nil)
	downgrade, _ := http.NewRequest("GET", "http://example.com", nil)
	if client.CheckRedirect(downgrade, []*http.Request{origin}) != http.ErrUseLastResponse {
		t.Fatal("credential request can downgrade to HTTP")
	}
	if err := client.CheckRedirect(origin, []*http.Request{origin}); err != nil {
		t.Fatal("same-origin redirect blocked", err)
	}
	for _, header := range []string{"Authorization", "x-ncp-apigw-api-key"} {
		t.Run(header, func(t *testing.T) {
			t.Setenv("REDIRECT_SECRET", "never-forward")
			reached := false
			dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
			defer dest.Close()
			src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, dest.URL, 302) }))
			defer src.Close()
			tg := target(t, "targets:\n  - id: a\n    auth: headers\n    classifier: kakao\n    headers:\n      "+header+": {env: REDIRECT_SECRET}\n    url: "+src.URL)
			New("test").Check(context.Background(), tg)
			if reached {
				t.Fatal("credential-bearing redirect crossed origin")
			}
		})
	}
	// Transport errors and slow responses retain their ordinary meaning.
	v := classifyTarget(Observation{Timeout: true}, &config.Target{Classifier: "kakao"})
	if v.Status != StatusDown {
		t.Fatal(v)
	}
	v = classifyTarget(Observation{HTTPStatus: 200, Body: []byte(`{"documents":[]}`), Latency: time.Second}, &config.Target{Classifier: "kakao", SlowAfter: config.Duration{Duration: time.Millisecond}})
	if v.Status != StatusSlow {
		t.Fatal(v)
	}
}
