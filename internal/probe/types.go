package probe

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ShapeTypes returns the JSON value kind at each field path of a body: string, number,
// bool, null, array (with items) or empty_array. Field paths alone miss the changes that
// break clients most quietly: a price that turns from 42 into "42", a field that keeps
// its name but comes back null, a list that is suddenly always []. Nil for XML (every
// value is text) and for bodies Shape learns nothing from.
func ShapeTypes(body []byte) map[string]string {
	b := bytes.TrimSpace(body)
	if len(b) == 0 || (b[0] != '{' && b[0] != '[') || Shape(b) == nil {
		return nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return nil
	}
	out := map[string]string{}
	walkTypes(v, "", out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func walkTypes(v any, path string, out map[string]string) {
	set := func(kind string) {
		if path == "" {
			return
		}
		// arrays merge their items: keep the first kind seen, but a value beats null
		if old, ok := out[path]; ok && old != "null" {
			return
		}
		out[path] = kind
	}
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			walkTypes(child, p, out)
		}
	case []any:
		if len(x) == 0 {
			set("empty_array")
			return
		}
		set("array")
		for i, child := range x {
			if i == 50 {
				break
			}
			walkTypes(child, path+"[]", out)
		}
	case string:
		set("string")
	case json.Number:
		set("number")
	case bool:
		set("bool")
	case nil:
		set("null")
	}
}

// KindName says a value kind in Korean, for change messages.
func KindName(kind string) string {
	switch kind {
	case "string":
		return "문자열"
	case "number":
		return "숫자"
	case "bool":
		return "참/거짓"
	case "null":
		return "null"
	case "array":
		return "목록"
	case "empty_array":
		return "빈 목록"
	}
	return strings.TrimSpace(kind)
}
