package probe

import (
	"bytes"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

// SuggestRules proposes json_expect rules from healthy JSON answers of one API, so that a
// 200 with wrong or empty data is caught without writing the rules by hand:
//   - a result code that was the same in every answer (status, resultCode, ...) must stay so
//   - the first list that always had items must keep at least one
//   - up to three numbers present in every answer must stay there (and >= 0 if they were)
//
// Rules are a starting point to read before use: a value that happened to be constant in
// a few samples is not always a contract.
func SuggestRules(bodies [][]byte) []config.JSONExpect {
	var docs []any
	for _, b := range bodies {
		b = bytes.TrimSpace(b)
		if len(b) == 0 || (b[0] != '{' && b[0] != '[') {
			continue
		}
		var v any
		if json.Unmarshal(b, &v) == nil {
			docs = append(docs, v)
		}
	}
	if len(docs) == 0 {
		return nil
	}
	// leaf values (and lists) per concrete path, first array element only
	type seen struct {
		kinds  map[string]bool
		values map[string]bool
		minNum float64
		items  int  // smallest list length
		numStr bool // every string value was a number ("98000000")
		n      int
	}
	paths := map[string]*seen{}
	var walk func(v any, path string, depth int)
	walk = func(v any, path string, depth int) {
		if depth > 12 {
			return
		}
		rec := func(kind string, val string, num float64, items int) {
			s := paths[path]
			if s == nil {
				s = &seen{kinds: map[string]bool{}, values: map[string]bool{}, minNum: num, items: items, numStr: true}
				paths[path] = s
			}
			s.n++
			s.kinds[kind] = true
			s.values[val] = true
			s.minNum = min(s.minNum, num)
			s.items = min(s.items, items)
		}
		join := func(k string) string {
			if path == "" {
				return k
			}
			return path + "." + k
		}
		switch x := v.(type) {
		case map[string]any:
			for k, c := range x {
				walk(c, join(k), depth+1)
			}
		case []any:
			if path != "" {
				rec("array", "", 0, len(x))
			}
			if len(x) > 0 {
				walk(x[0], join("0"), depth+1)
			}
		case string:
			n, err := strconv.ParseFloat(x, 64)
			rec("string", x, n, 0)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				paths[path].numStr = false
			}
		case float64:
			rec("number", "", x, 0)
		case bool:
			rec("bool", "", 0, 0)
		}
	}
	for _, d := range docs {
		walk(d, "", 0)
	}
	all := len(docs)
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	// shallow paths first: the envelope's code before an item's field
	sort.Slice(keys, func(i, j int) bool {
		di, dj := strings.Count(keys[i], "."), strings.Count(keys[j], ".")
		if di != dj {
			return di < dj
		}
		return keys[i] < keys[j]
	})
	var out []config.JSONExpect
	codes, lists, nums := 0, 0, 0
	for _, p := range keys {
		s := paths[p]
		if s.n < all || len(s.kinds) != 1 {
			continue
		}
		last := strings.ToLower(p[strings.LastIndex(p, ".")+1:])
		switch {
		case s.kinds["string"] && len(s.values) == 1 && codes < 2 && isCodeKey(last):
			for v := range s.values {
				v := v
				out = append(out, config.JSONExpect{Path: p, Equals: &v})
			}
			codes++
		case s.kinds["array"] && s.items > 0 && lists < 1:
			one := 1
			out = append(out, config.JSONExpect{Path: p, MinItems: &one})
			lists++
		case (s.kinds["number"] || s.kinds["string"] && s.numStr) && nums < 3 && !isCodeKey(last):
			r := config.JSONExpect{Path: p}
			if s.minNum >= 0 {
				zero := 0.0
				r.Min = &zero
			} // a number that was negative too: only required to be there
			out = append(out, r)
			nums++
		}
	}
	return out
}

func isCodeKey(k string) bool {
	switch k {
	case "status", "code", "resultcode", "result_code", "rsp_cd", "returncode", "result", "ok", "success", "stat":
		return true
	}
	return strings.HasSuffix(k, "resultcode") || strings.HasSuffix(k, "_code") && strings.Contains(k, "result")
}

// FormatRule writes a rule the way targets.yaml does: {path: a.b, equals: '00'}.
func FormatRule(r config.JSONExpect) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	parts := []string{"path: " + r.Path}
	if r.Equals != nil {
		parts = append(parts, "equals: "+q(*r.Equals))
	}
	if r.Min != nil {
		parts = append(parts, "min: "+strconv.FormatFloat(*r.Min, 'f', -1, 64))
	}
	if r.Max != nil {
		parts = append(parts, "max: "+strconv.FormatFloat(*r.Max, 'f', -1, 64))
	}
	if r.MinItems != nil {
		parts = append(parts, "min_items: "+strconv.Itoa(*r.MinItems))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
