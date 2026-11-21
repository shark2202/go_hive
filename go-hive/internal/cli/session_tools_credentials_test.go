package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
)

// Parity-exempt: no upstream agent tool exists; the catalogue must advertise
// the read-only credential status tool with no write parameters.
func TestCredentialStatusToolIsAdvertisedReadOnly(t *testing.T) {
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	var found bool
	for _, def := range catalogue.Defs() {
		if def.Name != CredentialStatusToolName {
			continue
		}
		found = true
		if !def.ConcurrencySafe {
			t.Fatal("credential_status should be concurrency safe (read-only)")
		}
		params := def.Parameters
		props, _ := params["properties"].(map[string]any)
		if len(props) != 0 {
			t.Fatalf("credential_status must take no arguments, got %v", props)
		}
		if extra, _ := params["additionalProperties"].(bool); extra {
			t.Fatal("credential_status must reject additional properties")
		}
	}
	if !found {
		t.Fatalf("%s missing from the session catalogue", CredentialStatusToolName)
	}
}

// Parity-exempt: secrecy guard. The payload may name credentials and their
// tool modules, but a key value (from either the store or the environment) must
// never appear in the encoded result.
func TestCredentialStatusOmitsSecretValues(t *testing.T) {
	const secret = "sk-live-SECRET-VALUE"
	cred := credentials.NewCredentialObject("airtable_pat")
	cred.Keys.Set(credentials.NewCredentialKey("api_key", secret))
	store := credentials.NewCredentialStore(credentials.WithStorage(
		credentials.NewInMemoryStorage(map[string]*credentials.CredentialObject{
			"airtable_pat": cred,
		}),
	))
	getenv := func(name string) string {
		if name == "GITHUB_TOKEN" {
			return secret
		}
		return ""
	}

	payload := buildCredentialStatusPayload(store, nil, getenv)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, secret) {
		t.Fatalf("credential_status leaked a secret value:\n%s", text)
	}

	byID := map[string]credentialStatusEntry{}
	for _, entry := range payload.Credentials {
		byID[entry.CredentialID] = entry
	}
	airtable, ok := byID["airtable_pat"]
	if !ok {
		t.Fatalf("stored credential missing from payload: %s", text)
	}
	if !airtable.Configured || len(airtable.Sources) != 1 || airtable.Sources[0] != "store" {
		t.Fatalf("airtable entry = %+v, want store-configured", airtable)
	}
	if len(airtable.ToolModules) != 1 || airtable.ToolModules[0] != "airtable_tool" {
		t.Fatalf("airtable tool modules = %v, want [airtable_tool]", airtable.ToolModules)
	}
	github, ok := byID["github"]
	if !ok {
		t.Fatalf("env credential missing from payload: %s", text)
	}
	if !github.Configured || len(github.Sources) != 1 || github.Sources[0] != "env" {
		t.Fatalf("github entry = %+v, want env-configured", github)
	}
	if len(github.ToolModules) != 1 || github.ToolModules[0] != "github_tool" {
		t.Fatalf("github tool modules = %v, want [github_tool]", github.ToolModules)
	}
}

// Parity-exempt: wiring guard. The catalogue dispatches the tool without
// arguments and returns a JSON payload rather than a transport error.
func TestCredentialStatusCatalogueExecutionReturnsJSON(t *testing.T) {
	t.Setenv("HIVE_HOME", t.TempDir())
	root := testTempDir(t)
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})

	output, isError, err := catalogue.Execute(context.Background(), CredentialStatusToolName, map[string]any{})
	if err != nil {
		t.Fatalf("credential_status: transport error: %v", err)
	}
	if isError {
		t.Fatalf("credential_status reported an error: %s", output)
	}
	var payload credentialStatusPayload
	if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
		t.Fatalf("credential_status output is not JSON (%q): %v", output, jsonErr)
	}
	if payload.Count != len(payload.Credentials) {
		t.Fatalf("count = %d, credentials = %d", payload.Count, len(payload.Credentials))
	}
}
