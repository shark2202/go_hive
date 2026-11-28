package credentials

// Parity: framework/credentials/validation.py
//
// validate_agent_credentials checks that every credential an agent's nodes
// declare is present (env var, encrypted store) and — when asked — actually
// works (one lightweight HTTP call per present credential). compute_unavailable_tools
// is the same check tuned for spawn preflight: it returns the tool names whose
// credentials failed instead of raising.

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// AgentCredentialStore is the slice of *CredentialStore validate_agent_credentials
// reads. An interface keeps the validator testable without an encrypted store.
type AgentCredentialStore interface {
	IsAvailable(credentialID string) bool
	Get(credentialID string) (*string, error)
	GetCredential(credentialID string, refreshIfNeeded bool) (*CredentialObject, error)
	SaveCredential(credential *CredentialObject) error
}

// AgentCredentialStatus is the status of one required credential after
// validation.
// Parity: core/framework/credentials/validation.py::CredentialStatus
type AgentCredentialStatus struct {
	CredentialName        string
	CredentialID          string
	EnvVar                string
	Description           string
	HelpURL               string
	APIKeyInstructions    string
	Tools                 []string
	NodeTypes             []string
	Available             bool
	Valid                 *bool // nil = not checked
	ValidationMessage     *string
	AdenSupported         bool
	DirectAPIKeySupported bool
	CredentialKey         string
	AdenNotConnected      bool
	AlternativeGroup      *string
}

// AgentCredentialValidationResult is the result of validating every credential
// an agent requires.
// Parity: core/framework/credentials/validation.py::CredentialValidationResult
type AgentCredentialValidationResult struct {
	Credentials []AgentCredentialStatus
	HasAdenKey  bool
}

// ValidateAgentCredentialsOptions are the knobs of ValidateAgentCredentials.
// The zero value matches the upstream defaults: run health checks, never raise.
type ValidateAgentCredentialsOptions struct {
	// SkipVerification turns off the phase-2 health checks (upstream verify=False).
	SkipVerification bool
	// RaiseOnError makes a failed validation return a CredentialError
	// (upstream raise_on_error=True; the reference default).
	RaiseOnError bool
	// ForceRefresh re-syncs Aden tokens even when their env vars are already
	// set (upstream force_refresh). It only affects Aden-supported specs and
	// requires ADEN_API_KEY.
	ForceRefresh bool
	// HealthCheck overrides the registered checker table (tests).
	HealthCheck func(credentialName, credentialValue, healthCheckEndpoint, healthCheckMethod string) HealthCheckResult
	// AdenClient overrides the client used by the Aden token pre-sync (tests).
	// When nil, a client is built from ADEN_API_KEY and ADEN_API_URL.
	AdenClient AdenTokenClient
}

// RegistryEnvMapping maps every merged CREDENTIAL_SPECS credential id to its
// env var — the env mapping validate_agent_credentials hands EnvVarStorage.
func RegistryEnvMapping() map[string]string {
	mapping := map[string]string{}
	for _, row := range mergedAgentSpecRows() {
		if row.spec.EnvVar == "" {
			continue
		}
		mapping[row.credentialID] = row.spec.EnvVar
	}
	return mapping
}

type agentSpecRow struct {
	spec         GeneratedCredentialSpec
	name         string
	credentialID string
}

// mergedAgentSpecRows projects GeneratedCredentialSpecs onto the rows
// CREDENTIAL_SPECS actually contains: unmerged credential modules (x.py) and
// duplicate ids are filtered out, exactly like the reference registry.
func mergedAgentSpecRows() []agentSpecRow {
	rows := make([]agentSpecRow, 0, len(GeneratedCredentialSpecs))
	seen := make(map[string]struct{}, len(GeneratedCredentialSpecs))
	for _, spec := range GeneratedCredentialSpecs {
		credentialID := spec.CredentialID
		if credentialID == "" {
			credentialID = spec.Name
		}
		if UnmergedCredentialID(credentialID) {
			continue
		}
		if _, duplicate := seen[credentialID]; duplicate {
			continue
		}
		seen[credentialID] = struct{}{}
		name := spec.Name
		if name == "" {
			name = credentialID
		}
		rows = append(rows, agentSpecRow{spec: spec, name: name, credentialID: credentialID})
	}
	return rows
}

