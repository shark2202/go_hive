package nativecli

import (
	"encoding/json"
	"testing"
)

// mustParseSchema decodes a JSON literal into the Ordered shape the extractor
// expects.
func mustParseSchema(t *testing.T, raw string) any {
	t.Helper()
	parsed, err := ParseJSON([]byte(raw))
	if err != nil {
		t.Fatalf("ParseJSON(%s): %v", raw, err)
	}
	return parsed
}

// Parity-exempt: direct port of mcp2cli's to_kebab; covered end-to-end by the parity suite.
func TestToKebab(t *testing.T) {
	cases := map[string]string{
		"echo":       "echo",
		"tagList":    "tag-list",
		"snake_case": "snake-case",
		"Mixed_Case": "mixed-case",
		"HTTPServer": "httpserver",
		"already-ok": "already-ok",
	}
	for input, want := range cases {
		if got := toKebab(input); got != want {
			t.Fatalf("toKebab(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity-exempt: direct port of mcp2cli's schema_type_to_python.
func TestSchemaToKind(t *testing.T) {
	cases := []struct {
		schema string
		kind   paramKind
		suffix string
	}{
		{`{"type": "string"}`, kindString, ""},
		{`{"type": "integer"}`, kindInt, ""},
		{`{"type": "number"}`, kindFloat, ""},
		{`{"type": "boolean"}`, kindBool, ""},
		{`{"type": "array", "items": {"type": "string"}}`, kindString, " (JSON array)"},
		{`{"type": "object"}`, kindString, " (JSON object)"},
		{`{"type": ["integer", "null"]}`, kindInt, ""},
		{`{"enum": [1, 2, 3]}`, kindInt, ""},
		{`{"enum": [1, 2.5]}`, kindFloat, ""},
		{`{"enum": ["a", "b"]}`, kindString, ""},
		{`{"anyOf": [{"type": "integer"}, {"type": "null"}]}`, kindInt, ""},
		{`{"anyOf": [{"type": "string"}, {"type": "integer"}]}`, kindUnion, ""},
		{`{}`, kindString, ""},
	}
	for _, item := range cases {
		kind, suffix := schemaToKind(mustParseSchema(t, item.schema))
		if kind != item.kind || suffix != item.suffix {
			t.Fatalf("schemaToKind(%s) = (%v, %q), want (%v, %q)", item.schema, kind, suffix, item.kind, item.suffix)
		}
	}
}

// Parity-exempt: direct port of mcp2cli's _collapse_nullable.
func TestCollapseNullable(t *testing.T) {
	schema := mustParseSchema(t, `{"anyOf": [{"type": "string"}, {"type": "null"}], "description": "note"}`)
	collapsed, ok := collapseNullable(schema).(Ordered)
	if !ok {
		t.Fatalf("collapsed type = %T", collapseNullable(schema))
	}
	kind, _ := schemaToKind(collapsed)
	if kind != kindString {
		t.Fatalf("collapsed kind = %v", kind)
	}
	if description, _ := collapsed.Lookup("description"); description != "note" {
		t.Fatalf("description = %v", description)
	}
	// A two-branch union with two concrete types must pass through untouched.
	unions := mustParseSchema(t, `{"anyOf": [{"type": "string"}, {"type": "integer"}]}`)
	if _, ok := collapseNullable(unions).(Ordered); !ok {
		t.Fatalf("union collapsed unexpectedly")
	}
	if kind, _ := schemaToKind(unions); kind != kindUnion {
		t.Fatalf("union kind = %v", kind)
	}
}

// Parity-exempt: direct port of mcp2cli's coerce_value.
func TestCoerceValueArraysAndObjects(t *testing.T) {
	arraySchema := mustParseSchema(t, `{"type": "array", "items": {"type": "integer"}}`)
	csv := coerceValue("1, 2,3", arraySchema).([]any)
	if len(csv) != 3 || string(csv[0].(json.Number)) != "1" || string(csv[2].(json.Number)) != "3" {
		t.Fatalf("csv coercion = %v", csv)
	}
	jsonArray := coerceValue(`["a","b"]`, arraySchema).([]any)
	if len(jsonArray) != 2 || jsonArray[1] != "b" {
		t.Fatalf("json coercion = %v", jsonArray)
	}
	single := coerceValue("7", arraySchema).([]any)
	if len(single) != 1 || string(single[0].(json.Number)) != "7" {
		t.Fatalf("single coercion = %v", single)
	}

	objectSchema := mustParseSchema(t, `{"type": "object"}`)
	parsed := coerceValue(`{"k": "v"}`, objectSchema)
	if object, ok := parsed.(Ordered); !ok || object[0].Key != "k" {
		t.Fatalf("object coercion = %v", parsed)
	}
	if got := coerceValue("not json", objectSchema); got != "not json" {
		t.Fatalf("invalid object coercion = %v", got)
	}

	// A schema-less value that looks like JSON is parsed.
	if _, ok := coerceValue(`{"a": 1}`, nil).(Ordered); !ok {
		t.Fatalf("schema-less object was not parsed")
	}
}

// Parity-exempt: direct port of mcp2cli's extract_mcp_commands.
func TestExtractCommandsBuildsParamsAndNames(t *testing.T) {
	tools := []any{
		mustParseSchema(t, `{"name": "tagList", "description": "Join tags", "inputSchema": {"type": "object", "properties": {"tags": {"type": "array", "items": {"type": "string"}}, "limit": {"type": "integer", "description": "Max"}}, "required": ["tags"]}}`),
		mustParseSchema(t, `{"name": "tag-list", "description": "duplicate kebab"}`),
		mustParseSchema(t, `{"name": "noSchema"}`),
	}
	commands := extractCommands(tools)
	if len(commands) != 3 {
		t.Fatalf("commands = %d", len(commands))
	}
	if commands[0].Name != "tag-list" || commands[1].Name != "tag-list-2" {
		t.Fatalf("names = %q, %q", commands[0].Name, commands[1].Name)
	}
	if commands[2].Name != "no-schema" || commands[2].HasBody {
		t.Fatalf("noSchema = %+v", commands[2])
	}
	first := commands[0]
	if first.ToolName != "tagList" || len(first.Params) != 2 {
		t.Fatalf("first = %+v", first)
	}
	if first.Params[0].Name != "tags" || !first.Params[0].Required || first.Params[0].Description != "tags (JSON array)" {
		t.Fatalf("tags param = %+v", first.Params[0])
	}
	if first.Params[1].Kind != kindInt || first.Params[1].Description != "Max" {
		t.Fatalf("limit param = %+v", first.Params[1])
	}
}

// Parity-exempt: direct port of mcp2cli's _allocate_param_cli_names.
func TestAllocateCLINamesAvoidsBuiltins(t *testing.T) {
	command := Command{
		Name:    "collide",
		HasBody: true,
		Params: []Param{
			{Name: "stdin", Original: "stdin"},
			{Name: "help", Original: "help"},
			{Name: "other", Original: "other"},
		},
	}
	allocateCLINames(&command)
	if command.Params[0].CLIName != "arg-stdin" {
		t.Fatalf("stdin param = %q", command.Params[0].CLIName)
	}
	if command.Params[1].CLIName != "arg-help" {
		t.Fatalf("help param = %q", command.Params[1].CLIName)
	}
	if command.Params[2].CLIName != "other" {
		t.Fatalf("other param = %q", command.Params[2].CLIName)
	}
}

// Parity-exempt: direct port of mcp2cli's _truncate_description.
func TestTruncateDescriptionBreaksAtWordBoundary(t *testing.T) {
	if got := truncateDescription("short text", 70); got != "short text" {
		t.Fatalf("short = %q", got)
	}
	long := "alpha beta gamma delta epsilon zeta eta theta iota kappa lambda mu nu xi omicron"
	got := truncateDescription(long, 30)
	if got != "alpha beta gamma delta..." {
		t.Fatalf("truncated = %q", got)
	}
}

// Parity-exempt: direct port of mcp2cli's _wrap_description.
func TestWrapDescriptionIndentsContinuation(t *testing.T) {
	description := "one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen"
	got := wrapDescription(description, 10, 40)
	for _, line := range splitLines(got) {
		if len(line) > 40 {
			t.Fatalf("line %q exceeds width", line)
		}
	}
	if len(splitLines(got)) < 2 {
		t.Fatalf("expected wrapping, got %q", got)
	}
}

func splitLines(value string) []string {
	lines := []string{}
	current := ""
	for _, r := range value {
		if r == '\n' {
			lines = append(lines, current)
			current = ""
			continue
		}
		current += string(r)
	}
	return append(lines, current)
}
