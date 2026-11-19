package cli

// Session credential status: `credential_status` is the agent-facing, read-only
// view of which credentials this host has configured. It exists so a queen can
// decide whether an integration's credential is available before calling it,
// without handing the model the credential store's write path or any secret
// material.
//
// The payload is deliberately metadata-only: credential names, environment
// variable names, tool modules and the source that made the credential
// configured. Key values, aliases and identities are never read — listing goes
// through CredentialStore.ListCredentials (index ids) and the local registry's
// account ids, neither of which decrypts a key.

import (
	"encoding/json"
	"os"
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/llm"
)

// CredentialStatusToolName is the tool name advertised to the model.
const CredentialStatusToolName = "credential_status"

// Parity-exempt: no upstream agent tool exists. The Python side only exposes
// credentials through HTTP routes (framework/server/routes_credentials.py) and
// the `hive cred` CLI; this read-only tool is the agent surface for the same
// availability question.
func credentialStatusToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name: CredentialStatusToolName,
		Description: "List the credentials configured on this host. Read-only: returns each credential's " +
			"name, environment variable, tool modules and how it is configured (env or store). " +
			"Never returns key values, aliases or identities.",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		},
		ConcurrencySafe: true,
	}
}

// credentialStatusEntry is one configured credential. Sources is the list of
// places the credential was found ("env" / "store"), never a value.
type credentialStatusEntry struct {
	Name         string   `json:"name"`
	CredentialID string   `json:"credential_id"`
	EnvVar       string   `json:"env_var,omitempty"`
	Configured   bool     `json:"configured"`
	Sources      []string `json:"sources,omitempty"`
	ToolModules  []string `json:"tool_modules"`
	Required     bool     `json:"required"`
}

type credentialStatusPayload struct {
	Credentials []credentialStatusEntry `json:"credentials"`
	Count       int                     `json:"count"`
}

// buildCredentialStatusPayload projects the configured-credential view. store
// and local may be nil (nothing is configured); getenv is injected so a test
// can prove the value is only compared, never copied into the payload.
func buildCredentialStatusPayload(store *credentials.CredentialStore, local credentials.LocalRegistry, getenv func(string) string) credentialStatusPayload {
	if getenv == nil {
		getenv = os.Getenv
	}
	stored := map[string]struct{}{}
	if store != nil {
		if ids, err := store.ListCredentials(); err == nil {
			for _, id := range ids {
				// Index ids may be "provider/alias"; the spec table keys by
				// provider, so normalise to the provider segment.
				base := strings.SplitN(id, "/", 2)[0]
				if base != "" {
					stored[base] = struct{}{}
				}
			}
		}
	}
	if local != nil {
		if infos, err := local.ListAccounts(""); err == nil {
			for _, info := range infos {
				if info.CredentialID != "" {
					stored[info.CredentialID] = struct{}{}
				}
			}
		}
	}

	entries := make([]credentialStatusEntry, 0)
	seen := map[string]struct{}{}
	for _, spec := range credentials.GeneratedCredentialSpecs {
		id := spec.CredentialID
		if id == "" {
			id = spec.Name
		}
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		sources := make([]string, 0, 2)
		if spec.EnvVar != "" && getenv(spec.EnvVar) != "" {
			sources = append(sources, "env")
		}
		if _, ok := stored[id]; ok {
			sources = append(sources, "store")
		} else if _, ok := stored[spec.Name]; ok && spec.Name != id {
			sources = append(sources, "store")
		}
		if len(sources) == 0 {
			continue
		}
		seen[id] = struct{}{}
		modules := append([]string(nil), spec.ToolModules...)
		if modules == nil {
			modules = []string{}
		}
		entries = append(entries, credentialStatusEntry{
			Name:         spec.Name,
			CredentialID: id,
			EnvVar:       spec.EnvVar,
			Configured:   true,
			Sources:      sources,
			ToolModules:  modules,
			Required:     spec.Required,
		})
	}

	// A stored credential with no generated spec must still be visible; it
	// simply has no tool-module mapping to report.
	extras := make([]string, 0)
	for id := range stored {
		if _, ok := seen[id]; !ok {
			extras = append(extras, id)
		}
	}
	sort.Strings(extras)
	for _, id := range extras {
		entries = append(entries, credentialStatusEntry{
			Name:         id,
			CredentialID: id,
			Configured:   true,
			Sources:      []string{"store"},
			ToolModules:  []string{},
		})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CredentialID < entries[j].CredentialID })
	return credentialStatusPayload{Credentials: entries, Count: len(entries)}
}

// executeCredentialStatusTool builds the default-adapter view and encodes it
// the way the other session tools encode results (JSON string, isError=false).
func executeCredentialStatusTool() (string, bool, error) {
	var store *credentials.CredentialStore
	if adapter, err := credentials.DefaultAdapter(nil); err == nil && adapter != nil {
		store = adapter.Store()
	}
	payload := buildCredentialStatusPayload(store, credentials.DefaultLocalCredentialRegistry(), os.Getenv)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "credential_status result could not be encoded: " + err.Error(), true, nil
	}
	return string(encoded), false, nil
}