// ValidateAgentCredentials checks that required credentials are available and,
// unless skipped, valid. When ADEN_API_KEY is set it first syncs Aden-backed
// OAuth tokens into the environment (presence needs them even when health
// checks are skipped), so validation sees fresh tokens instead of stale store
// values.
//
// Parity: core/framework/credentials/validation.py::validate_agent_credentials
func ValidateAgentCredentials(nodes []AgentNode, store AgentCredentialStore, opts ValidateAgentCredentialsOptions) (AgentCredentialValidationResult, error) {
	result := AgentCredentialValidationResult{Credentials: []AgentCredentialStatus{}}
	rows := mergedAgentSpecRows()
	if len(rows) == 0 {
		return result, nil
	}

	requiredTools := map[string]struct{}{}
	nodeTypes := map[string]struct{}{}
	for _, node := range nodes {
		for _, tool := range node.Tools {
			requiredTools[tool] = struct{}{}
		}
		if node.NodeType != "" {
			nodeTypes[node.NodeType] = struct{}{}
		}
	}

	// Reverse mappings — 1:many for multi-provider tools (send_email → resend
	// OR google), last-wins for node types.
	toolToCreds := map[string][]string{}
	nodeTypeToCred := map[string]string{}
	specByName := make(map[string]GeneratedCredentialSpec, len(rows))
	for _, row := range rows {
		specByName[row.name] = row.spec
		for _, tool := range row.spec.Tools {
			toolToCreds[tool] = append(toolToCreds[tool], row.name)
		}
		for _, nodeType := range row.spec.NodeTypes {
			nodeTypeToCred[nodeType] = row.name
		}
	}

	hasAdenKey := os.Getenv(AdenEnvVar) != ""
	result.HasAdenKey = hasAdenKey
	if hasAdenKey {
		// Presence validation reads env vars: refresh Aden-backed tokens
		// before any status row is built.
		specs := make([]GeneratedCredentialSpec, len(rows))
		for index, row := range rows {
			specs[index] = row.spec
		}
		presyncAdenTokens(specs, opts.ForceRefresh, opts.AdenClient)
	}
	checked := map[string]bool{}
	toVerify := []int{}

	checkCredential := func(spec GeneratedCredentialSpec, credName string, affectedTools, affectedNodeTypes []string, alternativeGroup *string) {
		status, needVerify := buildAgentCredentialStatus(spec, credName, affectedTools, affectedNodeTypes, alternativeGroup, store, hasAdenKey)
		result.Credentials = append(result.Credentials, status)
		if needVerify && !opts.SkipVerification {
			toVerify = append(toVerify, len(result.Credentials)-1)
		}
	}

	affectedToolsFor := func(spec GeneratedCredentialSpec) []string {
		affected := []string{}
		for tool := range requiredTools {
			for _, declared := range spec.Tools {
				if declared == tool {
					affected = append(affected, tool)
					break
				}
			}
		}
		sort.Strings(affected)
		return affected
	}

	// Check tool credentials.
	for _, toolName := range sortedSet(requiredTools) {
		credNames, present := toolToCreds[toolName]
		if !present {
			continue
		}
		unchecked := make([]string, 0, len(credNames))
		for _, name := range credNames {
			if !checked[name] {
				unchecked = append(unchecked, name)
			}
		}
		if len(unchecked) == 0 {
			continue
		}

		// Single provider — existing behavior.
		if len(unchecked) == 1 {
			credName := unchecked[0]
			checked[credName] = true
			spec := specByName[credName]
			if !spec.Required {
				continue
			}
			checkCredential(spec, credName, affectedToolsFor(spec), []string{}, nil)
			continue
		}

		// Multi-provider (e.g. send_email → resend OR google): satisfied if ANY
		// provider credential is available.
		availableCred := ""
		for _, name := range unchecked {
			spec := specByName[name]
			credentialID := spec.CredentialID
			if credentialID == "" {
				credentialID = name
			}
			if store != nil && store.IsAvailable(credentialID) {
				availableCred = name
				break
			}
		}
		if availableCred != "" {
			checked[availableCred] = true
			spec := specByName[availableCred]
			checkCredential(spec, availableCred, affectedToolsFor(spec), []string{}, nil)
			continue
		}
		// None available — report ALL alternatives so the modal can show them.
		group := toolName
		for _, name := range unchecked {
			checked[name] = true
			spec := specByName[name]
			groupCopy := group
			checkCredential(spec, name, affectedToolsFor(spec), []string{}, &groupCopy)
		}
	}

	// Check node type credentials (e.g. an API key for a node type). The
	// generated registry declares no node types today; the branch stays for
	// parity with the reference.
	for _, nodeType := range sortedSet(nodeTypes) {
		credName, present := nodeTypeToCred[nodeType]
		if !present || checked[credName] {
			continue
		}
		checked[credName] = true
		spec := specByName[credName]
		if !spec.Required {
			continue
		}
		affected := []string{}
		for candidate := range nodeTypes {
			for _, declared := range spec.NodeTypes {
				if declared == candidate {
					affected = append(affected, candidate)
					break
				}
			}
		}
		sort.Strings(affected)
		checkCredential(spec, credName, []string{}, affected, nil)
	}

	// Phase 2: health-check present credentials.
	healthCheck := opts.HealthCheck
	if healthCheck == nil {
		healthCheck = CheckCredentialHealth
	}
	for _, index := range toVerify {
		status := &result.Credentials[index]
		spec := specByName[status.CredentialName]
		var value *string
		if store != nil {
			loaded, err := store.Get(status.CredentialID)
			if err != nil {
				return result, err
			}
			value = loaded
		}
		if value == nil || *value == "" {
			continue
		}
		checkedResult := healthCheck(status.CredentialName, *value, spec.HealthCheckEndpoint, spec.HealthCheckMethod)
		valid := checkedResult.Valid
		message := checkedResult.Message
		status.Valid = &valid
		status.ValidationMessage = &message
		if valid {
			persistHealthIdentity(store, status.CredentialID, checkedResult.Details)
		}
	}

	if opts.RaiseOnError && result.HasErrors() {
		return result, NewCredentialError("%s", result.FormatErrorMessage())
	}
	return result, nil
}

