// Command llm-credential-smoke drives the M3 `hive` credential surface with the
// REAL go_hive clients:
//   - llm.ValidateProviderKey("hive", …) is the production keycheck path
//     (POST /v1/messages with Authorization: Bearer, 400 = key valid);
//   - credentials.CredentialStore stores/reads the runtime's `hive` credential
//     shape ({id:"hive", keys:{api_key: <streamToken>}}).
//
// It never contacts an external cloud: M3_LLM_BASE is the local server.
// Module note: nested under github.com/shark2202/go_hive/go-hive/ so the Go
// internal-package rule allows importing internal/llm and internal/credentials.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/llm"
)

type check struct {
	Name   string         `json:"name"`
	OK     bool           `json:"ok"`
	Detail map[string]any `json:"detail,omitempty"`
	Error  string         `json:"error,omitempty"`
}

type evidence struct {
	Harness    string         `json:"harness"`
	BaseURL    string         `json:"base_url"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	OK         bool           `json:"ok"`
	Checks     []check        `json:"checks"`
	Env        map[string]any `json:"env,omitempty"`
}

func main() {
	base := strings.TrimRight(os.Getenv("M3_LLM_BASE"), "/")
	if base == "" {
		fmt.Fprintln(os.Stderr, "llm-credential-smoke: M3_LLM_BASE is required")
		os.Exit(2)
	}
	tokens := map[string]string{
		"old":             os.Getenv("M3_OLD_TOKEN"),
		"current":         os.Getenv("M3_CURRENT_TOKEN"),
		"expired":         os.Getenv("M3_EXPIRED_TOKEN"),
		"wrong_signature": os.Getenv("M3_WRONG_TOKEN"),
		"user_jwt":        os.Getenv("M3_USER_JWT"),
		"garbage":         "garbage",
	}
	started := time.Now().UTC()
	out := evidence{Harness: "llm-credential-smoke (real go_hive keycheck + credential store)", BaseURL: base, StartedAt: started.Format(time.RFC3339Nano)}

	add := func(name string, ok bool, detail map[string]any, err error) {
		item := check{Name: name, OK: ok, Detail: detail}
		if err != nil {
			item.Error = err.Error()
		}
		out.Checks = append(out.Checks, item)
	}

	validToken := func(name, token string, wantValid bool) {
		result := llm.ValidateProviderKey("hive", token, &base, nil)
		valid, _ := result["valid"].(bool)
		message, _ := result["message"].(string)
		detail := map[string]any{"want_valid": wantValid, "valid": valid, "message": message}
		add(name, valid == wantValid, detail, nil)
	}

	validToken("llm_valid_stream_token_keycheck", tokens["old"], true)
	validToken("llm_current_stream_token_keycheck", tokens["current"], true)
	validToken("llm_valid_operator_jwt_keycheck", tokens["user_jwt"], true)
	validToken("llm_expired_token_keycheck", tokens["expired"], false)
	validToken("llm_wrong_signature_keycheck", tokens["wrong_signature"], false)
	validToken("llm_garbage_keycheck", tokens["garbage"], false)

	storage := credentials.NewInMemoryStorage(nil)
	store := credentials.NewCredentialStore(credentials.WithStorage(storage))
	credential := credentials.NewCredentialObject("hive")
	credential.SetKey("api_key", tokens["current"], nil, nil)
	saveErr := store.SaveCredential(credential)
	got, getErr := store.GetKey("hive", "api_key")
	valid, validateErr := store.ValidateCredential("hive")
	storeOK := saveErr == nil && getErr == nil && validateErr == nil && valid && got != nil && *got == tokens["current"]
	add(
		"hive_credential_store_roundtrip",
		storeOK,
		map[string]any{
			"credential_id": "hive",
			"key":           "api_key",
			"roundtripped":  got != nil && *got == tokens["current"],
			"valid":         valid,
		},
		firstError(saveErr, getErr, validateErr),
	)

	out.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	out.OK = true
	for _, item := range out.Checks {
		if !item.OK {
			out.OK = false
		}
	}
	out.Env = map[string]any{"keycheck_timeout_seconds": llm.KeyCheckTimeout}
	if path := os.Getenv("EVIDENCE_OUT"); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "llm-credential-smoke: %v\n", err)
			os.Exit(1)
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "llm-credential-smoke: %v\n", err)
			os.Exit(1)
		}
	}
	summary, _ := json.Marshal(map[string]any{"ok": out.OK, "checks": len(out.Checks)})
	fmt.Println(string(summary))
	if !out.OK {
		os.Exit(1)
	}
}

func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
