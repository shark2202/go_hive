package credentials

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type CredentialStorage interface {
	Save(credential *CredentialObject) error
	Load(credentialID string) (*CredentialObject, error)
	Delete(credentialID string) (bool, error)
	ListAll() ([]string, error)
	Exists(credentialID string) (bool, error)
}

type ProviderAccountStorage interface {
	LoadAllForProvider(providerName string) ([]*CredentialObject, error)
}

type AliasStorage interface {
	LoadByAlias(providerName, alias string) (*CredentialObject, error)
}

const IndexVersion = "2.0"

var indexInternalKeyNames = map[string]bool{"_alias": true, "_integration_type": true}

type IndexEntry struct {
	CredentialType string         `json:"credential_type"`
	Provider       *string        `json:"provider"`
	Alias          *string        `json:"alias"`
	Identity       map[string]any `json:"identity"`
	KeyNames       []string       `json:"key_names"`
	CreatedAt      *string        `json:"created_at"`
	UpdatedAt      *string        `json:"updated_at"`
	LastRefreshed  *string        `json:"last_refreshed"`
	ExpiresAt      *string        `json:"expires_at"`
	AutoRefresh    bool           `json:"auto_refresh"`
	Tags           []string       `json:"tags"`
}

type IndexEnvelope struct {
	Version     string                `json:"version"`
	StorePath   string                `json:"store_path,omitempty"`
	GeneratedAt string                `json:"generated_at,omitempty"`
	Credentials map[string]IndexEntry `json:"credentials"`
}

type EncryptedFileStorage struct {
	basePath string
	keyEnv   string
	fernet   *Fernet

	// indexMu serialises index read-modify-write cycles. The index is a
	// single file rebased on every credential save; without it concurrent
	// saves lose each other's entries.
	indexMu sync.Mutex
}

func NewEncryptedFileStorage(basePath, encryptionKey string) (*EncryptedFileStorage, error) {
	return NewEncryptedFileStorageWithOptions(basePath, encryptionKey, CredentialKeyEnvVar)
}

func NewEncryptedFileStorageWithOptions(basePath, encryptionKey, keyEnvVar string) (*EncryptedFileStorage, error) {
	if basePath == "" {
		home, err := hivehome.Resolve()
		if err != nil {
			return nil, err
		}
		basePath = filepath.Join(home.Root, "credentials")
	}
	s := &EncryptedFileStorage{basePath: basePath, keyEnv: keyEnvVar}
	if err := s.ensureDirs(); err != nil {
		return nil, err
	}

	key := encryptionKey
	if key == "" {
		key = os.Getenv(keyEnvVar)
	}
	if key == "" {
		generated, err := GenerateFernetKey()
		if err != nil {
			return nil, err
		}
		key = generated
		LogWarning("framework.credentials.storage",
			"credentials: Generated new encryption key. To persist credentials across restarts, set %s", keyEnvVar)
	}
	f, err := NewFernet(key)
	if err != nil {
		return nil, err
	}
	s.fernet = f

	if err := s.maybeRebuildIndex(); err != nil {
		return nil, fmt.Errorf("prepare credentials index: %w", err)
	}
	return s, nil
}

func (s *EncryptedFileStorage) BasePath() string { return s.basePath }