// buildAgentCredentialStatus projects one merged credential spec into the row
// the check-agent endpoint reports; needVerify reports whether the credential
// is available with a health-check endpoint (caller still applies SkipVerification).
// Parity: core/framework/credentials/validation.py::validate_agent_credentials
func buildAgentCredentialStatus(spec GeneratedCredentialSpec, credName string, affectedTools, affectedNodeTypes []string, alternativeGroup *string, store AgentCredentialStore, hasAdenKey bool) (AgentCredentialStatus, bool) {
	credentialID := spec.CredentialID
	if credentialID == "" {
		credentialID = credName
	}
	available := false
	if store != nil {
		available = store.IsAvailable(credentialID)
	}
	adenNotConnected := !available && hasAdenKey && spec.AdenSupported && !spec.DirectAPIKeySupported
	status := AgentCredentialStatus{
		CredentialName:        credName,
		CredentialID:          credentialID,
		EnvVar:                spec.EnvVar,
		Description:           spec.Description,
		HelpURL:               spec.HelpURL,
		APIKeyInstructions:    spec.APIKeyInstructions,
		Tools:                 copyStrings(affectedTools),
		NodeTypes:             copyStrings(affectedNodeTypes),
		Available:             available,
		AdenSupported:         spec.AdenSupported,
		DirectAPIKeySupported: spec.DirectAPIKeySupported,
		CredentialKey:         spec.CredentialKey,
		AdenNotConnected:      adenNotConnected,
		AlternativeGroup:      alternativeGroup,
	}
	return status, available && spec.HealthCheckEndpoint != ""
}

// persistHealthIdentity mirrors the reference's best-effort write of identity
// fields a health check learned (Brevo email/company, Telegram username).
func persistHealthIdentity(store AgentCredentialStore, credentialID string, details map[string]any) {
	if store == nil {
		return
	}
	raw, present := details["identity"]
	if !present {
		return
	}
	identity, ok := raw.(map[string]any)
	if !ok || len(identity) == 0 {
		return
	}
	fields := make(map[string]string, len(identity))
	for key, value := range identity {
		if text, isString := value.(string); isString && text != "" {
			fields[key] = text
		}
	}
	if len(fields) == 0 {
		return
	}
	credential, err := store.GetCredential(credentialID, false)
	if err != nil || credential == nil {
		return
	}
	credential.SetIdentity(fields)
	_ = store.SaveCredential(credential)
}

// Failed lists the credentials that are missing, invalid, or
// Aden-not-connected. For alternative groups the group is satisfied if ANY
// member is available and valid — failures are reported only when the whole
// group is unsatisfied.
//
// Parity: core/framework/credentials/validation.py::CredentialValidationResult.failed
func (r AgentCredentialValidationResult) Failed() []AgentCredentialStatus {
	altSatisfied := map[string]bool{}
	for _, credential := range r.Credentials {
		if credential.AlternativeGroup == nil {
			continue
		}
		group := *credential.AlternativeGroup
		if _, present := altSatisfied[group]; !present {
			altSatisfied[group] = false
		}
		if credential.Available && !isInvalid(&credential) {
			altSatisfied[group] = true
		}
	}

	failed := []AgentCredentialStatus{}
	for _, credential := range r.Credentials {
		if credential.AlternativeGroup != nil {
			if altSatisfied[*credential.AlternativeGroup] {
				continue
			}
			if !credential.Available || isInvalid(&credential) {
				failed = append(failed, credential)
			}
			continue
		}
		if !credential.Available || isInvalid(&credential) {
			failed = append(failed, credential)
		}
	}
	return failed
}

