package executor

import "testing"

func TestValidateArgsNilSchemaAlwaysPasses(t *testing.T) {
	if err := validateArgs(nil, map[string]interface{}{"anything": "goes"}); err != nil {
		t.Fatalf("a tool with no declared schema must accept anything, got %v", err)
	}
}

func TestValidateArgsCatchesMissingRequired(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"order_id", "amount"},
		"properties": map[string]interface{}{
			"order_id": map[string]interface{}{"type": "string"},
			"amount":   map[string]interface{}{"type": "number"},
		},
	}
	err := validateArgs(schema, map[string]interface{}{"order_id": "A-1"})
	if err == nil {
		t.Fatal("expected an error for the missing required argument")
	}
}

func TestValidateArgsAcceptsAWellFormedCall(t *testing.T) {
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"order_id", "amount"},
		"properties": map[string]interface{}{
			"order_id": map[string]interface{}{"type": "string"},
			"amount":   map[string]interface{}{"type": "number"},
			"status":   map[string]interface{}{"type": "string", "enum": []interface{}{"placed", "delivered"}},
		},
	}
	err := validateArgs(schema, map[string]interface{}{"order_id": "A-1", "amount": 240.0, "status": "delivered"})
	if err != nil {
		t.Fatalf("a call matching the schema must pass, got %v", err)
	}
}

func TestValidateArgsCatchesWrongType(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"amount": map[string]interface{}{"type": "number"}},
	}
	err := validateArgs(schema, map[string]interface{}{"amount": "a lot"})
	if err == nil {
		t.Fatal("a string where a number is declared must be rejected")
	}
}

func TestValidateArgsCatchesEnumViolation(t *testing.T) {
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"status": map[string]interface{}{"type": "string", "enum": []interface{}{"placed", "delivered"}}},
	}
	err := validateArgs(schema, map[string]interface{}{"status": "cancelled"})
	if err == nil {
		t.Fatal("a value outside the declared enum must be rejected")
	}
}

func TestValidateArgsCatchesUnexpectedArgumentWhenAdditionalPropertiesIsFalse(t *testing.T) {
	schema := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]interface{}{"order_id": map[string]interface{}{"type": "string"}},
	}
	err := validateArgs(schema, map[string]interface{}{"order_id": "A-1", "extra_field": "nope"})
	if err == nil {
		t.Fatal("an argument not in the schema must be rejected when additionalProperties is false")
	}
}

func TestValidateArgsAllowsUnknownArgumentsByDefault(t *testing.T) {
	// additionalProperties omitted → the common case for hand-written tool
	// schemas — must not reject extra fields a model adds defensively.
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"order_id": map[string]interface{}{"type": "string"}},
	}
	err := validateArgs(schema, map[string]interface{}{"order_id": "A-1", "note": "fyi"})
	if err != nil {
		t.Fatalf("without additionalProperties:false, extra fields must be tolerated, got %v", err)
	}
}

func TestValidateArgsValidatesArrayItems(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"ids": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
		},
	}
	if err := validateArgs(schema, map[string]interface{}{"ids": []interface{}{"a", "b"}}); err != nil {
		t.Fatalf("array of strings must pass, got %v", err)
	}
	if err := validateArgs(schema, map[string]interface{}{"ids": []interface{}{"a", 2.0}}); err == nil {
		t.Fatal("a non-string item in a string array must be rejected")
	}
}

func TestValidateArgsToleratesNumericStringsFromModels(t *testing.T) {
	// Models sometimes emit "42" instead of 42 — tolerate it rather than fail
	// a call the real tool would parse fine either way.
	schema := map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{"amount": map[string]interface{}{"type": "number"}},
	}
	if err := validateArgs(schema, map[string]interface{}{"amount": "42"}); err != nil {
		t.Fatalf("a numeric string should be tolerated, got %v", err)
	}
}
