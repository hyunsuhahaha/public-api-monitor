// Package contract compares an API's real answer with what its OpenAPI (or Swagger 2)
// document promises: fields the documentation requires that never came, fields that came
// undocumented, and fields whose kind differs (documented as a number, sent as a string).
// "The docs say one thing, the API does another" is how partner integrations stall for
// days; this makes the difference a list to send the provider.
package contract

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is a parsed OpenAPI 3 or Swagger 2 document (JSON or YAML).
type Spec struct{ root map[string]any }

// Load reads a document from a file.
func Load(path string) (*Spec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

// Parse reads a document (YAML is a superset of JSON, so both parse).
func Parse(b []byte) (*Spec, error) {
	var root map[string]any
	if err := yaml.Unmarshal(b, &root); err != nil {
		return nil, fmt.Errorf("openapi: %w", err)
	}
	if root["openapi"] == nil && root["swagger"] == nil {
		return nil, fmt.Errorf("openapi: neither an OpenAPI 3 nor a Swagger 2 document")
	}
	return &Spec{root: root}, nil
}

// Field is one documented field: its kind (string, number, bool, array, object) and
// whether every object on the way to it requires it.
type Field struct {
	Kind     string
	Required bool
}

// Expected returns the fields of the JSON success answer documented for GET on a URL
// path, in probe.ShapeTypes' path form ("data.items[].price"), and the template matched.
func (s *Spec) Expected(urlPath string) (map[string]Field, string, error) {
	paths, _ := s.root["paths"].(map[string]any)
	tmpl, op := "", map[string]any(nil)
	// longest matching template first: /pets/{id} before /{any}
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		if matchPath(k, urlPath) {
			item, _ := paths[k].(map[string]any)
			if g, ok := item["get"].(map[string]any); ok {
				tmpl, op = k, g
				break
			}
		}
	}
	if op == nil {
		return nil, "", fmt.Errorf("openapi: no GET operation matches %s", urlPath)
	}
	resps, _ := op["responses"].(map[string]any)
	var resp map[string]any
	for _, code := range []string{"200", "2XX", "2xx", "201", "default"} {
		if r, ok := resps[code].(map[string]any); ok {
			resp = s.deref(r)
			break
		}
	}
	if resp == nil {
		return nil, tmpl, fmt.Errorf("openapi: %s GET documents no success response", tmpl)
	}
	schema, _ := resp["schema"].(map[string]any) // Swagger 2
	if content, ok := resp["content"].(map[string]any); ok {
		for ct, c := range content {
			if strings.Contains(ct, "json") || ct == "*/*" {
				if m, ok := c.(map[string]any); ok {
					schema, _ = m["schema"].(map[string]any)
					break
				}
			}
		}
	}
	if schema == nil {
		return nil, tmpl, fmt.Errorf("openapi: %s GET documents no JSON schema", tmpl)
	}
	out := map[string]Field{}
	s.walk(schema, "", true, out, map[string]bool{}, 0)
	return out, tmpl, nil
}

// matchPath: a template matches the URL path itself or its end (servers may add a base).
func matchPath(tmpl, p string) bool {
	segs := strings.Split(strings.Trim(tmpl, "/"), "/")
	for i, seg := range segs {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			segs[i] = "[^/]+"
		} else {
			segs[i] = regexp.QuoteMeta(seg)
		}
	}
	ok, _ := regexp.MatchString("(^|/)"+strings.Join(segs, "/")+"/?$", p)
	return ok
}

func (s *Spec) deref(m map[string]any) map[string]any {
	for i := 0; i < 10; i++ {
		ref, ok := m["$ref"].(string)
		if !ok {
			return m
		}
		next := s.lookup(ref)
		if next == nil {
			return m
		}
		m = next
	}
	return m
}

func (s *Spec) lookup(ref string) map[string]any {
	if !strings.HasPrefix(ref, "#/") {
		return nil // remote references are not fetched
	}
	var cur any = s.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	m, _ := cur.(map[string]any)
	return m
}

