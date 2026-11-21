package builtin

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/toolgolden"
)

// Parity-exempt: Go-side golden harness; replays the captured scenarios in
// docs/tool-output-contract.md against the live tool.
//
// TestReadFileGoldenAgainstCapture replays the captured read_file scenarios
// against the live builtin and compares the canonical model-visible envelope
// (the loop's "ERROR: " prefix included) with the committed golden.
func TestReadFileGoldenAgainstCapture(t *testing.T) {
	scenarios, err := toolgolden.LoadScenarios("../../../../docs/e2e-assets/tool-output/results.json")
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	fixtures, err := toolgolden.LoadFixtures("../../../../docs/e2e-assets/tool-output/scenarios.json")
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	workspace := t.TempDir()
	if err := toolgolden.WriteFixtures(workspace, fixtures); err != nil {
		t.Fatal(err)
	}

	options := toolgolden.DefaultOptions().WithPath(workspace, "<WS>")
	reads := []struct {
		id   string
		path string
	}{
		{id: "read_file_ok", path: workspace + "/probe.txt"},
		{id: "read_file_missing", path: workspace + "/missing.txt"},
		{id: "read_file_dir", path: workspace},
	}

	box := New(workspace)
	for _, read := range reads {
		t.Run(read.id, func(t *testing.T) {
			golden, ok := scenarios[read.id]
			if !ok {
				t.Fatalf("scenario %s missing from the capture", read.id)
			}
			got := toolgolden.Canonicalize(ReadFileContent(t, box, read.path), options)
			if !got.Equal(golden.Go) {
				t.Fatalf("canonical envelope drifted from the capture.\n got: %#v\nwant: %#v", got, golden.Go)
			}
		})
	}
}

// ReadFileContent runs read_file and returns what the model would see.
func ReadFileContent(t *testing.T, box *Toolbox, path string) string {
	t.Helper()
	result, err := box.Execute(context.Background(), ToolReadFile, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("read_file %s: %v", path, err)
	}
	if result.IsError {
		return "ERROR: " + result.Content
	}
	return result.Content
}

// Parity: tools/src/aden_tools/file_ops.py::read_file (the directory branch).
//
// TestReadFileDirectoryListingFormat pins the upstream listing shape, which the
// golden above only covers for a two-entry directory.
func TestReadFileDirectoryListingFormat(t *testing.T) {
	workspace := t.TempDir()
	fixtures, err := toolgolden.LoadFixtures("../../../../docs/e2e-assets/tool-output/scenarios.json")
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	if err := toolgolden.WriteFixtures(workspace, fixtures); err != nil {
		t.Fatal(err)
	}
	mkdirAll(t, workspace+"/sub")
	box := New(workspace)
	got := ReadFileContent(t, box, workspace)
	names := make([]string, 0, len(fixtures)+1)
	for name := range fixtures {
		names = append(names, name)
	}
	names = append(names, "sub/")
	sort.Strings(names)
	lines := make([]string, 0, len(names))
	for _, name := range names {
		lines = append(lines, "  "+name)
	}
	want := fmt.Sprintf("Directory: %s (%d entries)\n%s", workspace, len(lines), strings.Join(lines, "\n"))
	if got != want {
		t.Fatalf("listing = %q, want %q", got, want)
	}
}