func (s *EncryptedFileStorage) ensureDirs() error {
	for _, sub := range []string{"credentials", "metadata"} {
		dir := filepath.Join(s.basePath, sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		// MkdirAll leaves an existing directory's mode alone; the store holds
		// encrypted credentials and their metadata, so tighten it explicitly.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	return nil
}

func safeCredentialID(id string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", "..", "_")
	return replacer.Replace(id)
}

func (s *EncryptedFileStorage) credPath(id string) string {
	return filepath.Join(s.basePath, "credentials", safeCredentialID(id)+".enc")
}

func (s *EncryptedFileStorage) indexPath() string {
	return filepath.Join(s.basePath, "metadata", "index.json")
}

func (s *EncryptedFileStorage) Save(credential *CredentialObject) error {
	data, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("marshal credential %q: %w", credential.ID, err)
	}
	token, err := s.fernet.Encrypt(data)
	if err != nil {
		return fmt.Errorf("encrypt credential %q: %w", credential.ID, err)
	}
	if err := atomicfs.WriteFile(s.credPath(credential.ID), []byte(token), 0o600); err != nil {
		return fmt.Errorf("write credential %q: %w", credential.ID, err)
	}
	if err := s.indexUpsert(credential); err != nil {
		return fmt.Errorf("update credentials index for %q: %w", credential.ID, err)
	}
	return nil
}

func (s *EncryptedFileStorage) Load(credentialID string) (*CredentialObject, error) {
	path := s.credPath(credentialID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	plain, err := s.fernet.Decrypt(string(raw))
	if err != nil {
		return nil, newCredentialDecryptionError("Failed to decrypt credential '%s': %v", credentialID, err)
	}
	var credential CredentialObject
	if err := json.Unmarshal(plain, &credential); err != nil {
		return nil, newCredentialDecryptionError("Failed to decrypt credential '%s': %v", credentialID, err)
	}
	if credential.CredentialType == "" {
		credential.CredentialType = CredentialTypeAPIKey
	}
	return &credential, nil
}

func (s *EncryptedFileStorage) Delete(credentialID string) (bool, error) {
	path := s.credPath(credentialID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	if err := s.indexRemove(credentialID); err != nil {
		// The .enc file is gone; report the index failure so callers know the
		// listing is stale instead of silently returning a ghost entry.
		return true, fmt.Errorf("update credentials index for %q: %w", credentialID, err)
	}
	return true, nil
}

func (s *EncryptedFileStorage) ListAll() ([]string, error) {
	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(index.Credentials))
	for id := range index.Credentials {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *EncryptedFileStorage) Exists(credentialID string) (bool, error) {
	_, err := os.Stat(s.credPath(credentialID))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// LoadAllForProvider returns every stored credential whose index entry names
// providerName as its provider (_integration_type). It reads the metadata
// index — the same read-only projection ListAll uses — and decrypts each
// matching credential. An entry whose file no longer decrypts is skipped so one
// broken account cannot hide the others; the index itself is never rewritten.
func (s *EncryptedFileStorage) LoadAllForProvider(providerName string) ([]*CredentialObject, error) {
	if providerName == "" {
		return []*CredentialObject{}, nil
	}
	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(index.Credentials))
	for id, entry := range index.Credentials {
		if entry.Provider != nil && *entry.Provider == providerName {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]*CredentialObject, 0, len(ids))
	for _, id := range ids {
		credential, err := s.Load(id)
		if err != nil || credential == nil {
			continue
		}
		out = append(out, credential)
	}
	return out, nil
}

// LoadByAlias resolves one provider/alias pair through the metadata index and
// decrypts the matching credential. It returns (nil, nil) when no entry carries
// that pair, which is what lets callers fall back to the local account registry
// (upstream AdenCachedStorage.load_by_alias -> store.get_credential_by_alias).
func (s *EncryptedFileStorage) LoadByAlias(providerName, alias string) (*CredentialObject, error) {
	if providerName == "" || alias == "" {
		return nil, nil
	}
	index, err := s.readIndex()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, 1)
	for id, entry := range index.Credentials {
		if entry.Provider == nil || *entry.Provider != providerName {
			continue
		}
		if entry.Alias == nil || *entry.Alias != alias {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sort.Strings(ids)
	return s.Load(ids[0])
}

func (s *EncryptedFileStorage) readIndex() (IndexEnvelope, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	return s.readIndexLocked()
}

// readIndexLocked loads metadata/index.json. A corrupt (truncated, half
// written) index is a derived-artifact failure: rebuild it from the encrypted
// credential files instead of reporting "no credentials" and letting the next
// save overwrite every other entry.
func (s *EncryptedFileStorage) readIndexLocked() (IndexEnvelope, error) {
	var index IndexEnvelope
	if err := atomicfs.ReadJSON(s.indexPath(), &index); err != nil {
		if os.IsNotExist(err) {
			return IndexEnvelope{Version: IndexVersion, Credentials: map[string]IndexEntry{}}, nil
		}
		rebuilt, rebuildErr := s.rebuildIndexLocked()
		if rebuildErr != nil {
			return IndexEnvelope{}, fmt.Errorf("read credentials index (%v) and rebuild failed: %w", err, rebuildErr)
		}
		return rebuilt, nil
	}
	if index.Credentials == nil {
		index.Credentials = map[string]IndexEntry{}
	}
	return index, nil
}

func formatIndexTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}

func indexEntryFor(credential *CredentialObject) IndexEntry {
	visible := make([]string, 0, credential.Keys.Len())
	for _, name := range credential.Keys.Names() {
		if indexInternalKeyNames[name] || strings.HasPrefix(name, "_identity_") {
			continue
		}
		visible = append(visible, name)
	}
	sort.Strings(visible)

	var earliest *time.Time
	for _, key := range credential.Keys.Values() {
		if key.ExpiresAt == nil {
			continue
		}
		if earliest == nil || key.ExpiresAt.Before(*earliest) {
			t := *key.ExpiresAt
			earliest = &t
		}
	}

	return IndexEntry{
		CredentialType: string(credential.CredentialType),
		Provider:       credential.ProviderType(),
		Alias:          credential.Alias(),
		Identity:       credential.Identity().ToDict(),
		KeyNames:       visible,
		CreatedAt:      formatIndexTime(credential.CreatedAt),
		UpdatedAt:      formatIndexTime(credential.UpdatedAt),
		LastRefreshed:  formatIndexTime(credential.LastRefreshed),
		ExpiresAt:      formatIndexTime(earliest),
		AutoRefresh:    credential.AutoRefresh,
		Tags:           append([]string{}, credential.Tags...),
	}
}

func (s *EncryptedFileStorage) writeIndex(index IndexEnvelope) error {
	index.Version = IndexVersion
	index.StorePath = s.basePath
	index.GeneratedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if index.Credentials == nil {
		index.Credentials = map[string]IndexEntry{}
	}
	return atomicfs.WriteJSON(s.indexPath(), index, 0o600)
}

func (s *EncryptedFileStorage) indexUpsert(credential *CredentialObject) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	index, err := s.readIndexLocked()
	if err != nil {
		return err
	}
	if index.Version != IndexVersion {
		_, err := s.rebuildIndexLocked()
		return err
	}
	index.Credentials[credential.ID] = indexEntryFor(credential)
	return s.writeIndex(index)
}

func (s *EncryptedFileStorage) indexRemove(credentialID string) error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	index, err := s.readIndexLocked()
	if err != nil {
		return err
	}
	if index.Version != IndexVersion {
		_, err := s.rebuildIndexLocked()
		return err
	}
	delete(index.Credentials, credentialID)
	return s.writeIndex(index)
}

func (s *EncryptedFileStorage) maybeRebuildIndex() error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	path := s.indexPath()
	var index IndexEnvelope
	if err := atomicfs.ReadJSON(path, &index); err == nil && index.Version == IndexVersion {
		return nil
	}
	_, err := s.rebuildIndexLocked()
	return err
}