func (s *Spec) walk(schema map[string]any, path string, required bool, out map[string]Field, seen map[string]bool, depth int) {
	if depth > 20 || schema == nil {
		return
	}
	if ref, ok := schema["$ref"].(string); ok {
		if seen[ref] {
			return // recursive type
		}
		seen = copySet(seen)
		seen[ref] = true
		schema = s.deref(schema)
	}
	for _, k := range []string{"allOf"} {
		if list, ok := schema[k].([]any); ok {
			for _, x := range list {
				if m, ok := x.(map[string]any); ok {
					s.walk(m, path, required, out, seen, depth+1)
				}
			}
		}
	}
	for _, k := range []string{"oneOf", "anyOf"} {
		if list, ok := schema[k].([]any); ok && len(list) > 0 {
			if m, ok := list[0].(map[string]any); ok {
				s.walk(m, path, false, out, seen, depth+1) // one variant; none is required
			}
		}
	}
	typ, _ := schema["type"].(string)
	if list, ok := schema["type"].([]any); ok { // OpenAPI 3.1: ["string", "null"]
		for _, x := range list {
			if t, ok := x.(string); ok && t != "null" {
				typ = t
			}
		}
	}
	props, hasProps := schema["properties"].(map[string]any)
	if typ == "" && hasProps {
		typ = "object"
	}
	switch typ {
	case "object":
		if !hasProps {
			if path != "" {
				out[path] = Field{Kind: "object", Required: required} // free-form: anything below goes
			}
			return
		}
		req := map[string]bool{}
		if list, ok := schema["required"].([]any); ok {
			for _, x := range list {
				if n, ok := x.(string); ok {
					req[n] = true
				}
			}
		}
		for name, p := range props {
			m, ok := p.(map[string]any)
			if !ok {
				continue
			}
			child := name
			if path != "" {
				child = path + "." + name
			}
			s.walk(m, child, required && req[name], out, seen, depth+1)
		}
	case "array":
		if path != "" {
			out[path] = Field{Kind: "array", Required: required}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			s.walk(items, path+"[]", required, out, seen, depth+1)
		}
	case "integer", "number":
		set(out, path, "number", required)
	case "boolean":
		set(out, path, "bool", required)
	case "string":
		set(out, path, "string", required)
	}
}

func set(out map[string]Field, path, kind string, required bool) {
	if path != "" {
		out[path] = Field{Kind: kind, Required: required || out[path].Required}
	}
}

func copySet(m map[string]bool) map[string]bool {
	c := make(map[string]bool, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

// Diff is what differs between the documentation and the answers.
type Diff struct {
	Missing      []string // required by the documentation, absent from every answer
	Optional     []string // documented, optional, not seen (often fine)
	Undocumented []string // in the answers, not in the documentation
	Mismatch     []string // "path: 문서 number, 실제 string"
}

// Empty reports whether nothing that matters differs (Optional does not count).
func (d Diff) Empty() bool { return len(d.Missing)+len(d.Undocumented)+len(d.Mismatch) == 0 }

// Compare documented fields with the kinds seen in answers (probe.ShapeTypes of each
// answer, merged: a path is seen if any answer had it).
func Compare(doc map[string]Field, seen []map[string]string) Diff {
	actual := map[string]map[string]bool{}
	for _, m := range seen {
		for p, k := range m {
			if actual[p] == nil {
				actual[p] = map[string]bool{}
			}
			actual[p][k] = true
		}
	}
	free := func(p string) bool { // under a documented free-form object
		for d, f := range doc {
			if f.Kind == "object" && strings.HasPrefix(p, d+".") {
				return true
			}
		}
		return false
	}
	var d Diff
	for p, f := range doc {
		kinds, ok := actual[p]
		if !ok {
			if f.Kind == "object" && hasPrefix(actual, p+".") {
				continue
			}
			if f.Required {
				d.Missing = append(d.Missing, p)
			} else {
				d.Optional = append(d.Optional, p)
			}
			continue
		}
		if f.Kind == "object" {
			continue
		}
		for k := range kinds {
			got := k
			if got == "empty_array" {
				got = "array"
			}
			if got != "null" && got != f.Kind {
				d.Mismatch = append(d.Mismatch, fmt.Sprintf("%s: 문서 %s, 실제 %s", p, f.Kind, got))
			}
		}
	}
	for p := range actual {
		if _, ok := doc[p]; !ok && !free(p) {
			d.Undocumented = append(d.Undocumented, p)
		}
	}
	for _, l := range [][]string{d.Missing, d.Optional, d.Undocumented, d.Mismatch} {
		sort.Strings(l)
	}
	return d
}

func hasPrefix(m map[string]map[string]bool, prefix string) bool {
	for p := range m {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
