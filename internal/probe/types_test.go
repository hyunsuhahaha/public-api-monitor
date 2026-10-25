package probe

import (
	"reflect"
	"testing"
)

func TestShapeTypes(t *testing.T) {
	got := ShapeTypes([]byte(`{"status":"0000","data":{"price":42,"ok":true,"note":null,"tags":[],"items":[{"v":null},{"v":"x"}]}}`))
	want := map[string]string{"status": "string", "data.price": "number", "data.ok": "bool", "data.note": "null",
		"data.tags": "empty_array", "data.items": "array", "data.items[].v": "string"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, body := range []string{`<a><b>1</b></a>`, `{"response":{"body":{"items":"","totalCount":0}}}`, `not json`} {
		if ShapeTypes([]byte(body)) != nil {
			t.Errorf("%s: want nil", body)
		}
	}
}
