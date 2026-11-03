package refcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// Decode reads a JSON request body into dst the way the API always has (fields it does not know are
// skipped) and reports, as problems, what a plain decode hides: a field no such name exists for, and a
// value of the wrong type. err is set only for a body that is not JSON at all.
//
// A client that sends "summry_model" used to get a 200 and an agent without a summary model. Now it gets the
// field's path and the names that were meant.
//
// alsoKnown are samples of other types whose top-level field names are accepted too, without being looked
// into: an update takes an edit, and a client that read the agent and sends it back carries fields (id,
// status, created_at) that are the agent's but not the edit's. Those are ignored, as they always were; a
// name that is on neither is the typo.
func Decode(raw []byte, dst interface{}, alsoKnown ...interface{}) (problems []Problem, err error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if derr := dec.Decode(dst); derr != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(derr, &typeErr) {
			path := fieldPath(typeErr.Field)
			problems = append(problems, Problem{Path: path, Code: CodeWrongType, Severity: SeverityError, Value: typeErr.Value,
				Message: fmt.Sprintf("%s: must be %s, not %s", path, typeWords(typeErr.Type), jsonKindWords(typeErr.Value))})
			// The decode went on past the mistake, so the rest of the body can still be read for unknown fields.
		} else {
			return nil, derr
		}
	}
	var tree interface{}
	if json.Unmarshal(raw, &tree) == nil {
		extra := map[string]reflect.Type{}
		for _, a := range alsoKnown {
			t := reflect.TypeOf(a)
			for t.Kind() == reflect.Ptr {
				t = t.Elem()
			}
			for n := range jsonFields(t) {
				extra[n] = anyType
			}
		}
		problems = append(problems, unknownFields("", tree, reflect.TypeOf(dst), extra)...)
	}
	return problems, nil
}

// UnknownFields reports the fields of a JSON object that the Go type sample has no such name for, at any
// depth below it. Free-form maps (an ingredient's config) are not looked into.
func UnknownFields(raw []byte, sample interface{}) []Problem {
	var tree interface{}
	if json.Unmarshal(raw, &tree) != nil {
		return nil
	}
	return unknownFields("", tree, reflect.TypeOf(sample), nil)
}

var anyType = reflect.TypeOf((*interface{})(nil)).Elem()

func fieldPath(goPath string) string {
	if goPath == "" {
		return "(body)"
	}
	return goPath
}

func unknownFields(path string, v interface{}, t reflect.Type, extra map[string]reflect.Type) []Problem {
	for t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t == nil {
		return nil
	}
	if t.Implements(unmarshalerType) || reflect.PtrTo(t).Implements(unmarshalerType) {
		return nil // it reads its own JSON (time.Time and the like)
	}
	switch val := v.(type) {
	case map[string]interface{}:
		switch t.Kind() {
		case reflect.Struct:
			known := jsonFields(t)
			for n, et := range extra {
				if _, own := known[n]; !own {
					known[n] = et
				}
			}
			var out []Problem
			keys := make([]string, 0, len(val))
			for k := range val {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			names := make([]string, 0, len(known))
			for n := range known {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, k := range keys {
				ft, ok := known[k]
				p := join(path, k)
				if !ok {
					// encoding/json matches names without regard to case; so does the API.
					if ft2, ci := caseInsensitive(known, k); ci {
						ft, ok = ft2, true
					}
				}
				if !ok {
					sugg := Nearest(k, names, maxSuggestions)
					msg := fmt.Sprintf("%s: there is no such field", p)
					if len(sugg) > 0 {
						msg = fmt.Sprintf("%s: there is no such field (did you mean %s?)", p, joinOr(sugg))
					}
					out = append(out, Problem{Path: p, Code: CodeUnknownField, Severity: SeverityError, Value: k, Message: msg, Suggestions: sugg})
					continue
				}
				out = append(out, unknownFields(p, val[k], ft, nil)...)
			}
			return out
		case reflect.Map:
			var out []Problem
			keys := make([]string, 0, len(val))
			for k := range val {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				out = append(out, unknownFields(join(path, k), val[k], t.Elem(), nil)...)
			}
			return out
		}
	case []interface{}:
		if t.Kind() == reflect.Slice || t.Kind() == reflect.Array {
			var out []Problem
			for i, el := range val {
				out = append(out, unknownFields(fmt.Sprintf("%s[%d]", path, i), el, t.Elem(), nil)...)
			}
			return out
		}
	}
	return nil
}

var unmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func caseInsensitive(known map[string]reflect.Type, k string) (reflect.Type, bool) {
	for n, t := range known {
		if strings.EqualFold(n, k) {
			return t, true
		}
	}
	return nil, false
}

// jsonFields maps the JSON names of a struct's fields (embedded structs flattened, as encoding/json does)
// to their types.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				for n, t := range jsonFields(ft) {
					out[n] = t
				}
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}

func typeWords(t reflect.Type) string {
	if t == nil {
		return "another type"
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	case reflect.Map, reflect.Struct:
		return "an object"
	}
	return t.String()
}

func jsonKindWords(v string) string {
	switch v {
	case "string":
		return "a string"
	case "number":
		return "a number"
	case "bool":
		return "true or false"
	case "array":
		return "a list"
	case "object":
		return "an object"
	}
	return v
}

// ServerFields stands for the field the server adds to every agent it sends (see models.Agent.MarshalJSON),
// so that a client that sends an agent back does not have it refused as unknown. Pass it to Decode as
// alsoKnown.
var ServerFields = struct {
	CreatedEpoch int64 `json:"created_epoch"`
}{}
