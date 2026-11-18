package orchestrator

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type ValidationResult struct {
	Success bool
	Errors  []string
}

func (r ValidationResult) ErrorText() string {
	return strings.Join(r.Errors, "; ")
}

type OutputValidator struct{}

var validatorCodeIndicators = []string{
	"def ",
	"class ",
	"import ",
	"from ",
	"if __name__",
	"async def ",
	"await ",
	"try:",
	"except:",
	"function ",
	"const ",
	"let ",
	"=> {",
	"require(",
	"export ",
	"SELECT ",
	"INSERT ",
	"UPDATE ",
	"DELETE ",
	"DROP ",
	"<script",
	"<?php",
	"<%",
}

func (v *OutputValidator) containsCodeIndicators(value string) bool {
	return containsCodeIndicator(value, validatorCodeIndicators)
}

func (v *OutputValidator) ValidateOutputKeys(
	output map[string]any,
	expectedKeys []string,
	allowEmpty bool,
	nullableKeys []string,
) ValidationResult {
	if output == nil {
		return ValidationResult{
			Success: false,
			Errors:  []string{"Output is not a dict, got NoneType"},
		}
	}

	nullable := keySet(nullableKeys)
	errors := []string{}
	for _, key := range expectedKeys {
		value, present := output[key]
		if !present {
			if !hasKey(nullable, key) {
				errors = append(errors, fmt.Sprintf("Missing required output key: '%s'", key))
			}
			continue
		}
		if allowEmpty {
			continue
		}
		if value == nil {
			if !hasKey(nullable, key) {
				errors = append(errors, fmt.Sprintf("Output key '%s' is None", key))
			}
			continue
		}
		if str, ok := value.(string); ok && strings.TrimSpace(str) == "" {
			if !hasKey(nullable, key) {
				errors = append(errors, fmt.Sprintf("Output key '%s' is empty string", key))
			}
		}
	}
	return ValidationResult{Success: len(errors) == 0, Errors: errors}
}

func (v *OutputValidator) ValidateNoHallucination(
	output map[string]any,
	maxLength ...int,
) ValidationResult {
	limit := 50000
	if len(maxLength) > 0 {
		limit = maxLength[0]
	}

	errors := []string{}
	for _, key := range sortedKeys(output) {
		str, ok := output[key].(string)
		if !ok {
			continue
		}
		length := utf8.RuneCountInString(str)
		if length > limit {
			errors = append(errors, fmt.Sprintf(
				"Output key '%s' exceeds max length (%d > %d)", key, length, limit,
			))
		}
	}
	return ValidationResult{Success: len(errors) == 0, Errors: errors}
}

func (v *OutputValidator) ValidateSchema(
	output map[string]any,
	schema map[string]any,
) ValidationResult {
	errors := []string{}
	validateSchemaNode(output, schema, "", &errors)
	return ValidationResult{Success: len(errors) == 0, Errors: errors}
}

func (v *OutputValidator) ValidateAll(
	output map[string]any,
	expectedKeys []string,
	schema map[string]any,
	checkHallucination bool,
	nullableKeys []string,
) ValidationResult {
	all := []string{}
	if len(expectedKeys) > 0 {
		result := v.ValidateOutputKeys(output, expectedKeys, false, nullableKeys)
		all = append(all, result.Errors...)
	}
	if schema != nil {
		result := v.ValidateSchema(output, schema)
		all = append(all, result.Errors...)
	}
	if checkHallucination {
		result := v.ValidateNoHallucination(output)
		all = append(all, result.Errors...)
	}
	return ValidationResult{Success: len(all) == 0, Errors: all}
}

