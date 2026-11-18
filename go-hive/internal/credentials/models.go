// matching "// Parity:" comments.
package credentials

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

type CredentialType string

const (
	CredentialTypeAPIKey      CredentialType = "api_key"
	CredentialTypeOAuth2      CredentialType = "oauth2"
	CredentialTypeBasicAuth   CredentialType = "basic_auth"
	CredentialTypeBearerToken CredentialType = "bearer_token"
	CredentialTypeCustom      CredentialType = "custom"
)

type CredentialKey struct {
	Name      string         `json:"name"`
	Value     string         `json:"value"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

func NewCredentialKey(name, value string) *CredentialKey {
	return &CredentialKey{Name: name, Value: value}
}

func (k *CredentialKey) IsExpired() bool {
	if k == nil || k.ExpiresAt == nil {
		return false
	}
	return !time.Now().UTC().Before(*k.ExpiresAt)
}

func (k *CredentialKey) GetSecretValue() string {
	if k == nil {
		return ""
	}
	return k.Value
}

type KeyMap struct {
	names  []string
	values map[string]*CredentialKey
}

func NewKeyMap(keys ...*CredentialKey) KeyMap {
	m := KeyMap{}
	for _, k := range keys {
		if k != nil {
			m.Set(k)
		}
	}
	return m
}

func (m *KeyMap) Set(k *CredentialKey) {
	if k == nil {
		return
	}
	if m.values == nil {
		m.values = map[string]*CredentialKey{}
	}
	if _, ok := m.values[k.Name]; !ok {
		m.names = append(m.names, k.Name)
	}
	m.values[k.Name] = k
}

func (m *KeyMap) Get(name string) *CredentialKey {
	if m == nil || m.values == nil {
		return nil
	}
	return m.values[name]
}

func (m *KeyMap) Has(name string) bool {
	return m.Get(name) != nil
}

func (m *KeyMap) Len() int {
	if m == nil {
		return 0
	}
	return len(m.names)
}

func (m *KeyMap) Names() []string {
	if m == nil {
		return nil
	}
	out := make([]string, len(m.names))
	copy(out, m.names)
	return out
}

func (m *KeyMap) Values() []*CredentialKey {
	if m == nil {
		return nil
	}
	out := make([]*CredentialKey, 0, len(m.names))
	for _, name := range m.names {
		out = append(out, m.values[name])
	}
	return out
}

func (m *KeyMap) First() *CredentialKey {
	if m == nil || len(m.names) == 0 {
		return nil
	}
	return m.values[m.names[0]]
}

func (m KeyMap) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range m.names {
		if i > 0 {
			buf.WriteByte(',')
		}
		nb, err := json.Marshal(name)
		if err != nil {
			return nil, err
		}
		buf.Write(nb)
		buf.WriteByte(':')
		kb, err := json.Marshal(m.values[name])
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func (m *KeyMap) UnmarshalJSON(data []byte) error {
	m.names = nil
	m.values = nil
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fmt.Errorf("credential keys must be a JSON object")
	}
	for dec.More() {
		nameTok, err := dec.Token()
		if err != nil {
			return err
		}
		name, ok := nameTok.(string)
		if !ok {
			return fmt.Errorf("credential key name must be a string")
		}
		var key CredentialKey
		if err := dec.Decode(&key); err != nil {
			return err
		}
		if key.Name == "" {
			key.Name = name
		}
		if m.values == nil {
			m.values = map[string]*CredentialKey{}
		}
		m.names = append(m.names, name)
		m.values[name] = &key
	}
	_, err = dec.Token()
	return err
}

type CredentialIdentity struct {
	Email     *string `json:"email,omitempty"`
	Username  *string `json:"username,omitempty"`
	Workspace *string `json:"workspace,omitempty"`
	AccountID *string `json:"account_id,omitempty"`
}

func firstNonEmpty(vals ...*string) string {
	for _, v := range vals {
		if v != nil && *v != "" {
			return *v
		}
	}
	return ""
}

func (i CredentialIdentity) Label() string {
	if label := firstNonEmpty(i.Email, i.Username, i.Workspace, i.AccountID); label != "" {
		return label
	}
	return "unknown"
}

func (i CredentialIdentity) IsKnown() bool {
	return firstNonEmpty(i.Email, i.Username, i.Workspace, i.AccountID) != ""
}

func (i CredentialIdentity) ToDict() map[string]any {
	out := map[string]any{}
	if i.Email != nil {
		out["email"] = *i.Email
	}
	if i.Username != nil {
		out["username"] = *i.Username
	}
	if i.Workspace != nil {
		out["workspace"] = *i.Workspace
	}
	if i.AccountID != nil {
		out["account_id"] = *i.AccountID
	}
	return out
}

type CredentialObject struct {
	ID             string         `json:"id"`
	CredentialType CredentialType `json:"credential_type"`
	Keys           KeyMap         `json:"keys"`

	ProviderID    string     `json:"provider_id,omitempty"`
	LastRefreshed *time.Time `json:"last_refreshed,omitempty"`
	AutoRefresh   bool       `json:"auto_refresh"`

	LastUsed *time.Time `json:"last_used,omitempty"`
	UseCount int        `json:"use_count"`

	Description string     `json:"description"`
	Tags        []string   `json:"tags"`
	CreatedAt   *time.Time `json:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

func NewCredentialObject(id string) *CredentialObject {
	now := time.Now().UTC()
	return &CredentialObject{
		ID:             id,
		CredentialType: CredentialTypeAPIKey,
		CreatedAt:      &now,
		UpdatedAt:      &now,
		Tags:           []string{},
	}
}

func (c *CredentialObject) GetKey(keyName string) *string {
	key := c.Keys.Get(keyName)
	if key == nil {
		return nil
	}
	v := key.GetSecretValue()
	return &v
}

func (c *CredentialObject) GetKeyValue(keyName string) (string, bool) {
	v := c.GetKey(keyName)
	if v == nil {
		return "", false
	}
	return *v, true
}

func (c *CredentialObject) SetKey(keyName, value string, expiresAt *time.Time, metadata map[string]any) {
	if metadata == nil {
		metadata = map[string]any{}
	}
	c.Keys.Set(&CredentialKey{Name: keyName, Value: value, ExpiresAt: expiresAt, Metadata: metadata})
	now := time.Now().UTC()
	c.UpdatedAt = &now
}

func (c *CredentialObject) HasKey(keyName string) bool {
	return c.Keys.Has(keyName)
}

func (c *CredentialObject) NeedsRefresh() bool {
	for _, key := range c.Keys.Values() {
		if key.IsExpired() {
			return true
		}
	}
	return false
}

func (c *CredentialObject) IsValid() bool {
	if c.Keys.Len() == 0 {
		return false
	}
	for _, key := range c.Keys.Values() {
		if !key.IsExpired() {
			return true
		}
	}
	return false
}

func (c *CredentialObject) RecordUsage() {
	now := time.Now().UTC()
	c.LastUsed = &now
	c.UseCount++
}

func (c *CredentialObject) GetDefaultKey() *string {
	for _, name := range []string{"value", "api_key", "access_token"} {
		if v := c.GetKey(name); v != nil {
			return v
		}
	}
	first := c.Keys.First()
	if first == nil {
		return nil
	}
	v := first.GetSecretValue()
	return &v
}

func (c *CredentialObject) Identity() CredentialIdentity {
	var id CredentialIdentity
	for _, name := range c.Keys.Names() {
		if len(name) <= len("_identity_") || name[:len("_identity_")] != "_identity_" {
			continue
		}
		field := name[len("_identity_"):]
		value := c.Keys.Get(name).GetSecretValue()
		v := value
		switch field {
		case "email":
			id.Email = &v
		case "username":
			id.Username = &v
		case "workspace":
			id.Workspace = &v
		case "account_id":
			id.AccountID = &v
		}
	}
	return id
}

func (c *CredentialObject) ProviderType() *string {
	if key := c.Keys.Get("_integration_type"); key != nil {
		v := key.GetSecretValue()
		return &v
	}
	return nil
}

func (c *CredentialObject) Alias() *string {
	if key := c.Keys.Get("_alias"); key != nil {
		v := key.GetSecretValue()
		return &v
	}
	return nil
}

func (c *CredentialObject) SetIdentity(fields map[string]string) {
	for name, value := range fields {
		if value != "" {
			c.SetKey("_identity_"+name, value, nil, nil)
		}
	}
}

type CredentialUsageSpec struct {
	CredentialID string            `json:"credential_id"`
	RequiredKeys []string          `json:"required_keys"`
	Headers      map[string]string `json:"headers"`
	QueryParams  map[string]string `json:"query_params"`
	BodyFields   map[string]string `json:"body_fields"`

	Required    bool   `json:"required"`
	Description string `json:"description"`
	HelpURL     string `json:"help_url"`
}

type CredentialError struct{ msg string }

func (e *CredentialError) Error() string { return e.msg }

func NewCredentialError(format string, args ...any) *CredentialError {
	return &CredentialError{msg: fmt.Sprintf(format, args...)}
}

type CredentialNotFoundError struct{ CredentialError }

func newCredentialNotFoundError(format string, args ...any) *CredentialNotFoundError {
	return &CredentialNotFoundError{CredentialError{msg: fmt.Sprintf(format, args...)}}
}

type CredentialKeyNotFoundError struct{ CredentialError }

func newCredentialKeyNotFoundError(format string, args ...any) *CredentialKeyNotFoundError {
	return &CredentialKeyNotFoundError{CredentialError{msg: fmt.Sprintf(format, args...)}}
}

type CredentialRefreshError struct{ CredentialError }

func newCredentialRefreshError(format string, args ...any) *CredentialRefreshError {
	return &CredentialRefreshError{CredentialError{msg: fmt.Sprintf(format, args...)}}
}

type CredentialValidationError struct{ CredentialError }

type CredentialDecryptionError struct{ CredentialError }

func newCredentialDecryptionError(format string, args ...any) *CredentialDecryptionError {
	return &CredentialDecryptionError{CredentialError{msg: fmt.Sprintf(format, args...)}}
}

type CredentialExpiredError struct {
	CredentialID string
	Provider     string
	Alias        string
	HelpURL      string
	msg          string
}

func (e *CredentialExpiredError) Error() string { return e.msg }

type AccountSelectionRequiredError struct {
	CredentialID      string
	Provider          string
	AvailableAccounts []map[string]any
	msg               string
}

func (e *AccountSelectionRequiredError) Error() string { return e.msg }
