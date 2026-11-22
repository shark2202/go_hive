package server

// Parity: framework/server/routes_credentials.py::handle_check_agent
//
// check-agent answers "which credentials does this agent need, and are they
// present/valid?" for the CredentialsModal. The route lives with the agent
// routes; the credential semantics it needs live here so the handler stays a
// thin HTTP shell.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hiveconf"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// agentCredentialRow is one entry of the check-agent "required" list.
// Field order mirrors _status_to_dict in the reference handler.
type agentCredentialRow struct {
	CredentialName        string   `json:"credential_name"`
	CredentialID          string   `json:"credential_id"`
	EnvVar                string   `json:"env_var"`
	Description           string   `json:"description"`
	HelpURL               string   `json:"help_url"`
	Tools                 []string `json:"tools"`
	NodeTypes             []string `json:"node_types"`
	Available             bool     `json:"available"`
	DirectAPIKeySupported bool     `json:"direct_api_key_supported"`
	AdenSupported         bool     `json:"aden_supported"`
	CredentialKey         string   `json:"credential_key"`
	Valid                 *bool    `json:"valid"`
	ValidationMessage     *string  `json:"validation_message"`
	AlternativeGroup      *string  `json:"alternative_group"`
}

// adenPlatformCredentialRow is the synthetic Aden Platform entry. Upstream
// builds it as a literal dict; it carries no alternative_group key.
type adenPlatformCredentialRow struct {
	CredentialName        string   `json:"credential_name"`
	CredentialID          string   `json:"credential_id"`
	EnvVar                string   `json:"env_var"`
	Description           string   `json:"description"`
	HelpURL               string   `json:"help_url"`
	Tools                 []string `json:"tools"`
	NodeTypes             []string `json:"node_types"`
	Available             bool     `json:"available"`
	Valid                 *bool    `json:"valid"`
	ValidationMessage     *string  `json:"validation_message"`
	DirectAPIKeySupported bool     `json:"direct_api_key_supported"`
	AdenSupported         bool     `json:"aden_supported"`
	CredentialKey         string   `json:"credential_key"`
}

// checkAgentCredentialRequirements runs the same validation agent startup
// uses and shapes the result for the modal.
//
// Parity: framework/server/routes_credentials.py::handle_check_agent
func (deps AgentRouteDeps) checkAgentCredentialRequirements(agentPath string, verify bool) (map[string]any, error) {
	// Load env vars from shell config (same as runtime startup).
	if deps.EnsureCredentialEnv != nil {
		deps.EnsureCredentialEnv()
	} else {
		credentials.EnsureCredentialKeyEnv(
			credentials.WithExtraEnvVars(agentCredentialEnvVars()...),
			credentials.WithLLMEnvVar(configuredLLMEnvVar(deps.Home)),
		)
	}

	nodes := checkAgentNodes(agentPath)
	store := checkAgentCredentialStore(deps.Home)
	result, err := credentials.ValidateAgentCredentials(nodes, store, credentials.ValidateAgentCredentialsOptions{
		SkipVerification: !verify,
		ForceRefresh:     true,
	})
	if err != nil {
		return nil, err
	}

	required := make([]any, 0, len(result.Credentials)+1)
	anyAden := false
	for _, status := range result.Credentials {
		if status.AdenSupported {
			anyAden = true
			break
		}
	}
	if anyAden {
		// If any credential needs Aden, include ADEN_API_KEY as a first-class row.
		required = append(required, adenPlatformCredentialRow{
			CredentialName:        "Aden Platform",
			CredentialID:          credentials.AdenCredentialID,
			EnvVar:                "ADEN_API_KEY",
			Description:           "API key from the Developers tab in Settings",
			HelpURL:               "https://hive.adenhq.com/",
			Tools:                 []string{},
			NodeTypes:             []string{},
			Available:             result.HasAdenKey,
			Valid:                 nil,
			ValidationMessage:     nil,
			DirectAPIKeySupported: true,
			AdenSupported:         true,
			CredentialKey:         "api_key",
		})
	}
	for _, status := range result.Credentials {
		required = append(required, credentialStatusRow(status))
	}

	return map[string]any{
		"required":     required,
		"has_aden_key": result.HasAdenKey,
	}, nil
}

