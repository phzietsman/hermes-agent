package tools

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

// Args are a tool call's decoded arguments. Numbers are json.Number so integers keep their exact text.
type Args map[string]any

// Coerce repairs string-typed values the model emitted against the tool's JSON Schema: "42" for an integer,
// "true" for a boolean, a JSON-encoded string for an array or object (also nested inside containers), a bare
// scalar where an array is expected. It is schema-guided and conservative: a value is only replaced when the
// repair is unambiguous. args is modified in place and returned.
func Coerce(schema map[string]any, args Args) Args {
	if len(args) == 0 {
		return args
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return args
	}
	for key, value := range args {
		prop, _ := props[key].(map[string]any)
		if len(prop) == 0 {
			continue
		}
		expected := prop["type"]
		_, isContainer := value.([]any)
		if expected == "array" && value != nil && !isContainer {
			args[key] = coerceToArray(value, prop)
			continue
		}
		str, isString := value.(string)
		if !isString {
			_, isObject := value.(map[string]any)
			if (expected == "array" && isContainer) || (expected == "object" && isObject) {
				args[key], _ = normalizeJSONStrings(value, prop)
			}
			continue
		}
		if expected == nil && !allowsNull(prop) {
			continue
		}
		if coerced, changed := coerceValue(str, expected, prop); changed {
			switch coerced.(type) {
			case []any, map[string]any:
				coerced, _ = normalizeJSONStrings(coerced, prop)
			}
			args[key] = coerced
		}
	}
	return args
}

// coerceToArray wraps a bare value for an array schema. A string is tried as JSON first, so an encoded array is
// parsed and a nullable "null" becomes nil rather than ["null"].
func coerceToArray(value any, prop map[string]any) any {
	if s, ok := value.(string); ok {
		if coerced, changed := coerceValue(s, "array", prop); changed {
			return coerced
		}
	}
	return []any{value}
}

func acceptsKind(schema any, kind string) bool {
	m, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	switch t := m["type"].(type) {
	case string:
		if t == kind {
			return true
		}
	case []any:
		for _, x := range t {
			if x == kind {
				return true
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if branches, ok := m[key].([]any); ok {
			for _, b := range branches {
				if acceptsKind(b, kind) {
					return true
				}
			}
		}
	}
	return false
}

// normalizeJSONStrings recursively parses JSON-encoded strings where the schema expects an array or object, so
// legitimate JSON-looking type:string fields survive. changed is false when nothing was rewritten.
func normalizeJSONStrings(value any, schema any) (any, bool) {
	sm, ok := schema.(map[string]any)
	if !ok {
		return value, false
	}
	changed := false
	if s, isStr := value.(string); isStr {
		trimmed := strings.TrimSpace(s)
		expectsArray, expectsObject := acceptsKind(sm, "array"), acceptsKind(sm, "object")
		if !((expectsArray && strings.HasPrefix(trimmed, "[")) || (expectsObject && strings.HasPrefix(trimmed, "{"))) {
			return value, false
		}
		parsed, err := decodeJSON(trimmed)
		if err != nil {
			return value, false
		}
		_, isList := parsed.([]any)
		_, isDict := parsed.(map[string]any)
		if !((isList && expectsArray) || (isDict && expectsObject)) {
			return value, false
		}
		value, changed = parsed, true
	}
	switch v := value.(type) {
	case []any:
		items, ok := sm["items"].(map[string]any)
		if !ok {
			return value, changed
		}
		out := make([]any, len(v))
		any2 := false
		for i, item := range v {
			var c bool
			out[i], c = normalizeJSONStrings(item, items)
			any2 = any2 || c
		}
		if any2 {
			return out, true
		}
	case map[string]any:
		props, ok := sm["properties"].(map[string]any)
		if !ok {
			return value, changed
		}
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = x
		}
		any2 := false
		for k, ps := range props {
			if x, present := v[k]; present {
				if _, isMap := ps.(map[string]any); isMap {
					var c bool
					out[k], c = normalizeJSONStrings(x, ps)
					any2 = any2 || c
				}
			}
		}
		if any2 {
			return out, true
		}
	}
	return value, changed
}

// coerceValue converts a string to expected (a type name or a union list). changed is false when the original
// is kept.
func coerceValue(value string, expected any, schema map[string]any) (any, bool) {
	if allowsNull(schema) && strings.EqualFold(strings.TrimSpace(value), "null") {
		return nil, true
	}
	if list, ok := expected.([]any); ok {
		for _, t := range list {
			if r, changed := coerceValue(value, t, schema); changed {
				return r, true
			}
		}
		return value, false
	}
	switch expected {
	case "integer":
		return coerceNumber(value, true)
	case "number":
		return coerceNumber(value, false)
	case "boolean":
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return value, false
	case "array":
		return coerceJSON(value, true)
	case "object":
		return coerceJSON(value, false)
	case "null":
		if strings.EqualFold(strings.TrimSpace(value), "null") {
			return nil, true
		}
	}
	return value, false
}

func allowsNull(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	switch t := schema["type"].(type) {
	case string:
		if t == "null" {
			return true
		}
	case []any:
		for _, x := range t {
			if x == "null" {
				return true
			}
		}
	}
	if schema["nullable"] == true {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if variants, ok := schema[key].([]any); ok {
			for _, v := range variants {
				if vm, ok := v.(map[string]any); ok && vm["type"] == "null" {
					return true
				}
			}
		}
	}
	return false
}

func decodeJSON(s string) (any, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errTrailing
	}
	return v, nil
}

var errTrailing = errors.New("trailing data after JSON value")

func coerceJSON(value string, wantList bool) (any, bool) {
	parsed, err := decodeJSON(value)
	if err != nil {
		return value, false
	}
	switch parsed.(type) {
	case []any:
		return parsed, wantList
	case map[string]any:
		return parsed, !wantList
	}
	return value, false
}

// decimalRe is a decimal number as Python's float() reads it: optional sign, digits with single underscores
// between them, optional fraction and exponent. Hex floats, which Go would also accept, are not numbers here.
var decimalRe = regexp.MustCompile(`^[+-]?(?:\d+(?:_\d+)*(?:\.(?:\d+(?:_\d+)*)?)?|\.\d+(?:_\d+)*)(?:[eE][+-]?\d+(?:_\d+)*)?$`)

// coerceNumber parses value as a number; the original string stays on failure, a non-finite result, or a
// fractional value when only an integer is wanted.
func coerceNumber(value string, integerOnly bool) (any, bool) {
	t := strings.TrimSpace(value)
	if !decimalRe.MatchString(t) {
		return value, false
	}
	f, err := parseFloat(strings.ReplaceAll(t, "_", ""))
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return value, false
	}
	if f == math.Trunc(f) {
		i, _ := new(big.Float).SetFloat64(f).Int(nil)
		return json.Number(i.String()), true
	}
	if integerOnly {
		return value, false
	}
	b, _ := json.Marshal(f)
	return json.Number(string(b)), true
}

func parseFloat(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
