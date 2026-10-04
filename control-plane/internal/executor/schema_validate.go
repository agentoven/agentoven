package executor

import (
	"fmt"
	"sort"
	"strconv"
)

// validateArgs checks a tool call's arguments against the tool's declared
// JSON Schema before the call is dispatched, so a model's malformed call
// fails with a message naming exactly what is wrong instead of reaching the
// tool and failing there — or worse, silently running with the wrong shape
// of input because a missing field decoded to a zero value.
//
// This is deliberately not a general JSON Schema implementation. It covers
// the subset every MCP tool schema in this codebase actually uses: type,
// required, properties, enum, items and additionalProperties at the top
// level and one level of nesting into object/array properties. No $ref, no
// oneOf/anyOf/allOf, no format validators, no pattern. A tool whose schema
// needs more than that is validated loosely (unknown keywords are ignored)
// rather than rejected — under-validating a declared-but-unsupported
// constraint is the safe direction of failure here; over-rejecting a call
// the real tool would have accepted is not.
func validateArgs(schema map[string]interface{}, args map[string]interface{}) error {
	if schema == nil {
		return nil
	}
	return validateObject("", schema, args, 0)
}

const maxValidationDepth = 4

func validateObject(path string, schema map[string]interface{}, value interface{}, depth int) error {
	if depth > maxValidationDepth {
		return nil // stop descending rather than risk an infinite loop on a cyclic-looking schema
	}

	if t, ok := schema["type"].(string); ok && t != "" && t != "object" {
		return typeError(path, t, value)
	}

	obj, isObj := value.(map[string]interface{})
	if !isObj {
		if value == nil {
			// A missing optional object is fine; "required" below catches a
			// missing required one with a clearer message than a type error.
			return nil
		}
		return typeError(path, "object", value)
	}

	if req, ok := schema["required"].([]interface{}); ok {
		missing := make([]string, 0, len(req))
		for _, r := range req {
			name, _ := r.(string)
			if name == "" {
				continue
			}
			if _, present := obj[name]; !present {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Errorf("missing required argument(s): %v", missing)
		}
	}

	props, _ := schema["properties"].(map[string]interface{})
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional && props != nil {
		var unknown []string
		for k := range obj {
			if _, declared := props[k]; !declared {
				unknown = append(unknown, k)
			}
		}
		if len(unknown) > 0 {
			sort.Strings(unknown)
			return fmt.Errorf("unexpected argument(s) not in the tool's schema: %v", unknown)
		}
	}

	for name, raw := range props {
		propSchema, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		val, present := obj[name]
		if !present {
			continue // required-ness already checked above
		}
		fieldPath := name
		if path != "" {
			fieldPath = path + "." + name
		}
		if err := validateValue(fieldPath, propSchema, val, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func validateValue(path string, schema map[string]interface{}, value interface{}, depth int) error {
	t, _ := schema["type"].(string)

	if enum, ok := schema["enum"].([]interface{}); ok && len(enum) > 0 {
		matched := false
		for _, e := range enum {
			if fmt.Sprint(e) == fmt.Sprint(value) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("argument %q = %v is not one of the allowed values %v", path, value, enum)
		}
	}

	switch t {
	case "":
		return nil // no type constraint declared
	case "object":
		return validateObject(path, schema, value, depth)
	case "array":
		arr, ok := value.([]interface{})
		if !ok {
			return typeError(path, "array", value)
		}
		items, _ := schema["items"].(map[string]interface{})
		if items != nil {
			for i, el := range arr {
				if err := validateValue(fmt.Sprintf("%s[%d]", path, i), items, el, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return typeError(path, t, value)
		}
	case "number", "integer":
		if _, ok := value.(float64); !ok {
			// A model sometimes emits a numeric literal as a JSON string
			// ("42" instead of 42); tolerate that rather than fail a call
			// whose real tool would parse it fine either way — but a string
			// that isn't actually numeric ("a lot") is still a real mismatch.
			if s, ok := value.(string); ok {
				if _, err := strconv.ParseFloat(s, 64); err == nil {
					return nil
				}
			}
			return typeError(path, t, value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return typeError(path, t, value)
		}
	}
	return nil
}

func typeError(path string, want string, got interface{}) error {
	if path == "" {
		return fmt.Errorf("expected %s, got %T", want, got)
	}
	return fmt.Errorf("argument %q: expected %s, got %T", path, want, got)
}