func (s *EncryptedFileStorage) rebuildIndex() error {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	_, err := s.rebuildIndexLocked()
	return err
}

func (s *EncryptedFileStorage) rebuildIndexLocked() (IndexEnvelope, error) {
	entries, err := os.ReadDir(filepath.Join(s.basePath, "credentials"))
	if err != nil {
		if os.IsNotExist(err) {
			return IndexEnvelope{Version: IndexVersion, Credentials: map[string]IndexEntry{}}, nil
		}
		return IndexEnvelope{}, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".enc") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)

	out := IndexEnvelope{Version: IndexVersion, Credentials: map[string]IndexEntry{}}
	for _, name := range names {
		id := strings.TrimSuffix(name, ".enc")
		credential, err := s.Load(id)
		if err != nil || credential == nil {
			continue
		}
		out.Credentials[credential.ID] = indexEntryFor(credential)
	}
	if err := s.writeIndex(out); err != nil {
		return out, err
	}
	return out, nil
}

var ErrEnvStorageReadOnlySave = errors.New("EnvVarStorage is read-only. Set environment variables externally or use EncryptedFileStorage.")

var ErrEnvStorageReadOnlyDelete = errors.New("EnvVarStorage is read-only. Unset environment variables externally.")

type EnvVarStorage struct {
	envMapping map[string]string
	dotenvPath string
}

func NewEnvVarStorage(envMapping map[string]string) *EnvVarStorage {
	return NewEnvVarStorageWithDotenv(envMapping, "")
}

func NewEnvVarStorageWithDotenv(envMapping map[string]string, dotenvPath string) *EnvVarStorage {
	if envMapping == nil {
		envMapping = map[string]string{}
	}
	if dotenvPath == "" {
		cwd, err := os.Getwd()
		if err == nil {
			dotenvPath = filepath.Join(cwd, ".env")
		}
	}
	return &EnvVarStorage{envMapping: envMapping, dotenvPath: dotenvPath}
}

func (s *EnvVarStorage) AddMapping(credentialID, envVar string) {
	s.envMapping[credentialID] = envVar
}

