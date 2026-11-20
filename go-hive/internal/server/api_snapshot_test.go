// Response-shape contract snapshots for the deterministic endpoints.
//
// The contract baseline is the Python checkout; go_hive/contracts/api-responses
// holds the frozen shapes.  This test pins the Go handler output to the frozen
// *target* shape (a change in the Go payload fails the gate) and reports drift
// against the Python *reference* shape without failing by default
// (HIVE_CONTRACT_STRICT=1 turns the drift report into failures).
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

type frozenShape struct {
	Endpoint string            `json:"endpoint"`
	Shape    map[string]string `json:"shape"`
	Counts   map[string]int    `json:"counts"`
}

type snapshotCase struct {
	endpoint string
	path     string
}

// Parity-exempt: 响应 shape 快照门禁；基线是 go_hive/contracts/api-responses（reference=python 抓取），上游无对应测试。
func TestApiResponseShapesMatchFrozenContract(t *testing.T) {
	cases := []snapshotCase{
		{"health", "/api/health"},
		{"queen_profiles", "/api/queen/profiles"},
		{"queen_tools", "/api/queen/queen_growth/tools"},
		{"config_llm", "/api/config/llm"},
		{"skills", "/api/skills"},
	}
	shapesDir := filepath.Join("..", "..", "..", "contracts", "api-responses", "shapes")
	if _, err := os.Stat(shapesDir); err != nil {
		t.Skipf("contract snapshots not present (standalone module): %v", err)
	}

	app := newAppForTest(t, AppDeps{Home: hivehome.Home{Root: t.TempDir()}, Manager: NewSessionManager()})
	strict := os.Getenv("HIVE_CONTRACT_STRICT") == "1"

	for _, testCase := range cases {
		t.Run(testCase.endpoint, func(t *testing.T) {
			status, payload := getJSONAny(t, app, testCase.path)
			if status != http.StatusOK {
				t.Fatalf("GET %s status=%d", testCase.path, status)
			}
			live := shapeOfJSON(payload)
			target := readFrozenShape(t, filepath.Join(shapesDir, "target", testCase.endpoint+".shape.json"))
			reference := readFrozenShape(t, filepath.Join(shapesDir, "reference", testCase.endpoint+".shape.json"))

			var newFields, missingFields []string
			for field := range live {
				if _, ok := target.Shape[field]; !ok {
					newFields = append(newFields, field)
				}
			}
			for field := range target.Shape {
				if _, ok := live[field]; ok {
					continue
				}
				if arrayEmptyAt(payload, arrayContainer(field)) {
					continue
				}
				missingFields = append(missingFields, field)
			}
			sort.Strings(newFields)
			sort.Strings(missingFields)
			for _, field := range newFields {
				t.Errorf("live response has field %q not present in the frozen target snapshot — update the contract after reviewing the Python reference", field)
			}
			for _, field := range missingFields {
				t.Errorf("frozen target snapshot expects field %q but the live response omits it", field)
			}

			var drift []string
			for field := range reference.Shape {
				if _, ok := live[field]; !ok {
					if arrayEmptyAt(payload, arrayContainer(field)) {
						continue
					}
					drift = append(drift, "missing:"+field)
				}
			}
			for field := range live {
				if _, ok := reference.Shape[field]; !ok {
					drift = append(drift, "extra:"+field)
				}
			}
			sort.Strings(drift)
			if len(drift) > 0 {
				if strict {
					t.Errorf("drift from the Python reference shape (%d fields): %s", len(drift), strings.Join(drift, ", "))
				} else {
					t.Logf("reference drift (report-only, %d fields): %s", len(drift), strings.Join(drift, ", "))
				}
			}
		})
	}
}

func readFrozenShape(t *testing.T, path string) frozenShape {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read frozen shape %s: %v", path, err)
	}
	var shape frozenShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("parse frozen shape %s: %v", path, err)
	}
	return shape
}

func getJSONAny(t *testing.T, app *App, target string) (int, any) {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	var payload any
	if err := json.NewDecoder(recorder.Body).Decode(&payload); err != nil {
		t.Fatalf("decode %s body: %v", target, err)
	}
	return recorder.Code, payload
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, float32, int, int64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func shapeOfJSON(value any) map[string]string {
	out := map[string]string{}
	var walk func(value any, prefix string)
	walk = func(value any, prefix string) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				field := key
				if prefix != "" {
					field = prefix + "." + key
				}
				out[field] = jsonKind(child)
				walk(child, field)
			}
		case []any:
			for _, item := range typed {
				switch item.(type) {
				case map[string]any, []any:
					walk(item, prefix+"[]")
				default:
					out[prefix+"[]"] = jsonKind(item)
				}
			}
		}
	}
	walk(value, "")
	return out
}

func arrayContainer(field string) string {
	index := strings.LastIndex(field, "[]")
	if index < 0 {
		return ""
	}
	return field[:index]
}

// arrayEmptyAt reports whether the dotted array path resolves to an empty
// array (or cannot be resolved at all), which makes element-shaped snapshot
// keys inapplicable rather than missing.
func arrayEmptyAt(payload any, dotted string) bool {
	if dotted == "" {
		return false
	}
	current := payload
	for _, raw := range strings.Split(dotted, ".") {
		isArray := strings.HasSuffix(raw, "[]")
		name := strings.TrimSuffix(raw, "[]")
		object, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = object[name]
		if !ok {
			return true
		}
		if isArray {
			items, ok := current.([]any)
			if !ok {
				return false
			}
			if len(items) == 0 {
				return true
			}
			current = items[0]
		}
	}
	items, ok := current.([]any)
	return ok && len(items) == 0
}
