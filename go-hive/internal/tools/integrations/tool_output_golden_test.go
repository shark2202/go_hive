package integrations

import (
	"context"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/toolgolden"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
	"github.com/shark2202/go_hive/go-hive/internal/tools/integrations/pdf"
)

// goldenReportPath is the committed capture report: canonical tool-result
// envelopes for both runtimes, produced by go_hive/scripts/capture_tool_output.py
// and go_hive/scripts/tool_output_golden.py. docs/tool-output-contract.md
// explains the scenario matrix and the exceptions.
const (
	goldenReportPath   = "../../../../docs/e2e-assets/tool-output/results.json"
	goldenScenarioPath = "../../../../docs/e2e-assets/tool-output/scenarios.json"
)

// Parity-exempt: Go-side golden harness; replays the captured scenarios in
// docs/tool-output-contract.md against the live tools.
//
// TestToolOutputGoldenAgainstCapture replays the captured integration-tool
// scenarios against the live Go implementations and compares the canonical
// envelope with the committed golden. No network: every scenario here is a
// local tool or an argument error.
func TestToolOutputGoldenAgainstCapture(t *testing.T) {
	scenarios, err := toolgolden.LoadScenarios(goldenReportPath)
	if err != nil {
		t.Fatalf("load golden: %v", err)
	}
	fixtures, err := toolgolden.LoadFixtures(goldenScenarioPath)
	if err != nil {
		t.Fatalf("load fixtures: %v", err)
	}
	// pdf_read resolves relative paths against the process working directory;
	// the capture ran with its fixture workspace as cwd.
	workspace := t.TempDir()
	if err := toolgolden.WriteFixtures(workspace, fixtures); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)

	registry := New()
	ctx := context.Background()

	cases := []struct {
		id    string
		run   func(t *testing.T) string
		paths [][2]string
	}{
		{
			id: "get_current_time_utc",
			run: func(t *testing.T) string {
				return executeGoldenTool(t, registry, "get_current_time", map[string]any{"timezone": "UTC"})
			},
		},
		{
			id: "get_current_time_offset",
			run: func(t *testing.T) string {
				return executeGoldenTool(t, registry, "get_current_time", map[string]any{"timezone": "Asia/Kolkata"})
			},
		},
		{
			id: "get_current_time_bad_tz",
			run: func(t *testing.T) string {
				return executeGoldenTool(t, registry, "get_current_time", map[string]any{"timezone": "Not/AZone"})
			},
		},
		{
			id: "pdf_read_missing",
			run: func(t *testing.T) string {
				value, err := pdf.NewRegistry().Execute(ctx, "pdf_read", map[string]any{"file_path": "missing.pdf"})
				if err != nil {
					t.Fatalf("pdf_read: %v", err)
				}
				return tools.PythonJSONDumps(value)
			},
		},
		{
			id: "pdf_read_both_args",
			run: func(t *testing.T) string {
				value, err := pdf.NewRegistry().Execute(ctx, "pdf_read", map[string]any{
					"file_path": "probe.pdf",
					"files":     []any{"probe.pdf"},
				})
				if err != nil {
					t.Fatalf("pdf_read: %v", err)
				}
				return tools.PythonJSONDumps(value)
			},
		},
		{
			id: "pdf_read_bad_pages",
			run: func(t *testing.T) string {
				value, err := pdf.NewRegistry().Execute(ctx, "pdf_read", map[string]any{"file_path": "probe.pdf", "pages": "abc"})
				if err != nil {
					t.Fatalf("pdf_read: %v", err)
				}
				return tools.PythonJSONDumps(value)
			},
		},
		{
			id: "attach_file_missing",
			run: func(t *testing.T) string {
				value, err := attachFile(ctx, nil, map[string]any{"paths": []any{"missing.png"}})
				if err != nil {
					t.Fatalf("attach_file: %v", err)
				}
				result, ok := value.(tools.ToolResult)
				if !ok {
					t.Fatalf("attach_file result = %T", value)
				}
				return result.Content
			},
		},
		{
			id: "attach_file_ok",
			run: func(t *testing.T) string {
				return runGoldenAttachFileOK(t, ctx)
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.id, func(t *testing.T) {
			golden, ok := scenarios[testCase.id]
			if !ok {
				t.Fatalf("scenario %s missing from %s", testCase.id, goldenReportPath)
			}
			options := toolgolden.DefaultOptions()
			for _, pair := range testCase.paths {
				options = options.WithPath(pair[0], pair[1])
			}
			got := toolgolden.Canonicalize(testCase.run(t), options)
			if !got.Equal(golden.Go) {
				t.Fatalf("canonical envelope drifted from the capture.\n got: %#v\nwant: %#v", got, golden.Go)
			}
		})
	}
}

func executeGoldenTool(t *testing.T, registry *Registry, name string, input map[string]any) string {
	t.Helper()
	value, err := registry.Execute(context.Background(), name, input)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return tools.PythonJSONDumps(value)
}

// runGoldenAttachFileOK replays the text-attachment scenario end to end: the
// tool builds the summary plus the trailing preview block, then the publisher
// copies the file and injects the chip URL — the same two steps the runtime
// performs, so the captured content is comparable. The fixture workspace is
// the test's cwd (TestToolOutputGoldenAgainstCapture).
func runGoldenAttachFileOK(t *testing.T, ctx context.Context) string {
	t.Helper()
	sessionDir := t.TempDir()

	value, err := attachFile(ctx, nil, map[string]any{"paths": []any{"probe.txt"}})
	if err != nil {
		t.Fatalf("attach_file: %v", err)
	}
	result, ok := value.(tools.ToolResult)
	if !ok {
		t.Fatalf("attach_file result = %T", value)
	}
	published, isError := PublishAttachFileResult(result.Content, result.Attachments, sessionDir)
	if isError {
		t.Fatalf("publish failed: %s", published)
	}
	return published
}
