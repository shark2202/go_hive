package credentials

// Parity: framework/credentials/setup.py::load_agent_nodes

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeAgentFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const declarativeAgentJSON = `{
  "name": "lead-enrichment",
  "version": "1.0.0",
  "goal": {"description": "Enrich leads", "success_criteria": [], "constraints": []},
  "nodes": [
    {"id": "start", "tools": {"policy": "explicit", "allowed": ["github_create_issue"]}},
    {"id": "inherit"},
    {"id": "no_tools", "tools": {"policy": "none"}},
    {"id": "alias_form", "tools": {"policy": "explicit", "tools": ["send_email"]}},
    {"id": "typed", "node_type": "judge"}
  ],
  "edges": [{"from_node": "start", "to_node": "inherit"}],
  "entry_node": "start",
  "tools": {"policy": "explicit", "allowed": ["notion_search"]}
}`

// Parity: framework/credentials/setup.py::_load_nodes_from_json_declarative —
// node-level tools win, agent-level tools are inherited, policy none empties
// the list, and node_type defaults to event_loop.
func TestLoadAgentNodesDeclarativeToolResolution(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "agent.json", declarativeAgentJSON)

	nodes := LoadAgentNodes(dir)
	if len(nodes) != 5 {
		t.Fatalf("nodes = %d, want 5", len(nodes))
	}
	want := []AgentNode{
		{Tools: []string{"github_create_issue"}, NodeType: "event_loop"},
		{Tools: []string{"notion_search"}, NodeType: "event_loop"},
		{Tools: []string{}, NodeType: "event_loop"},
		{Tools: []string{"send_email"}, NodeType: "event_loop"},
		{Tools: []string{"notion_search"}, NodeType: "judge"},
	}
	if !reflect.DeepEqual(nodes, want) {
		t.Fatalf("nodes = %#v, want %#v", nodes, want)
	}
}

// A document the Pydantic AgentConfig would reject yields no nodes — the
// reference swallows load_agent_config exceptions.
func TestLoadAgentNodesRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]string{
		"not json":          `{`,
		"missing edges":     `{"name":"a","goal":{"description":"g"},"nodes":[{"id":"n"}],"entry_node":"n"}`,
		"missing entry":     `{"name":"a","goal":{"description":"g"},"nodes":[{"id":"n"}],"edges":[]}`,
		"missing goal desc": `{"name":"a","goal":{},"nodes":[{"id":"n"}],"edges":[],"entry_node":"n"}`,
		"node without id":   `{"name":"a","goal":{"description":"g"},"nodes":[{}],"edges":[],"entry_node":"n"}`,
		"node tools list":   `{"name":"a","goal":{"description":"g"},"nodes":[{"id":"n","tools":["x"]}],"edges":[],"entry_node":"n"}`,
		"policy all":        `{"name":"a","goal":{"description":"g"},"nodes":[{"id":"n"}],"edges":[],"entry_node":"n","tools":{"policy":"all"}}`,
	}
	for label, body := range cases {
		dir := t.TempDir()
		writeAgentFile(t, dir, "agent.json", body)
		if nodes := LoadAgentNodes(dir); len(nodes) != 0 {
			t.Fatalf("%s: nodes = %#v, want none", label, nodes)
		}
	}
}

// Parity: framework/credentials/setup.py::load_agent_nodes — agent.py is the
// legacy branch; the Go runtime has no Python interpreter, so it reports no
// nodes. Pinned so the branch is a decision, not an accident.
func TestLoadAgentNodesPythonAgentHasNoNodes(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "agent.py", "nodes = [type('N', (), {'tools': ['x'], 'node_type': 'event_loop'})()]")
	if nodes := LoadAgentNodes(dir); len(nodes) != 0 {
		t.Fatalf("nodes = %#v, want none", nodes)
	}
}

func TestLoadAgentNodesEmptyDirectory(t *testing.T) {
	if nodes := LoadAgentNodes(t.TempDir()); len(nodes) != 0 {
		t.Fatalf("nodes = %#v, want none", nodes)
	}
}

func TestPyTitleMatchesPythonTitleCasing(t *testing.T) {
	cases := map[string]string{
		"google":        "Google",
		"lusha_api_key": "Lusha_Api_Key",
	}
	for input, want := range cases {
		if got := PyTitle(input); got != want {
			t.Fatalf("PyTitle(%q) = %q, want %q", input, got, want)
		}
	}
	if got := PyTitle(strings.ReplaceAll("lusha_api_key", "_", " ")); got != "Lusha Api Key" {
		t.Fatalf("fallback service name = %q, want %q", got, "Lusha Api Key")
	}
}
