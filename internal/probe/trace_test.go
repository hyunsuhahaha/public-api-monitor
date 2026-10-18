package probe

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// The service key must never reach a span: data.go.kr puts it in the query, Seoul's open
// API in the path (/{key}/json/...). Found when the first traces showed the path.
func TestSpansNeverCarryTheKey(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"RESULT":{"CODE":"INFO-000"}}`)
	}))
	defer srv.Close()
	const key = "Sup3rS3cretKey"
	t.Setenv("PAMON_TRACE_KEY", key)
	p := New("test")
	for _, url := range []string{srv.URL + "/api?serviceKey={{.Key}}", srv.URL + "/{{.Key}}/json/bikeList/1/5/"} {
		tg := target(t, "targets:\n  - id: a\n    key_env: PAMON_TRACE_KEY\n    url: "+url+"\n")
		if r := p.Check(context.Background(), tg); r.Status != StatusOK {
			t.Fatalf("%s: %+v", url, r)
		}
	}
	spans := rec.Ended()
	if len(spans) != 2 {
		t.Fatalf("want 2 HTTP spans, got %d", len(spans))
	}
	for _, s := range spans {
		if strings.Contains(s.Name(), key) {
			t.Errorf("span name has the key: %s", s.Name())
		}
		for _, a := range s.Attributes() {
			if strings.Contains(a.Value.Emit(), key) {
				t.Errorf("attribute %s has the key: %s", a.Key, a.Value.Emit())
			}
		}
	}
}
