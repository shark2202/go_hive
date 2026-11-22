package agents

import (
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/colony"
)

// Parity-exempt: Go-only compatibility guard — a legacy worker.json whose
// system_prompt is the queen persona loads verbatim (no guessing rewrite) and
// its tools list still reaches the worker spec.
func TestLoadAgentSpecPreservesLegacyQueenPersonaPrompt(t *testing.T) {
	colonyDir := t.TempDir()
	legacyPrompt := "You are Charlotte, the queen of this hive. You remember every conversation."
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"worker","system_prompt":"`+legacyPrompt+`","tools":["read_file","github"],`+
			`"goal":{"description":"legacy colony"}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SystemPrompt == nil || *spec.AgentSpec.SystemPrompt != legacyPrompt {
		t.Fatalf("legacy system_prompt = %v, want it preserved verbatim", spec.AgentSpec.SystemPrompt)
	}
	if len(spec.AgentSpec.Tools) != 2 || spec.AgentSpec.Tools[0] != "read_file" || spec.AgentSpec.Tools[1] != "github" {
		t.Fatalf("legacy tools = %v, want [read_file github]", spec.AgentSpec.Tools)
	}
}

// Parity: tests/test_worker_definition.py::TestBuildMeta::test_tools_is_independent_copy
func TestLoadAgentSpecReadsForkedWorkerDefinition(t *testing.T) {
	colonyDir := t.TempDir()
	task := "summarise the tracker"
	prompt := colony.BuildSystemPrompt(&task)
	writeWorkerSpecFile(t, colonyDir, "worker.json",
		`{"name":"Forked","system_prompt":`+quoteJSON(prompt)+`,"tools":["tracker_upsert","csv"],`+
			`"goal":{"description":"`+task+`"}}`)

	spec, err := LoadAgentSpec(colonyDir)
	if err != nil {
		t.Fatalf("LoadAgentSpec: %v", err)
	}
	if spec.AgentSpec.SystemPrompt == nil || *spec.AgentSpec.SystemPrompt != prompt {
		t.Fatalf("forked system_prompt did not round-trip through the loader")
	}
	if strings.Contains(prompt, "You are the queen") {
		t.Fatalf("worker prompt carries the queen persona: %q", prompt)
	}
	if len(spec.AgentSpec.Tools) != 2 || spec.AgentSpec.Tools[0] != "tracker_upsert" || spec.AgentSpec.Tools[1] != "csv" {
		t.Fatalf("forked tools = %v, want [tracker_upsert csv]", spec.AgentSpec.Tools)
	}
}

func quoteJSON(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}