func (s *EnvVarStorage) envVarName(credentialID string) string {
	if name, ok := s.envMapping[credentialID]; ok {
		return name
	}
	return strings.ToUpper(strings.ReplaceAll(credentialID, "-", "_")) + "_API_KEY"
}

func (s *EnvVarStorage) readEnvValue(envVar string) *string {
	if value := os.Getenv(envVar); value != "" {
		v := value
		return &v
	}
	if s.dotenvPath != "" {
		if values := readDotenv(s.dotenvPath); values != nil {
			if value, ok := values[envVar]; ok {
				v := value
				return &v
			}
		}
	}
	return nil
}

func readDotenv(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		values[key] = value
	}
	return values
}

func (s *EnvVarStorage) Save(*CredentialObject) error { return ErrEnvStorageReadOnlySave }

func (s *EnvVarStorage) Load(credentialID string) (*CredentialObject, error) {
	envVar := s.envVarName(credentialID)
	value := s.readEnvValue(envVar)
	if value == nil {
		return nil, nil
	}
	cred := NewCredentialObject(credentialID)
	cred.CredentialType = CredentialTypeAPIKey
	cred.Keys.Set(NewCredentialKey("api_key", *value))
	cred.Description = "Loaded from " + envVar
	return cred, nil
}

func (s *EnvVarStorage) Delete(string) (bool, error) { return false, ErrEnvStorageReadOnlyDelete }

func (s *EnvVarStorage) ListAll() ([]string, error) {
	out := make([]string, 0, len(s.envMapping))
	for id := range s.envMapping {
		if ok, _ := s.Exists(id); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *EnvVarStorage) Exists(credentialID string) (bool, error) {
	return s.readEnvValue(s.envVarName(credentialID)) != nil, nil
}

type InMemoryStorage struct {
	mu   sync.Mutex
	data map[string]*CredentialObject
}

func NewInMemoryStorage(initial map[string]*CredentialObject) *InMemoryStorage {
	data := map[string]*CredentialObject{}
	for id, cred := range initial {
		data[id] = cred
	}
	return &InMemoryStorage{data: data}
}

func (s *InMemoryStorage) Save(credential *CredentialObject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string]*CredentialObject{}
	}
	s.data[credential.ID] = credential
	return nil
}

func (s *InMemoryStorage) Load(credentialID string) (*CredentialObject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[credentialID], nil
}

func (s *InMemoryStorage) Delete(credentialID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data[credentialID]; !ok {
		return false, nil
	}
	delete(s.data, credentialID)
	return true, nil
}

func (s *InMemoryStorage) ListAll() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data))
	for id := range s.data {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (s *InMemoryStorage) Exists(credentialID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[credentialID]
	return ok, nil
}

func (s *InMemoryStorage) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = map[string]*CredentialObject{}
}

type CompositeStorage struct {
	primary   CredentialStorage
	fallbacks []CredentialStorage
}

func NewCompositeStorage(primary CredentialStorage, fallbacks ...CredentialStorage) *CompositeStorage {
	return &CompositeStorage{primary: primary, fallbacks: fallbacks}
}

func (s *CompositeStorage) Save(credential *CredentialObject) error {
	return s.primary.Save(credential)
}

func (s *CompositeStorage) Load(credentialID string) (*CredentialObject, error) {
	credential, err := s.primary.Load(credentialID)
	if err != nil {
		return nil, err
	}
	if credential != nil {
		return credential, nil
	}
	for _, fallback := range s.fallbacks {
		credential, err := fallback.Load(credentialID)
		if err != nil {
			return nil, err
		}
		if credential != nil {
			return credential, nil
		}
	}
	return nil, nil
}

func (s *CompositeStorage) Delete(credentialID string) (bool, error) {
	return s.primary.Delete(credentialID)
}

func (s *CompositeStorage) ListAll() ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	add := func(ids []string) {
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	ids, err := s.primary.ListAll()
	if err != nil {
		return nil, err
	}
	add(ids)
	for _, fallback := range s.fallbacks {
		ids, err := fallback.ListAll()
		if err != nil {
			return nil, err
		}
		add(ids)
	}
	return out, nil
}

func (s *CompositeStorage) Exists(credentialID string) (bool, error) {
	ok, err := s.primary.Exists(credentialID)
	if err != nil {
		return false, err
	}
	if ok {
		return true, nil
	}
	for _, fallback := range s.fallbacks {
		ok, err := fallback.Exists(credentialID)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}
