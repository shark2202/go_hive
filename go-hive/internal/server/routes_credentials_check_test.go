package server

// Parity: framework/server/routes_credentials.py::handle_check_agent — the
// response shape and per-branch semantics of /api/credentials/check-agent.
// Upstream's only HTTP test covers the traversal branch; everything below is
// Parity-exempt pinning derived from the handler source.

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

func makeCredentialColony(t *testing.T, home hivehome.Home, name, workerTools string) string {
	t.Helper()
	colony := filepath.Join(home.ColoniesDir(), name)
	if err := os.MkdirAll(colony, 0o755); err != nil {
		t.Fatalf("mkdir colony: %v", err)
	}
	writeAgentFixture(t, colony, "worker.json",
		`{"name":"`+name+`","description":"d","tools":`+workerTools+`}`)
	return colony
}

func checkAgentRequest(t *testing.T, mux *http.ServeMux, payload string) (int, map[string]any) {
	t.Helper()
	recorder, body := doSessionJSON(t, mux, http.MethodPost, "/api/credentials/check-agent", payload)
	return recorder.Code, body
}

func requirementRows(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["required"].([]any)
	if !ok {
		t.Fatalf("required = %#v", body["required"])
	}
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("row = %#v", item)
		}
		rows = append(rows, row)
	}
	return rows
}

func rowByCredentialID(t *testing.T, rows []map[string]any, credentialID string) map[string]any {
	t.Helper()
	for _, row := range rows {
		if row["credential_id"] == credentialID {
			return row
		}
	}
	t.Fatalf("row %q missing from %#v", credentialID, rows)
	return nil
}

