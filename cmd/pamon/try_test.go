package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

func TestTryProposesRulesAndComparesDocs(t *testing.T) {
	trySpacing = 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"status":"0000","data":{"items":[{"id":1,"price":"9.9"}],"total":1}}`)
	}))
	defer srv.Close()
	spec := filepath.Join(t.TempDir(), "spec.yaml")
	os.WriteFile(spec, []byte(`openapi: 3.0.0
paths:
  /v1/items:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                type: object
                required: [status, data]
                properties:
                  status: {type: string}
                  data:
                    type: object
                    required: [items, cursor]
                    properties:
                      items: {type: array, items: {type: object, properties: {id: {type: integer}, price: {type: number}}}}
                      cursor: {type: string}
`), 0o644)
	var out strings.Builder
	code := tryTarget(&config.Config{}, srv.URL+"/v1/items?page=1", 2, spec, &out)
	s := out.String()
	for _, want := range []string{"#2 ok", "판정: 정상입니다.", "{path: status, equals: '0000'}", "{path: data.items, min_items: 1}",
		"문서는 필수라는데 오지 않은 필드 1개", "data.cursor", "data.items[].price: 문서 number, 실제 string", "문서에 없는 필드 1개", "data.total",
		"targets.yaml에 넣을 항목", "classifier: http"} {
		if !strings.Contains(s, want) {
			t.Errorf("lacks %q", want)
		}
	}
	if code != 0 || t.Failed() {
		t.Fatalf("exit %d\n%s", code, s)
	}
}

func TestTryNeverPrintsAConfiguredURL(t *testing.T) {
	trySpacing = 0
	t.Setenv("TRY_KEY", "try-secret-key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[{"v":"1"}]}}}}`)
	}))
	defer srv.Close()
	cfg, err := config.Parse([]byte("targets:\n  - id: k\n    name: K\n    key_env: TRY_KEY\n    url: " + srv.URL + "/api?serviceKey={{.Key}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	tryTarget(cfg, "k", 1, "", &out)
	if s := out.String(); strings.Contains(s, "try-secret-key") || strings.Contains(s, srv.URL) || !strings.Contains(s, "resultCode, equals: '00'") {
		t.Fatalf("%s", s)
	}
}
