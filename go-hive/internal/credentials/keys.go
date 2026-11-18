package credentials

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

const (
	CredentialKeyEnvVar = "HIVE_CREDENTIAL_KEY"
	AdenEnvVar          = "ADEN_API_KEY"
	AdenCredentialID    = "aden_api_key"
)

func CredentialKeyPath() (string, error) {
	home, err := hivehome.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.SecretsDir(), "credential_key"), nil
}

func LoadCredentialKey() (string, error) {
	if key := os.Getenv(CredentialKeyEnvVar); key != "" {
		persistKeyIfMissingOrStale(key)
		return key, nil
	}

	if key := readCredentialKeyFile(); key != "" {
		_ = os.Setenv(CredentialKeyEnvVar, key)
		return key, nil
	}

	if key := readFromShellConfig(CredentialKeyEnvVar); key != "" {
		_ = os.Setenv(CredentialKeyEnvVar, key)
		persistKeyIfMissingOrStale(key)
		return key, nil
	}
	return "", nil
}

func readCredentialKeyFile() string {
	path, err := CredentialKeyPath()
	if err != nil {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func persistKeyIfMissingOrStale(key string) {
	existing := readCredentialKeyFile()
	if existing == key {
		return
	}
	if err := saveCredentialKey(key); err != nil {
		path, _ := CredentialKeyPath()
		log.Printf(
			"credentials: failed to persist HIVE_CREDENTIAL_KEY to %s; future restarts without the env var may not decrypt the credential store: %v",
			path, err,
		)
	}
}

func saveCredentialKey(key string) error {
	path, err := CredentialKeyPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create secrets dir: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("chmod secrets dir: %w", err)
	}
	if err := atomicfs.WriteFile(path, []byte(key), 0o600); err != nil {
		return err
	}
	return os.Setenv(CredentialKeyEnvVar, key)
}

func SaveCredentialKey(key string) (string, error) {
	if err := saveCredentialKey(key); err != nil {
		return "", err
	}
	path, err := CredentialKeyPath()
	if err != nil {
		return "", err
	}
	return path, nil
}

func GenerateAndSaveCredentialKey() (string, error) {
	key, err := GenerateFernetKey()
	if err != nil {
		return "", err
	}
	if _, err := SaveCredentialKey(key); err != nil {
		return "", err
	}
	return key, nil
}

func LoadAdenAPIKey() (string, error) {
	if key := os.Getenv(AdenEnvVar); key != "" {
		return key, nil
	}
	if key := readAdenFromEncryptedStore(); key != "" {
		_ = os.Setenv(AdenEnvVar, key)
		return key, nil
	}
	if key := readFromShellConfig(AdenEnvVar); key != "" {
		_ = os.Setenv(AdenEnvVar, key)
		return key, nil
	}
	return "", nil
}

func readAdenFromEncryptedStore() string {
	if os.Getenv(CredentialKeyEnvVar) == "" {
		return ""
	}
	storage, err := NewEncryptedFileStorage("", "")
	if err != nil {
		return ""
	}
	credential, err := storage.Load(AdenCredentialID)
	if err != nil || credential == nil {
		return ""
	}
	if value := credential.GetKey("api_key"); value != nil {
		return *value
	}
	return ""
}

func SaveAdenAPIKey(key string) error {
	storage, err := NewEncryptedFileStorage("", "")
	if err != nil {
		return err
	}
	credential := NewCredentialObject(AdenCredentialID)
	credential.Keys.Set(NewCredentialKey("api_key", key))
	if err := storage.Save(credential); err != nil {
		return err
	}
	return os.Setenv(AdenEnvVar, key)
}

func DeleteAdenAPIKey() bool {
	deleted := false
	if storage, err := NewEncryptedFileStorage("", ""); err == nil {
		if ok, err := storage.Delete(AdenCredentialID); err == nil {
			deleted = ok
		}
	}
	_ = os.Unsetenv(AdenEnvVar)
	return deleted
}

func readFromShellConfig(envVar string) string {
	if os.Getenv("HIVE_DESKTOP_MODE") == "1" {
		return ""
	}
	found, value := CheckEnvVarInShellConfig(envVar)
	if found && value != "" {
		return value
	}
	return ""
}