func credentialStatusRow(status credentials.AgentCredentialStatus) agentCredentialRow {
	tools := status.Tools
	if tools == nil {
		tools = []string{}
	}
	nodeTypes := status.NodeTypes
	if nodeTypes == nil {
		nodeTypes = []string{}
	}
	return agentCredentialRow{
		CredentialName:        status.CredentialName,
		CredentialID:          status.CredentialID,
		EnvVar:                status.EnvVar,
		Description:           status.Description,
		HelpURL:               status.HelpURL,
		Tools:                 tools,
		NodeTypes:             nodeTypes,
		Available:             status.Available,
		DirectAPIKeySupported: status.DirectAPIKeySupported,
		AdenSupported:         status.AdenSupported,
		CredentialKey:         status.CredentialKey,
		Valid:                 status.Valid,
		ValidationMessage:     status.ValidationMessage,
		AlternativeGroup:      status.AlternativeGroup,
	}
}

// checkAgentNodes resolves the credential-relevant nodes for an agent directory.
//
// Upstream load_agent_nodes reads agent.json (declarative) then agent.py. The
// Go runtime has no Python interpreter, so a directory carrying neither file
// is a Go-colony directory: its agent definition is the worker config the
// colony host already loads (agents.LoadAgentSpec, first config in sorted
// order), and that config's tools are the equivalent of upstream NodeSpec.tools.
//
// Parity-exempt: the worker-config fallback has no upstream counterpart —
// worker.json is this port's agent-definition format.
func checkAgentNodes(agentPath string) []credentials.AgentNode {
	if pathExists(filepath.Join(agentPath, "agent.json")) || pathExists(filepath.Join(agentPath, "agent.py")) {
		return credentials.LoadAgentNodes(agentPath)
	}
	spec, err := agents.LoadAgentSpec(agentPath)
	if err != nil || spec == nil {
		return []credentials.AgentNode{}
	}
	tools := make([]string, len(spec.AgentSpec.Tools))
	copy(tools, spec.AgentSpec.Tools)
	return []credentials.AgentNode{{
		Tools:    tools,
		NodeType: spec.AgentSpec.AgentType,
	}}
}

// checkAgentCredentialStore mirrors the store validate_agent_credentials builds
// upstream: env vars take priority, the encrypted store is the fallback when a
// credential key is available. Built per request so a freshly saved key is
// visible without a restart and no stale cache can mask it.
//
// Parity: framework/credentials/validation.py::validate_agent_credentials
func checkAgentCredentialStore(home hivehome.Home) *credentials.CredentialStore {
	envStorage := credentials.NewEnvVarStorage(credentials.RegistryEnvMapping())
	if os.Getenv(credentials.CredentialKeyEnvVar) == "" {
		return credentials.NewCredentialStore(credentials.WithStorage(envStorage))
	}
	encrypted, err := credentials.NewEncryptedFileStorage(home.CredentialsDir(), "")
	if err != nil {
		return credentials.NewCredentialStore(credentials.WithStorage(envStorage))
	}
	return credentials.NewCredentialStore(
		credentials.WithStorage(credentials.NewCompositeStorage(envStorage, encrypted)),
	)
}

// agentCredentialEnvVars lists the env vars ensure_credential_key_env loads
// from shell config: every CREDENTIAL_SPECS env var except the two bootstrap
// keys (bootstrap skips those itself). Unmerged credential modules are not in
// CREDENTIAL_SPECS, so their env vars stay out here too.
func agentCredentialEnvVars() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0, len(credentials.GeneratedCredentialSpecs))
	for _, spec := range credentials.GeneratedCredentialSpecs {
		name := strings.TrimSpace(spec.EnvVar)
		if name == "" {
			continue
		}
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if credentials.UnmergedCredentialID(credentialID) {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

// configuredLLMEnvVar reads llm.api_key_env_var from configuration.json, the
// env var ensure_credential_key_env also backfills from shell config.
//
// Parity: framework/config.py::get_hive_config
func configuredLLMEnvVar(home hivehome.Home) string {
	section := hiveconf.Section(home, "llm")
	if section == nil {
		return ""
	}
	value, ok := section["api_key_env_var"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
