package credentials

// Parity: framework/credentials/catalog.py

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const MaxBrowseProviders = 40

type CredentialSpec struct {
	CredentialID     string
	AdenProviderName string
	Description      string
}

type NamedSpec struct {
	Name string
	Spec CredentialSpec
	// EnvVar is the credential's env fallback (upstream CredentialSpec.env_var).
	// The default adapter maps names to it so env-only keys resolve.
	EnvVar string
}

type CatalogAdapter interface {
	Store() *CredentialStore
	GetAllAccountInfo() []map[string]any
}

func AccountProvider(acct map[string]any) string {
	if value, ok := acct["provider"]; ok && pyTruthy(value) {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	if value, ok := acct["credential_id"]; ok && pyTruthy(value) {
		return strings.TrimSpace(fmt.Sprint(value))
	}
	return ""
}

func formatIdentity(identity any) string {
	dict, ok := identity.(map[string]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, len(dict))
	for _, key := range sortedKeys(dict) {
		value := dict[key]
		if pyTruthy(value) {
			parts = append(parts, fmt.Sprint(value))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func sortedKeys(dict map[string]any) []string {
	keys := make([]string, 0, len(dict))
	for key := range dict {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func FormatAccountLine(acct map[string]any, includeProvider, includeStatus bool) string {
	return FormatAccountLineWith(acct, includeProvider, includeStatus, "default")
}

func FormatAccountLineWith(acct map[string]any, includeProvider, includeStatus bool, defaultAlias string) string {
	provider := ""
	if includeProvider {
		provider = AccountProvider(acct) + " "
	}
	alias := defaultAlias
	if value, ok := acct["alias"]; ok && pyTruthy(value) {
		alias = fmt.Sprint(value)
	}
	source := any("aden")
	if value, ok := acct["source"]; ok {
		source = value
	}
	tag := ""
	if source == "local" {
		tag = " [local]"
	}
	statusTag := ""
	if includeStatus {
		if status, ok := acct["status"]; ok && pyTruthy(status) && fmt.Sprint(status) != "active" {
			statusTag = " — " + fmt.Sprint(status)
		}
	}
	return "- " + provider + `"` + alias + `"` + formatIdentity(acct["identity"]) + tag + statusTag
}

type PublicAccount struct {
	Provider string
	Alias    string
	Identity any
	Source   any
	Status   any
}

func (a PublicAccount) MarshalJSON() ([]byte, error) {
	return marshalOrdered([][2]any{
		{"provider", a.Provider},
		{"alias", a.Alias},
		{"identity", a.Identity},
		{"source", a.Source},
		{"status", a.Status},
	})
}

func (a PublicAccount) toDict() map[string]any {
	return map[string]any{
		"provider": a.Provider,
		"alias":    a.Alias,
		"identity": a.Identity,
		"source":   a.Source,
		"status":   a.Status,
	}
}

func publicAccount(acct map[string]any) PublicAccount {
	alias := "default"
	if value, ok := acct["alias"]; ok && pyTruthy(value) {
		alias = fmt.Sprint(value)
	}
	var identity any
	if value, ok := acct["identity"]; ok {
		if _, isDict := value.(map[string]any); isDict {
			identity = value
		}
	}
	source := any("aden")
	if value, ok := acct["source"]; ok {
		source = value
	}
	status := any("active")
	if value, ok := acct["status"]; ok && pyTruthy(value) {
		status = value
	}
	return PublicAccount{
		Provider: AccountProvider(acct),
		Alias:    alias,
		Identity: identity,
		Source:   source,
		Status:   status,
	}
}

type Connectable struct {
	CredentialID string
	Description  string
}

func (c Connectable) MarshalJSON() ([]byte, error) {
	return marshalOrdered([][2]any{
		{"credential_id", c.CredentialID},
		{"description", c.Description},
	})
}

type BrowseResult struct {
	Connected            []PublicAccount
	Connectable          []Connectable
	ConnectableTruncated int
}

func (b BrowseResult) MarshalJSON() ([]byte, error) {
	connected := b.Connected
	if connected == nil {
		connected = []PublicAccount{}
	}
	connectable := b.Connectable
	if connectable == nil {
		connectable = []Connectable{}
	}
	return marshalOrdered([][2]any{
		{"ok", true},
		{"connected", connected},
		{"connectable", connectable},
		{"connectable_truncated", b.ConnectableTruncated},
	})
}

func Browse(adapter CatalogAdapter, query string, specs []NamedSpec) BrowseResult {
	accounts := []map[string]any{}
	if adapter != nil {
		accounts = adapter.GetAllAccountInfo()
	}

	q := strings.ToLower(strings.TrimSpace(query))
	connectedProviders := make(map[string]struct{}, len(accounts))
	for _, acct := range accounts {
		connectedProviders[AccountProvider(acct)] = struct{}{}
	}
	shown := make([]map[string]any, 0, len(accounts))
	for _, acct := range accounts {
		if q == "" || strings.Contains(strings.ToLower(FormatAccountLine(acct, true, true)), q) {
			shown = append(shown, acct)
		}
	}

	connectable := make([]Connectable, 0)
	for _, named := range specs {
		credID := named.Spec.CredentialID
		if credID == "" {
			credID = named.Name
		}
		provider := named.Spec.AdenProviderName
		if provider == "" {
			provider = credID
		}
		if _, ok := connectedProviders[provider]; ok {
			continue
		}
		if _, ok := connectedProviders[credID]; ok {
			continue
		}
		desc := named.Spec.Description
		if q != "" && !strings.Contains(strings.ToLower(credID+" "+desc), q) {
			continue
		}
		connectable = append(connectable, Connectable{CredentialID: credID, Description: desc})
	}
	sort.SliceStable(connectable, func(i, j int) bool {
		return connectable[i].CredentialID < connectable[j].CredentialID
	})

	truncated := 0
	if len(connectable) > MaxBrowseProviders {
		truncated = len(connectable) - MaxBrowseProviders
		connectable = connectable[:MaxBrowseProviders]
	}

	connected := make([]PublicAccount, 0, len(shown))
	for _, acct := range shown {
		connected = append(connected, publicAccount(acct))
	}

	return BrowseResult{
		Connected:            connected,
		Connectable:          connectable,
		ConnectableTruncated: truncated,
	}
}

func RenderBrowse(result BrowseResult) string {
	lines := []string{"# Available credentials", ""}
	connected := result.Connected
	if len(connected) > 0 {
		lines = append(lines, "Connected (usable now via account=<alias>):")
		for _, acct := range connected {
			lines = append(lines, FormatAccountLine(acct.toDict(), true, true))
		}
		lines = append(lines, "")
	} else {
		lines = append(lines, "No credentials are connected yet.")
		lines = append(lines, "")
	}
	connectable := result.Connectable
	if len(connectable) > 0 {
		lines = append(lines, "Connectable providers (ask the user to connect one with the credentials tool, action=collect):")
		for _, c := range connectable {
			lines = append(lines, strings.TrimRight("- "+c.CredentialID+" — "+c.Description, " —"))
		}
		if result.ConnectableTruncated > 0 {
			lines = append(lines, fmt.Sprintf("… and %d more (pass a query to filter).", result.ConnectableTruncated))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "`hive-cred inspect <id> --account <alias>` for details; `hive-cred token <id> --account <alias>` for a fresh value.")
	return strings.Join(lines, "\n")
}

type InspectResult struct {
	CredentialID string
	Account      *string
	Accounts     []PublicAccount
	Keys         []string
	Type         *string
}

func (r InspectResult) MarshalJSON() ([]byte, error) {
	accounts := r.Accounts
	if accounts == nil {
		accounts = []PublicAccount{}
	}
	keys := r.Keys
	if keys == nil {
		keys = []string{}
	}
	return marshalOrdered([][2]any{
		{"ok", true},
		{"credential_id", r.CredentialID},
		{"account", r.Account},
		{"accounts", accounts},
		{"keys", keys},
		{"type", r.Type},
	})
}

func Inspect(adapter CatalogAdapter, credentialID, account string) InspectResult {
	accounts := []map[string]any{}
	if adapter != nil {
		accounts = adapter.GetAllAccountInfo()
	}
	filtered := make([]map[string]any, 0, len(accounts))
	for _, acct := range accounts {
		if AccountProvider(acct) == credentialID {
			filtered = append(filtered, acct)
			continue
		}
		if value, ok := acct["credential_id"]; ok && pyEqual(value, credentialID) {
			filtered = append(filtered, acct)
		}
	}
	if account != "" {
		kept := make([]map[string]any, 0, len(filtered))
		for _, acct := range filtered {
			alias := "default"
			if value, ok := acct["alias"]; ok && pyTruthy(value) {
				alias = fmt.Sprint(value)
			}
			if alias == account {
				kept = append(kept, acct)
			}
		}
		filtered = kept
	}

	keys := []string{}
	var credType *string
	if adapter != nil {
		if store := adapter.Store(); store != nil {
			if cred, err := store.GetCredential(credentialID, false); err == nil && cred != nil {
				for _, name := range cred.Keys.Names() {
					if !strings.HasPrefix(name, "_") {
						keys = append(keys, name)
					}
				}
				sort.Strings(keys)
				if cred.CredentialType != "" {
					value := string(cred.CredentialType)
					credType = &value
				}
			}
		}
	}

	public := make([]PublicAccount, 0, len(filtered))
	for _, acct := range filtered {
		public = append(public, publicAccount(acct))
	}

	var accountPtr *string
	if account != "" {
		value := account
		accountPtr = &value
	}

	return InspectResult{
		CredentialID: credentialID,
		Account:      accountPtr,
		Accounts:     public,
		Keys:         keys,
		Type:         credType,
	}
}

func RenderInspect(result InspectResult) string {
	header := "# " + result.CredentialID
	if result.Account != nil && *result.Account != "" {
		header += ` (account "` + *result.Account + `")`
	}
	out := []string{header}
	if len(result.Accounts) > 0 {
		out = append(out, "Accounts:")
		for _, acct := range result.Accounts {
			out = append(out, FormatAccountLine(acct.toDict(), true, true))
		}
	} else {
		out = append(out, "No connected account found for this id.")
	}
	if len(result.Keys) > 0 {
		out = append(out, "Keys: "+strings.Join(result.Keys, ", "))
	}
	if result.Type != nil && *result.Type != "" {
		out = append(out, "Type: "+*result.Type)
	}
	out = append(out, "")
	out = append(out, "To use it, pass account=<alias> to the relevant tool. Need the raw value in a script? `hive-cred token` fetches a fresh one at call time.")
	return strings.Join(out, "\n")
}

func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return true
	}
}

func pyEqual(value any, want string) bool {
	text, ok := value.(string)
	return ok && text == want
}

func marshalOrdered(pairs [][2]any) ([]byte, error) {
	var builder strings.Builder
	builder.WriteString("{")
	for index, pair := range pairs {
		if index > 0 {
			builder.WriteString(",")
		}
		keyJSON, err := json.Marshal(pair[0])
		if err != nil {
			return nil, err
		}
		valueJSON, err := json.Marshal(pair[1])
		if err != nil {
			return nil, err
		}
		builder.Write(keyJSON)
		builder.WriteString(":")
		builder.Write(valueJSON)
	}
	builder.WriteString("}")
	return []byte(builder.String()), nil
}
