package probe

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"sort"
	"strings"
)

// Shape returns the field paths of a response body, without values: JSON object keys
// ("response.body.items.item[].dataTime") or XML element paths. It is how schema changes
// are noticed: a provider renaming or dropping a field breaks every client while the API
// stays "up". Arrays are merged (the union of the first 50 elements), so the order and
// number of items don't matter.
//
// Nil when the body carries no data to learn from: an empty item list ("items":"" or
// totalCount 0, as data.go.kr sends for "no results"), the data.go.kr gateway's error
// envelope (OpenAPI_ServiceResponse: its fields are the gateway's, not the API's), or a
// body that isn't JSON or XML.
func Shape(body []byte) []string {
	b := bytes.TrimSpace(body)
	if len(b) == 0 {
		return nil
	}
	set := map[string]bool{}
	var empty bool
	switch b[0] {
	case '{', '[':
		var v any
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		if dec.Decode(&v) != nil {
			return nil
		}
		empty = walkJSON(v, "", set)
	case '<':
		empty = walkXML(b, set)
	default:
		return nil
	}
	if empty || len(set) == 0 {
		return nil
	}
	for p := range set {
		if strings.HasPrefix(p, "OpenAPI_ServiceResponse") {
			return nil
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// walkJSON records leaf paths; reports whether the payload says it has no results.
func walkJSON(v any, path string, set map[string]bool) (empty bool) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			return false
		}
		for k, child := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			lk := strings.ToLower(k)
			if lk == "totalcount" || lk == "list_total_count" || lk == "total" {
				if n, ok := child.(json.Number); ok && n.String() == "0" {
					empty = true
				}
			}
			if lk == "items" {
				if s, ok := child.(string); ok && s == "" {
					empty = true
				}
			}
			if walkJSON(child, p, set) {
				empty = true
			}
		}
	case []any:
		for i, child := range x {
			if i == 50 {
				break
			}
			if walkJSON(child, path+"[]", set) {
				empty = true
			}
		}
	default:
		if path != "" {
			set[path] = true
		}
	}
	return empty
}

// walkXML records element paths that hold text; reports "no results".
func walkXML(b []byte, set map[string]bool) (empty bool) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	var stack []string
	var text strings.Builder
	children := []int{0}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return true // malformed: don't learn from it
		}
		switch t := tok.(type) {
		case xml.StartElement:
			stack = append(stack, t.Name.Local)
			children[len(children)-1]++
			children = append(children, 0)
			text.Reset()
		case xml.CharData:
			text.Write(t)
		case xml.EndElement:
			p := strings.Join(stack, ".")
			leaf := children[len(children)-1] == 0
			if leaf {
				set[p] = true
				name := strings.ToLower(t.Name.Local)
				if (name == "totalcount" || name == "list_total_count") && strings.TrimSpace(text.String()) == "0" {
					empty = true
				}
				if name == "items" && strings.TrimSpace(text.String()) == "" {
					empty = true
				}
			}
			children = children[:len(children)-1]
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			text.Reset()
		}
	}
	return empty
}
