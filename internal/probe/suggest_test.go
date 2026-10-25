package probe

import "testing"

func TestSuggestRules(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"response":{"header":{"resultCode":"00","resultMsg":"NORMAL"},"body":{"items":{"item":[{"price":"10","temp":-3.5,"count":4}]},"totalCount":7}}}`),
		[]byte(`{"response":{"header":{"resultCode":"00","resultMsg":"NORMAL"},"body":{"items":{"item":[{"price":"12","temp":2,"count":5},{"price":"1"}]},"totalCount":9}}}`),
		[]byte(`<xml/>`),
	}
	var got string
	for _, r := range SuggestRules(bodies) {
		got += FormatRule(r) + "\n"
	}
	want := `{path: response.body.totalCount, min: 0}
{path: response.header.resultCode, equals: '00'}
{path: response.body.items.item, min_items: 1}
{path: response.body.items.item.0.count, min: 0}
{path: response.body.items.item.0.price, min: 0}
`
	if got != want {
		t.Fatalf("got\n%s", got)
	}
	// the rules pass on the answers they came from, and catch an empty list
	rules := SuggestRules(bodies)
	for _, b := range bodies[:2] {
		if st, why := checkJSON(b, rules); st != StatusOK {
			t.Fatalf("own answer fails: %s", why)
		}
	}
	empty := []byte(`{"response":{"header":{"resultCode":"00"},"body":{"items":{"item":[]},"totalCount":0}}}`)
	if st, _ := checkJSON(empty, rules); st != StatusStale {
		t.Fatal("an empty list passed")
	}
	if SuggestRules([][]byte{[]byte("<a/>")}) != nil {
		t.Fatal("XML: no rules")
	}
}
