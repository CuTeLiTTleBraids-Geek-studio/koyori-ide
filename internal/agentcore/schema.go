package agentcore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
)

// schema is intentionally a deliberately small, closed JSON Schema subset.
// Agent tools only need object/array/scalar structural validation today. A
// permissive unknown-keyword implementation would turn an unsupported schema
// feature into an authorization bypass, so compileSchema rejects anything this
// validator cannot enforce.
type schema struct {
	Type                 string
	Properties           map[string]*schema
	Required             map[string]struct{}
	AdditionalProperties bool
	Items                *schema
	Enum                 []any
	MinLength            *int
	MinItems             *int
	MaxItems             *int
}

func compileSchema(raw json.RawMessage) (*schema, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("input schema is not valid JSON: %w", ErrInvalidToolDef)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("input schema must be an object: %w", ErrInvalidToolDef)
	}
	compiled, err := compileSchemaObject(object, "input schema")
	if err != nil {
		return nil, err
	}
	if compiled.Type != "object" {
		return nil, fmt.Errorf("input schema root must have type object: %w", ErrInvalidToolDef)
	}
	return compiled, nil
}

func compileSchemaObject(raw map[string]any, location string) (*schema, error) {
	allowed := map[string]bool{
		"type": true, "properties": true, "required": true,
		"additionalProperties": true, "items": true, "enum": true,
		"minLength": true, "minItems": true, "maxItems": true,
		// Annotation-only keywords are retained in the catalog/native schema
		// but do not alter validation semantics.
		"title": true, "description": true, "default": true, "examples": true,
	}
	for key := range raw {
		if !allowed[key] {
			return nil, fmt.Errorf("%s uses unsupported keyword %q: %w", location, key, ErrInvalidToolDef)
		}
	}
	typeName, ok := raw["type"].(string)
	if !ok || !isSupportedSchemaType(typeName) {
		return nil, fmt.Errorf("%s needs a supported type: %w", location, ErrInvalidToolDef)
	}
	compiled := &schema{Type: typeName}
	if enum, exists := raw["enum"]; exists {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("%s enum must be a non-empty array: %w", location, ErrInvalidToolDef)
		}
		compiled.Enum = append([]any(nil), values...)
	}

	switch typeName {
	case "object":
		// Closed input schemas are mandatory. Dynamic MCP schemas that permit
		// unknown fields must be normalized by their owning adapter before they
		// are admitted to the catalog.
		additional, ok := raw["additionalProperties"].(bool)
		if !ok || additional {
			return nil, fmt.Errorf("%s must set additionalProperties to false: %w", location, ErrInvalidToolDef)
		}
		compiled.AdditionalProperties = additional
		if properties, exists := raw["properties"]; exists {
			propertyMap, ok := properties.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%s.properties must be an object: %w", location, ErrInvalidToolDef)
			}
			compiled.Properties = make(map[string]*schema, len(propertyMap))
			for name, propertyRaw := range propertyMap {
				if strings.TrimSpace(name) == "" {
					return nil, fmt.Errorf("%s.properties has empty name: %w", location, ErrInvalidToolDef)
				}
				propertyObject, ok := propertyRaw.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("%s.properties.%s must be an object: %w", location, name, ErrInvalidToolDef)
				}
				property, err := compileSchemaObject(propertyObject, location+".properties."+name)
				if err != nil {
					return nil, err
				}
				compiled.Properties[name] = property
			}
		} else {
			compiled.Properties = map[string]*schema{}
		}
		if required, exists := raw["required"]; exists {
			requiredItems, ok := required.([]any)
			if !ok {
				return nil, fmt.Errorf("%s.required must be an array: %w", location, ErrInvalidToolDef)
			}
			compiled.Required = make(map[string]struct{}, len(requiredItems))
			for _, item := range requiredItems {
				name, ok := item.(string)
				if !ok || name == "" {
					return nil, fmt.Errorf("%s.required must contain non-empty strings: %w", location, ErrInvalidToolDef)
				}
				if _, defined := compiled.Properties[name]; !defined {
					return nil, fmt.Errorf("%s.required references undefined property %q: %w", location, name, ErrInvalidToolDef)
				}
				if _, duplicate := compiled.Required[name]; duplicate {
					return nil, fmt.Errorf("%s.required repeats property %q: %w", location, name, ErrInvalidToolDef)
				}
				compiled.Required[name] = struct{}{}
			}
		} else {
			compiled.Required = map[string]struct{}{}
		}
	case "array":
		items, exists := raw["items"]
		if !exists {
			return nil, fmt.Errorf("%s array schema needs items: %w", location, ErrInvalidToolDef)
		}
		itemObject, ok := items.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s.items must be an object: %w", location, ErrInvalidToolDef)
		}
		item, err := compileSchemaObject(itemObject, location+".items")
		if err != nil {
			return nil, err
		}
		compiled.Items = item
		minItems, maxItems, err := compileArrayBounds(raw, location)
		if err != nil {
			return nil, err
		}
		compiled.MinItems = minItems
		compiled.MaxItems = maxItems
	case "string":
		if minLength, exists := raw["minLength"]; exists {
			value, ok := nonNegativeInt(minLength)
			if !ok {
				return nil, fmt.Errorf("%s.minLength must be a non-negative integer: %w", location, ErrInvalidToolDef)
			}
			compiled.MinLength = &value
		}
	case "boolean", "number", "integer", "null":
		// No type-specific constraints currently supported.
	}
	return compiled, nil
}

