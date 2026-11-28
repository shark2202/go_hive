package integrations

// Response envelopes for the six declarative REST integrations.
//
// The upstream Python modules do not hand the provider body back to the model
// verbatim: GitHub wraps everything in {"success":true,"data":...}, Slack and
// Airtable rebuild small projections, Trello names its arrays, Stripe formats
// each resource, and Telegram returns its ok/result envelope. The Go REST
// kernel used to return the wire body for every module (audit item A34).
//
// Each module declares an explicit shape per action below. An action that
// upstream really does return raw (Slack's file/user lookups) registers
// rawShape on purpose, so nothing falls back to "provider body" by accident.

// shapeFunc mirrors RestSpec.Shape.
type shapeFunc func(parsed any, input map[string]any) any

// withShapes attaches the module's per-action formatters to its spec set.
// Specs without an entry keep the kernel default and are reported by the
// six-module shape coverage test.
func withShapes(specs map[string]RestSpec, shapes map[string]shapeFunc) map[string]RestSpec {
	for action, shape := range shapes {
		spec, ok := specs[action]
		if !ok {
			continue
		}
		spec.Shape = shape
		specs[action] = spec
	}
	return specs
}

// rawShape is the explicit registration for an action whose upstream tool
// returns the provider body unchanged.
func rawShape(parsed any, _ map[string]any) any { return parsed }

// shapeMap reads a provider body as an object. A nil or non-object body reads
// as an empty object, matching the Python formatters' .get(...) defaults.
func shapeMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	if object == nil {
		return map[string]any{}
	}
	return object
}

// shapeList reads a field as an array; missing or non-array values read as an
// empty list, matching .get(key, []).
func shapeList(object map[string]any, name string) []any {
	items, ok := object[name].([]any)
	if !ok {
		return []any{}
	}
	return items
}

// shapeDict reads a field as an object; missing values read as an empty
// object, matching .get(key, {}).
func shapeDict(object map[string]any, name string) map[string]any {
	value, ok := object[name].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return value
}

// shapeAt walks a dot path through nested objects and returns nil when any
// segment is missing.
func shapeAt(object map[string]any, path string) any {
	var current any = object
	for _, segment := range splitShapePath(path) {
		container, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = container[segment]
	}
	return current
}

func splitShapePath(path string) []string {
	if path == "" {
		return nil
	}
	segments := make([]string, 0, 2)
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '.' {
			segments = append(segments, path[start:i])
			start = i + 1
		}
	}
	return append(segments, path[start:])
}

// shapeText renders a scalar the way Python's f-string/JSON encoders do:
// nil stays nil (null), strings stay strings, numbers keep their JSON form.
func shapeText(value any) any {
	switch value.(type) {
	case nil, string, float64, bool:
		return value
	}
	return value
}

// shapeTextOr applies Python's .get(key, fallback) default for string fields
// (a missing topic/purpose reads as "").
func shapeTextOr(value any, fallback string) any {
	if value == nil {
		return fallback
	}
	return shapeText(value)
}

// shapeArray reads a bare JSON array body (GitHub list endpoints); a nil or
// non-array body reads as an empty list.
func shapeArray(value any) []any {
	items, _ := value.([]any)
	if items == nil {
		return []any{}
	}
	return items
}

// shapeInt converts a decoded JSON number to int.
func shapeInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	}
	return 0
}

// shapeTruthy applies Python truthiness to a decoded JSON value (bool(msg.get
// ("bot_id")), bool(msg.get("parent_user_id"))).
func shapeTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	}
	return true
}

// shapeBool reads .get(name, fallback) truthiness.
func shapeBool(object map[string]any, name string, fallback bool) bool {
	value, present := object[name]
	if !present || value == nil {
		return fallback
	}
	boolean, ok := value.(bool)
	if !ok {
		return fallback
	}
	return boolean
}

// shapeNumber reads .get(name, 0) for numeric projections.
func shapeNumber(object map[string]any, name string) any {
	value := object[name]
	switch value.(type) {
	case float64, int, int64:
		return value
	}
	return float64(0)
}
