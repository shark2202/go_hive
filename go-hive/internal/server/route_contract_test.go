// Route-table contract gate: the Go server's registered routes must match the
// frozen manifest in go_hive/contracts/api-routes.json (reference = Python
// checkout).  The default mode is report-only (the lane that froze the
// manifest is not the lane that fixes drift); set HIVE_ROUTE_CONTRACT_STRICT=1
// to turn drift into failures.
package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type contractRoute struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	PathShape  string `json:"path_shape"`
	Handler    string `json:"handler"`
	Source     string `json:"source"`
	Kind       string `json:"kind"`
	Annotation string `json:"annotation"`
}

type contractRouteTable struct {
	Version    string          `json:"version"`
	Commit     string          `json:"commit"`
	SourceRoot string          `json:"source_root"`
	RouteCount int             `json:"route_count"`
	Routes     []contractRoute `json:"routes"`
}

type contractRouteManifest struct {
	Schema    string             `json:"schema"`
	Reference contractRouteTable `json:"reference"`
	Target    contractRouteTable `json:"target"`
	Summary   map[string]int     `json:"summary"`
	Python    contractRouteTable `json:"python"`
	Go        contractRouteTable `json:"go"`
}

var handleFuncPattern = regexp.MustCompile(`HandleFunc\(\s*"([A-Z]+)\s+([^"]+)"`)

func routeKey(method, path string) string { return method + " " + path }

// scanGoRoutes re-derives the route table from the production sources in the
// current package, mirroring go_hive/scripts/export_api_routes.py.
func scanGoRoutes(t *testing.T) map[string]contractRoute {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	found := map[string]contractRoute{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		lines := strings.Split(string(source), "\n")
		for lineNo, line := range lines {
			match := handleFuncPattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			route := contractRoute{
				Method: match[1],
				Path:   match[2],
				Source: fmt.Sprintf("%s:%d", name, lineNo+1),
			}
			found[routeKey(route.Method, route.Path)] = route
		}
	}
	return found
}

// Parity-exempt: 路由契约门禁；基线是 go_hive/contracts/api-routes.json（静态提取产物），上游无对应测试。
func TestRouteTableMatchesManifest(t *testing.T) {
	manifestPath := filepath.Join("..", "..", "..", "contracts", "api-routes.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("route manifest missing: %v (run go_hive/scripts/export_api_routes.py)", err)
	}
	var manifest contractRouteManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	if manifest.Reference.Commit == "" || manifest.Target.Commit == "" {
		t.Fatalf("manifest is missing reference/target commit fields")
	}
	if len(manifest.Go.Routes) == 0 || len(manifest.Python.Routes) == 0 {
		t.Fatalf("manifest route tables are empty")
	}

	manifestRoutes := map[string]contractRoute{}
	for _, route := range manifest.Go.Routes {
		manifestRoutes[routeKey(route.Method, route.Path)] = route
	}
	scanned := scanGoRoutes(t)

	var missingFromSource, extraInSource []string
	for key := range manifestRoutes {
		if _, ok := scanned[key]; !ok {
			missingFromSource = append(missingFromSource, key)
		}
	}
	for key := range scanned {
		if _, ok := manifestRoutes[key]; !ok {
			extraInSource = append(extraInSource, key)
		}
	}
	sort.Strings(missingFromSource)
	sort.Strings(extraInSource)

	strict := os.Getenv("HIVE_ROUTE_CONTRACT_STRICT") == "1"
	report := func(format string, args ...any) {
		if strict {
			t.Errorf(format, args...)
			return
		}
		t.Logf(format, args...)
	}
	for _, key := range missingFromSource {
		route := manifestRoutes[key]
		report("route in manifest but not in source: %s %s (manifest %s)", route.Method, route.Path, route.Source)
	}
	for _, key := range extraInSource {
		route := scanned[key]
		report("route in source but not in manifest: %s %s (%s) — re-run export_api_routes.py", route.Method, route.Path, route.Source)
	}
	t.Logf(
		"route contract: manifest=%d scanned=%d missing_from_source=%d extra_in_source=%d strict=%v",
		len(manifestRoutes), len(scanned), len(missingFromSource), len(extraInSource), strict,
	)

	if summary, ok := manifest.Summary["reference_routes"]; ok && summary != len(manifest.Python.Routes) {
		t.Errorf("manifest summary.reference_routes=%d but python.routes=%d", summary, len(manifest.Python.Routes))
	}
}