func validateSchemaNode(value any, schema map[string]any, path string, errors *[]string) {
	if schema == nil {
		return
	}

	if raw, ok := schema["type"]; ok {
		types := schemaTypes(raw)
		if len(types) > 0 && !matchesAnyType(value, types) {
			*errors = append(*errors, fmt.Sprintf(
				"%s: %s is not of type %s", schemaPath(path), valueRepr(value), quotedList(types),
			))
		}
	}

	if raw, ok := schema["enum"]; ok {
		if list, ok := raw.([]any); ok && !anyEqual(value, list) {
			*errors = append(*errors, fmt.Sprintf(
				"%s: %s is not one of %s", schemaPath(path), valueRepr(value), reprList(list),
			))
		}
	}

	if raw, ok := schema["const"]; ok && !valuesEqual(value, raw) {
		*errors = append(*errors, fmt.Sprintf(
			"%s: %s is not const %s", schemaPath(path), valueRepr(value), valueRepr(raw),
		))
	}

	if str, ok := value.(string); ok {
		length := utf8.RuneCountInString(str)
		if limit, ok := intKeyword(schema, "minLength"); ok && length < limit {
			*errors = append(*errors, fmt.Sprintf("%s: %s is too short", schemaPath(path), valueRepr(str)))
		}
		if limit, ok := intKeyword(schema, "maxLength"); ok && length > limit {
			*errors = append(*errors, fmt.Sprintf("%s: %s is too long", schemaPath(path), valueRepr(str)))
		}
		if pattern, ok := schema["pattern"].(string); ok {
			re, err := regexp.Compile(pattern)
			if err != nil {
				*errors = append(*errors, fmt.Sprintf(
					"%s: invalid schema pattern %s", schemaPath(path), valueRepr(pattern),
				))
			} else if !re.MatchString(str) {
				*errors = append(*errors, fmt.Sprintf(
					"%s: %s does not match %s", schemaPath(path), valueRepr(str), valueRepr(pattern),
				))
			}
		}
	}

	if number, ok := toFloat(value); ok {
		if minimum, ok := floatKeyword(schema, "minimum"); ok && number < minimum {
			*errors = append(*errors, fmt.Sprintf("%s: %s is less than the minimum of %v", schemaPath(path), valueRepr(value), minimum))
		}
		if maximum, ok := floatKeyword(schema, "maximum"); ok && number > maximum {
			*errors = append(*errors, fmt.Sprintf("%s: %s is greater than the maximum of %v", schemaPath(path), valueRepr(value), maximum))
		}
	}

	if object, ok := value.(map[string]any); ok {
		properties, _ := schema["properties"].(map[string]any)
		if raw, ok := schema["required"]; ok {
			for _, key := range stringList(raw) {
				if _, present := object[key]; !present {
					*errors = append(*errors, fmt.Sprintf(
						"%s: '%s' is a required property", schemaPath(path), key,
					))
				}
			}
		}
		for _, key := range sortedKeys(object) {
			if properties != nil {
				if child, ok := properties[key].(map[string]any); ok {
					validateSchemaNode(object[key], child, childPath(path, key), errors)
					continue
				}
			}
			if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
				*errors = append(*errors, fmt.Sprintf(
					"%s: Additional properties are not allowed ('%s' was unexpected)",
					schemaPath(path), key,
				))
			}
		}
	}

	if array, ok := value.([]any); ok {
		if itemSchema, ok := schema["items"].(map[string]any); ok {
			for i, item := range array {
				validateSchemaNode(item, itemSchema, childPath(path, strconv.Itoa(i)), errors)
			}
		}
	}
}

func schemaTypes(raw any) []string {
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if name, ok := item.(string); ok {
				out = append(out, name)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

func matchesAnyType(value any, types []string) bool {
	for _, name := range types {
		if matchesType(value, name) {
			return true
		}
	}
	return false
}

func matchesType(value any, name string) bool {
	switch name {
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
	case "null":
		return value == nil
	case "number":
		_, ok := toFloat(value)
		return ok
	case "integer":
		number, ok := toFloat(value)
		return ok && number == math.Trunc(number)
	}
	return true
}

func schemaPath(path string) string {
	if path == "" {
		return "root"
	}
	return path
}

func childPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func intKeyword(schema map[string]any, key string) (int, bool) {
	number, ok := toFloat(schema[key])
	if !ok {
		return 0, false
	}
	return int(number), true
}

func floatKeyword(schema map[string]any, key string) (float64, bool) {
	return toFloat(schema[key])
}

func toFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func valuesEqual(a, b any) bool {
	if an, ok := toFloat(a); ok {
		if bn, ok := toFloat(b); ok {
			return an == bn
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

func anyEqual(value any, candidates []any) bool {
	for _, candidate := range candidates {
		if valuesEqual(value, candidate) {
			return true
		}
	}
	return false
}

func stringList(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

func sortedKeys(value map[string]any) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = "'" + value + "'"
	}
	return strings.Join(quoted, ", ")
}

func reprList(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = valueRepr(value)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func valueRepr(value any) string {
	var buf strings.Builder
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Sprintf("%v", value)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}
