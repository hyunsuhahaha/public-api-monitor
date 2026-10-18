package probe

import (
	"reflect"
	"testing"
)

func TestShapeJSON(t *testing.T) {
	body := `{"response":{"header":{"resultCode":"00","resultMsg":"NORMAL"},"body":{"items":{"item":[
		{"dataTime":"2026-10-19 10:00","pm10Value":"31"},
		{"dataTime":"2026-10-19 10:00","pm10Value":"28","khaiGrade":"1"}]},"totalCount":2}}}`
	want := []string{
		"response.body.items.item[].dataTime", "response.body.items.item[].khaiGrade", "response.body.items.item[].pm10Value",
		"response.body.totalCount", "response.header.resultCode", "response.header.resultMsg",
	}
	if got := Shape([]byte(body)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	// values and item count don't change the shape
	other := `{"response":{"header":{"resultCode":"00","resultMsg":"x"},"body":{"items":{"item":[{"dataTime":"y","pm10Value":"1","khaiGrade":"2"}]},"totalCount":1}}}`
	if got := Shape([]byte(other)); !reflect.DeepEqual(got, want) {
		t.Fatalf("values changed the shape: %v", got)
	}
}

func TestShapeXML(t *testing.T) {
	body := `<?xml version="1.0"?><response><header><resultCode>00</resultCode></header><body><items>
		<item><locdate>20261019</locdate><sunrise>0645</sunrise></item></items><totalCount>1</totalCount></body></response>`
	want := []string{"response.body.items.item.locdate", "response.body.items.item.sunrise", "response.body.totalCount", "response.header.resultCode"}
	if got := Shape([]byte(body)); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestShapeNoData(t *testing.T) {
	for _, body := range []string{
		`{"response":{"header":{"resultCode":"00"},"body":{"items":"","totalCount":0}}}`,
		`<response><header><resultCode>00</resultCode></header><body><items/><totalCount>0</totalCount></body></response>`,
		`{"RESULT":{"CODE":"INFO-200"},"list_total_count":0}`,
		`not a structured body`,
		``,
	} {
		if got := Shape([]byte(body)); got != nil {
			t.Errorf("%q: want nil, got %v", body, got)
		}
	}
}