// The worker config's declared tools map to credential rows: a present key is
// available with valid unset (verify off), the same declaration without a key
// reports the missing credential. Any aden_supported credential prepends the
// synthetic Aden row (github is aden_supported), available following
// ADEN_API_KEY.
// Parity: core/framework/server/routes_credentials.py::handle_check_agent
func TestCheckAgentWorkerConfigRows(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeCredentialColony(t, home, "cred_colony", `["github_create_issue"]`)
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	t.Setenv("GITHUB_TOKEN", "gh-token")
	mux := agentRoutesMux(home, NewSessionManager())

	status, body := checkAgentRequest(t, mux, `{"agent_path":"`+colony+`","verify":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	rows := requirementRows(t, body)
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0]["credential_id"] != "aden_api_key" || rows[0]["available"] != false {
		t.Fatalf("aden row = %#v", rows[0])
	}
	if body["has_aden_key"] != false {
		t.Fatalf("has_aden_key = %#v", body["has_aden_key"])
	}
	row := rowByCredentialID(t, rows, "github")
	expects := map[string]any{
		"credential_name":          "github",
		"credential_id":            "github",
		"env_var":                  "GITHUB_TOKEN",
		"available":                true,
		"direct_api_key_supported": true,
		"aden_supported":           true,
		"credential_key":           "access_token",
		"valid":                    nil,
		"validation_message":       nil,
		"alternative_group":        nil,
	}
	for key, want := range expects {
		if got := row[key]; got != want {
			t.Fatalf("row[%q] = %#v, want %#v (row %#v)", key, got, want, row)
		}
	}
	if tools, ok := row["tools"].([]any); !ok || len(tools) != 1 || tools[0] != "github_create_issue" {
		t.Fatalf("tools = %#v", row["tools"])
	}
	if nodeTypes, ok := row["node_types"].([]any); !ok || len(nodeTypes) != 0 {
		t.Fatalf("node_types = %#v", row["node_types"])
	}

	// Same declaration without the key: the credential is reported missing.
	t.Setenv("GITHUB_TOKEN", "")
	status, body = checkAgentRequest(t, mux, `{"agent_path":"`+colony+`","verify":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	rows = requirementRows(t, body)
	if len(rows) != 2 || rowByCredentialID(t, rows, "github")["available"] != false {
		t.Fatalf("missing rows = %#v", rows)
	}
}

// A declarative agent.json is the upstream branch: node tools (and agent-level
// inheritance) come from the document itself.
// Parity: core/framework/server/routes_credentials.py::handle_check_agent
func TestCheckAgentDeclarativeAgentJSONRow(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := filepath.Join(home.ColoniesDir(), "json_colony")
	if err := os.MkdirAll(colony, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeAgentFixture(t, colony, "agent.json", `{
	  "name": "json-agent",
	  "goal": {"description": "g"},
	  "nodes": [{"id": "start", "tools": {"policy": "explicit", "allowed": ["github_create_issue"]}}],
	  "edges": [],
	  "entry_node": "start"
	}`)
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	t.Setenv("GITHUB_TOKEN", "gh-token")
	mux := agentRoutesMux(home, NewSessionManager())

	status, body := checkAgentRequest(t, mux, `{"agent_path":"`+colony+`","verify":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	rows := requirementRows(t, body)
	if len(rows) != 2 || rowByCredentialID(t, rows, "github")["available"] != true {
		t.Fatalf("rows = %#v", rows)
	}
}

// Any returned credential with aden_supported prepends the synthetic Aden
// Platform row; the row has no alternative_group key and reports the Aden key
// presence.
// Parity: core/framework/server/routes_credentials.py::handle_check_agent
func TestCheckAgentAdenRowPrepended(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	colony := makeCredentialColony(t, home, "aden_colony", `["notion_search"]`)
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "aden-key")
	t.Setenv("NOTION_API_TOKEN", "")
	mux := agentRoutesMux(home, NewSessionManager())

	status, body := checkAgentRequest(t, mux, `{"agent_path":"`+colony+`","verify":false}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	if body["has_aden_key"] != true {
		t.Fatalf("has_aden_key = %#v", body["has_aden_key"])
	}
	rows := requirementRows(t, body)
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	aden := rows[0]
	expects := map[string]any{
		"credential_name":          "Aden Platform",
		"credential_id":            "aden_api_key",
		"env_var":                  "ADEN_API_KEY",
		"description":              "API key from the Developers tab in Settings",
		"help_url":                 "https://hive.adenhq.com/",
		"available":                true,
		"valid":                    nil,
		"validation_message":       nil,
		"direct_api_key_supported": true,
		"aden_supported":           true,
		"credential_key":           "api_key",
	}
	for key, want := range expects {
		if got := aden[key]; got != want {
			t.Fatalf("aden[%q] = %#v, want %#v", key, got, want)
		}
	}
	if _, present := aden["alternative_group"]; present {
		t.Fatalf("aden row carries alternative_group: %#v", aden)
	}
	if tools, ok := aden["tools"].([]any); !ok || len(tools) != 0 {
		t.Fatalf("aden tools = %#v", aden["tools"])
	}
	if nodeTypes, ok := aden["node_types"].([]any); !ok || len(nodeTypes) != 0 {
		t.Fatalf("aden node_types = %#v", aden["node_types"])
	}
	if rows[1]["credential_id"] != "notion" || rows[1]["available"] != false {
		t.Fatalf("notion row = %#v", rows[1])
	}
}

// An unknown directory inside the allowed roots is a valid path: the reference
// returns an empty requirement list rather than a 404.
// Parity: core/framework/server/routes_credentials.py::handle_check_agent
func TestCheckAgentUnknownAgentInRootsIsEmpty(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	t.Setenv("HIVE_CREDENTIAL_KEY", "")
	t.Setenv(credentials.AdenEnvVar, "")
	mux := agentRoutesMux(home, NewSessionManager())

	status, body := checkAgentRequest(t, mux, `{"agent_path":"`+filepath.Join(home.ColoniesDir(), "ghost")+`"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	if rows := requirementRows(t, body); len(rows) != 0 {
		t.Fatalf("rows = %#v", rows)
	}
	if body["has_aden_key"] != false {
		t.Fatalf("has_aden_key = %#v", body["has_aden_key"])
	}
}

// Non-string agent_path follows the reference's TypeError path → 500.
// Parity: core/framework/server/routes_credentials.py::handle_check_agent
func TestCheckAgentNonStringPath(t *testing.T) {
	home := hivehome.Home{Root: t.TempDir()}
	mux := agentRoutesMux(home, NewSessionManager())
	status, body := checkAgentRequest(t, mux, `{"agent_path":123}`)
	if status != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %#v", status, body)
	}
	if body["error"] != internalServerError {
		t.Fatalf("error = %#v", body["error"])
	}
}