func isInvalid(status *AgentCredentialStatus) bool {
	return status.Valid != nil && !*status.Valid
}

// HasErrors reports whether any required credential failed.
func (r AgentCredentialValidationResult) HasErrors() bool {
	return len(r.Failed()) > 0
}

// FailedCredNames lists credential names that need (re-)collection, excluding
// Aden-not-connected rows.
func (r AgentCredentialValidationResult) FailedCredNames() []string {
	names := []string{}
	for _, credential := range r.Failed() {
		if credential.AdenNotConnected {
			continue
		}
		names = append(names, credential.CredentialName)
	}
	return names
}

// FormatErrorMessage renders the human-readable CLI/runner error message.
// Parity: core/framework/credentials/validation.py::CredentialValidationResult.format_error_message
func (r AgentCredentialValidationResult) FormatErrorMessage() string {
	missing := []AgentCredentialStatus{}
	invalid := []AgentCredentialStatus{}
	adenNotConnected := []AgentCredentialStatus{}
	for _, credential := range r.Credentials {
		if !credential.Available && !credential.AdenNotConnected {
			missing = append(missing, credential)
		}
		if credential.Available && isInvalid(&credential) {
			invalid = append(invalid, credential)
		}
		if credential.AdenNotConnected {
			adenNotConnected = append(adenNotConnected, credential)
		}
	}

	lines := []string{}
	if len(missing) > 0 {
		lines = append(lines, "Missing credentials:\n")
		for _, credential := range missing {
			entry := "  " + credential.EnvVar + " for " + agentCredentialLabel(credential)
			if credential.HelpURL != "" {
				entry += "\n    Get it at: " + credential.HelpURL
			}
			lines = append(lines, entry)
		}
	}
	if len(invalid) > 0 {
		if len(missing) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Invalid or expired credentials:\n")
		for _, credential := range invalid {
			message := ""
			if credential.ValidationMessage != nil {
				message = *credential.ValidationMessage
			}
			entry := "  " + credential.EnvVar + " for " + agentCredentialLabel(credential) + " — " + message
			if credential.HelpURL != "" {
				entry += "\n    Get a new key at: " + credential.HelpURL
			}
			lines = append(lines, entry)
		}
	}
	if len(adenNotConnected) > 0 {
		if len(missing) > 0 || len(invalid) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "Aden integrations not connected (ADEN_API_KEY is set but OAuth tokens unavailable):\n")
		for _, credential := range adenNotConnected {
			lines = append(lines, "  "+credential.EnvVar+" for "+agentCredentialLabel(credential)+"\n    Connect this integration at hive.adenhq.com first.")
		}
	}
	lines = append(lines, "\nIf you've already set up credentials, restart your terminal to load them.")
	return strings.Join(lines, "\n")
}

func agentCredentialLabel(credential AgentCredentialStatus) string {
	if len(credential.Tools) > 0 {
		return strings.Join(credential.Tools, ", ")
	}
	if len(credential.NodeTypes) > 0 {
		return strings.Join(credential.NodeTypes, ", ") + " nodes"
	}
	return credential.CredentialName
}

// ComputeUnavailableTools names the tools bound to failed credentials so a
// spawn preflight can drop them from the worker's tool list.
//
// Parity: core/framework/credentials/validation.py::compute_unavailable_tools
func ComputeUnavailableTools(nodes []AgentNode, store AgentCredentialStore) (map[string]bool, []string) {
	result, err := ValidateAgentCredentials(nodes, store, ValidateAgentCredentialsOptions{SkipVerification: true})
	if err != nil {
		return map[string]bool{}, []string{}
	}

	dropped := map[string]bool{}
	messages := []string{}
	for _, status := range result.Failed() {
		if len(status.Tools) == 0 {
			continue
		}
		for _, tool := range status.Tools {
			dropped[tool] = true
		}
		reason := "missing"
		if status.AdenNotConnected {
			reason = "aden_not_connected"
		} else if status.Available && isInvalid(&status) {
			reason = "invalid"
		}
		sample := status.Tools
		suffix := ""
		if len(sample) > 6 {
			sample = sample[:6]
			suffix = fmt.Sprintf(" +%d more", len(status.Tools)-6)
		}
		messages = append(messages, fmt.Sprintf("%s (%s) → drops %d tool(s): %s%s",
			status.EnvVar, reason, len(status.Tools), strings.Join(sample, ", "), suffix))
	}
	return dropped, messages
}

func copyStrings(values []string) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}

func sortedSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