func compileArrayBounds(raw map[string]any, location string) (*int, *int, error) {
	var min, max *int
	if rawMin, exists := raw["minItems"]; exists {
		value, ok := nonNegativeInt(rawMin)
		if !ok {
			return nil, nil, fmt.Errorf("%s.minItems must be a non-negative integer: %w", location, ErrInvalidToolDef)
		}
		min = &value
	}
	if rawMax, exists := raw["maxItems"]; exists {
		value, ok := nonNegativeInt(rawMax)
		if !ok {
			return nil, nil, fmt.Errorf("%s.maxItems must be a non-negative integer: %w", location, ErrInvalidToolDef)
		}
		max = &value
	}
	if min != nil && max != nil && *min > *max {
		return nil, nil, fmt.Errorf("%s minItems exceeds maxItems: %w", location, ErrInvalidToolDef)
	}
	return min, max, nil
}

func isSupportedSchemaType(value string) bool {
	switch value {
	case "object", "array", "string", "boolean", "number", "integer", "null":
		return true
	default:
		return false
	}
}

func nonNegativeInt(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	if err != nil || parsed < 0 || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func canonicalArguments(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decodeJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("arguments are not a single JSON value: %w", ErrInvalidArguments)
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("arguments must be an object: %w", ErrInvalidArguments)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("canonicalize arguments: %w", ErrInvalidArguments)
	}
	return json.RawMessage(canonical), nil
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func validateArguments(rawSchema json.RawMessage, rawArguments json.RawMessage) error {
	compiled, err := compileSchema(rawSchema)
	if err != nil {
		return fmt.Errorf("compiled catalog has invalid schema: %w", ErrInvalidArguments)
	}
	value, err := decodeJSON(rawArguments)
	if err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", ErrInvalidArguments)
	}
	if err := compiled.validate(value, "arguments"); err != nil {
		return err
	}
	return nil
}

func (s *schema) validate(value any, location string) error {
	if !matchesSchemaType(s.Type, value) {
		return fmt.Errorf("%s must be %s: %w", location, s.Type, ErrInvalidArguments)
	}
	if len(s.Enum) > 0 && !matchesEnum(value, s.Enum) {
		return fmt.Errorf("%s is not in enum: %w", location, ErrInvalidArguments)
	}

	switch s.Type {
	case "object":
		object := value.(map[string]any)
		for required := range s.Required {
			if _, exists := object[required]; !exists {
				return fmt.Errorf("%s.%s is required: %w", location, required, ErrInvalidArguments)
			}
		}
		for name, property := range object {
			child, exists := s.Properties[name]
			if !exists {
				return fmt.Errorf("%s.%s is not allowed: %w", location, name, ErrInvalidArguments)
			}
			if err := child.validate(property, location+"."+name); err != nil {
				return err
			}
		}
	case "array":
		array := value.([]any)
		if s.MinItems != nil && len(array) < *s.MinItems {
			return fmt.Errorf("%s needs at least %d items: %w", location, *s.MinItems, ErrInvalidArguments)
		}
		if s.MaxItems != nil && len(array) > *s.MaxItems {
			return fmt.Errorf("%s allows at most %d items: %w", location, *s.MaxItems, ErrInvalidArguments)
		}
		for index, item := range array {
			if err := s.Items.validate(item, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
	case "string":
		if s.MinLength != nil && len([]rune(value.(string))) < *s.MinLength {
			return fmt.Errorf("%s needs at least %d characters: %w", location, *s.MinLength, ErrInvalidArguments)
		}
	}
	return nil
}

func matchesSchemaType(typeName string, value any) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, ok = new(big.Int).SetString(string(number), 10)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}

func matchesEnum(value any, enum []any) bool {
	canonical, err := json.Marshal(value)
	if err != nil {
		return false
	}
	for _, option := range enum {
		candidate, err := json.Marshal(option)
		if err == nil && bytes.Equal(canonical, candidate) {
			return true
		}
	}
	return false
}
