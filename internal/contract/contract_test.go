package contract

import (
	"reflect"
	"testing"

	"github.com/hyunsuhahaha/public-api-monitor/internal/probe"
)

const spec = `
openapi: 3.0.3
info: {title: shop, version: "1"}
servers: [{url: https://api.example.com/v1}]
paths:
  /items/{id}:
    get:
      responses:
        "200":
          content:
            application/json:
              schema: {$ref: "#/components/schemas/Item"}
  /items:
    get:
      responses:
        "200":
          content:
            application/json:
              schema:
                type: object
                required: [items]
                properties:
                  items: {type: array, items: {$ref: "#/components/schemas/Item"}}
components:
  schemas:
    Item:
      type: object
      required: [id, price]
      properties:
        id: {type: integer}
        price: {type: number}
        name: {type: string}
        meta: {type: object}
        parent: {$ref: "#/components/schemas/Item"}
`

func TestCompareWithDocs(t *testing.T) {
	s, err := Parse([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	doc, tmpl, err := s.Expected("/v1/items/42")
	if err != nil || tmpl != "/items/{id}" {
		t.Fatalf("%s %v", tmpl, err)
	}
	body := []byte(`{"id":42,"price":"9.90","meta":{"a":1},"sku":"X1"}`)
	d := Compare(doc, []map[string]string{probe.ShapeTypes(body)})
	want := Diff{Optional: []string{"name"}, Undocumented: []string{"sku"}, Mismatch: []string{"price: 문서 number, 실제 string"}}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("%+v", d)
	}
	list, tmpl, _ := s.Expected("/v1/items")
	if tmpl != "/items" || !list["items[].price"].Required || list["items[].name"].Required {
		t.Fatalf("%s %+v", tmpl, list)
	}
	d = Compare(list, []map[string]string{probe.ShapeTypes([]byte(`{"items":[{"id":1}]}`))})
	if !reflect.DeepEqual(d.Missing, []string{"items[].price"}) {
		t.Fatalf("%+v", d)
	}
	if _, _, err := s.Expected("/v1/orders"); err == nil {
		t.Fatal("no such operation")
	}
	if _, err := Parse([]byte(`{"a":1}`)); err == nil {
		t.Fatal("not a spec")
	}
}
