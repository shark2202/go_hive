// Command storagecompat is the Go half of the HIVE_HOME storage-compat
// double run. It is a test harness (not a shipped binary): the Python half
// lives in go_hive/scripts/storage_compat_probes.py and both sides are
// driven by go_hive/scripts/storage_compat_run.sh.
//
// Go migration functions are invoked explicitly here: production `hive serve`
// does not call them today (see docs/storage-contract-diff.md), so an
// old-layout HIVE_HOME is only migrated when something explicitly runs them.
//
// Subcommands:
//
//	migrate --home <dir>     run RunMigrationV2 -> V3 -> ColonySessions +
//	                         EnsureAllColonyTrackerDBs (mirrors the Python
//	                         SessionManager.__init__ storage-prep sequence)
//	write-probes --home <d>  write state.json / conversations / checkpoints /
//	                         credentials / fresh tracker.db through the real
//	                         writer APIs for field-level comparison
//	read-credential --dir <d> --key <path> --id <id>   decrypt one credential
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/credentials"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/schemas"
	"github.com/aden-hive/hive-go/internal/storage"
	"github.com/aden-hive/hive-go/internal/tracker"
)

func out(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "storagecompat:", err)
		os.Exit(1)
	}
}

func mustHome(path string) hivehome.Home {
	h, err := hivehome.ResolveAt(path)
	fail(err)
	return h
}

// ---------------------------------------------------------------------------
// migrate
// ---------------------------------------------------------------------------

func cmdMigrate(args []string) {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root")
	_ = fs.Parse(args)
	root := *home
	h := mustHome(root)

	report := map[string]any{
		"home":                  h.Root,
		"needs_v2":              storage.NeedsMigrationV2(h.Root),
		"needs_v3":              storage.NeedsMigrationV3(h.Root),
		"needs_colony_sessions": storage.NeedsMigrationColonySessions(h.Root),
	}
	fail(storage.RunMigrationV2(h.Root, filepath.Join(h.Root, "exports")))
	fail(storage.RunMigrationV3(h.Root))
	fail(storage.RunMigrationColonySessions(h.Root))
	ensured, err := tracker.EnsureAllColonyTrackerDBs(h.ColoniesDir())
	fail(err)
	rel := make([]string, 0, len(ensured))
	for _, p := range ensured {
		if r, err := filepath.Rel(h.Root, p); err == nil {
			rel = append(rel, r)
		}
	}
	report["tracker_dbs_ensured"] = rel
	report["post_needs_v2"] = storage.NeedsMigrationV2(h.Root)
	report["post_needs_v3"] = storage.NeedsMigrationV3(h.Root)
	report["post_needs_colony_sessions"] = storage.NeedsMigrationColonySessions(h.Root)
	out(report)
}

// ---------------------------------------------------------------------------
// write probes
// ---------------------------------------------------------------------------

func writeStateProbe(h hivehome.Home, sessionDir string) {
	store := storage.NewSessionStore(filepath.Join(h.QueensDir(), "queen_growth"))
	state := schemas.NewSessionState("session_compat_probe_go", "goal_compat", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	state.Status = schemas.SessionPaused
	state.AgentID = "queen_growth"
	state.Timestamps.UpdatedAt = schemas.NewTimestamp(time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC))
	state.Progress.CurrentNode = strPtr("start")
	state.Progress.StepsExecuted = 1
	state.Progress.Path = []string{"start"}
	state.ActiveTriggers = []string{"trg-1"}
	state.TriggerTasks = map[string]string{"trg-1": "check"}
	state.WorkerConfigured = true
	fail(store.WriteState("session_compat_probe_go", state))
}

func writeConversationProbe(sessionDir string) {
	store := agentloop.NewFileConversationStore(filepath.Join(sessionDir, "conversations"))
	outputKeys := []string{"answer"}
	bufTokens := 4096
	bufRatio := 0.05
	warnTokens := 2048
	conv := agentloop.NewNodeConversationWithOptions(agentloop.ConversationOptions{
		SystemPrompt:                  "You are Hive.",
		Store:                         store,
		OutputKeys:                    outputKeys,
		CompactionBufferTokens:        &bufTokens,
		CompactionBufferRatio:         &bufRatio,
		CompactionWarningBufferTokens: &warnTokens,
	})
	_, err := conv.AddUserMessage("hello", agentloop.UserMessageOptions{IsClientInput: true})
	fail(err)
	reasoning := "checking the file"
	_, err = conv.AddAssistantMessage("let me look",
		[]map[string]any{{"id": "call_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": "{}"}}},
		agentloop.AssistantMessageOptions{ReasoningContent: &reasoning})
	fail(err)
	spill := filepath.Join(sessionDir, "artifacts", "spill.txt")
	_, err = conv.AddToolResult("call_1", "file contents", agentloop.ToolResultOptions{IsSkillContent: true, SpilloverPath: &spill})
	fail(err)
}

func writeCheckpointProbe(sessionDir string) {
	store := storage.NewCheckpointStore(sessionDir)
	cp := &schemas.Checkpoint{
		CheckpointID:       "cp_node_complete_start_20260101T000000",
		CheckpointType:     "node_complete",
		SessionID:          filepath.Base(sessionDir),
		RunID:              strPtr("run-compat"),
		CreatedAt:          "2026-01-01T00:00:00Z",
		CurrentNode:        strPtr("start"),
		NextNode:           strPtr("finish"),
		ExecutionPath:      []string{"start"},
		DataBuffer:         map[string]any{"k": "v"},
		AccumulatedOutputs: map[string]any{"answer": "42"},
		MetricsSnapshot:    map[string]any{"total_tokens": 7},
		IsClean:            true,
		Description:        "compat probe",
	}
	fail(store.SaveCheckpoint(cp))
}

func writeCredentialProbes(h hivehome.Home) map[string]any {
	keyPath := filepath.Join(h.SecretsDir(), "credential_key")
	fail(os.MkdirAll(h.SecretsDir(), 0o700))
	key := ""
	if raw, err := os.ReadFile(keyPath); err == nil {
		key = string(trimSpace(string(raw)))
	}
	if key == "" {
		generated, err := credentials.GenerateFernetKey()
		fail(err)
		key = generated
		fail(os.WriteFile(keyPath, []byte(key), 0o600))
	}
	store, err := credentials.NewEncryptedFileStorage(h.CredentialsDir(), key)
	fail(err)

	api := credentials.NewCredentialObject("demo_api_key")
	api.Description = "demo api key"
	api.Tags = []string{"demo", "compat"}
	api.SetKey("api_key", "sk-demo-123", nil, map[string]any{"scope": "read"})
	fail(store.Save(api))

	oauth := credentials.NewCredentialObject("demo_oauth")
	oauth.CredentialType = credentials.CredentialTypeOAuth2
	expires := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	oauth.SetKey("access_token", "at-demo", &expires, nil)
	oauth.SetKey("refresh_token", "rt-demo", nil, nil)
	oauth.SetKey("_alias", "work", nil, nil)
	oauth.SetKey("_integration_type", "google", nil, nil)
	oauth.SetKey("_identity_email", "demo@example.com", nil, nil)
	oauth.ProviderID = "oauth2"
	oauth.AutoRefresh = true
	oauth.Description = "demo oauth"
	fail(store.Save(oauth))

	return map[string]any{"saved": []string{"demo_api_key", "demo_oauth"}, "key_path": keyPath}
}

func cmdProbes(args []string) {
	fs := flag.NewFlagSet("write-probes", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root")
	_ = fs.Parse(args)
	h := mustHome(*home)

	sessionDir := filepath.Join(h.QueensDir(), "queen_growth", "sessions", "session_compat_probe_go")
	fail(os.MkdirAll(sessionDir, 0o755))
	writeStateProbe(h, sessionDir)
	writeConversationProbe(sessionDir)
	writeCheckpointProbe(sessionDir)

	// Fresh colony + tracker ensure (same path as the Python half).
	colony := filepath.Join(h.ColoniesDir(), "probe_colony")
	fail(os.MkdirAll(colony, 0o755))
	metaJSON := `{"name": "probe_colony", "created_at": 1767225600.0}`
	fail(os.WriteFile(filepath.Join(colony, "metadata.json"), []byte(metaJSON), 0o644))
	_, err := tracker.EnsureTrackerDB(colony)
	fail(err)

	creds := writeCredentialProbes(h)
	out(map[string]any{"probes": "written", "session_dir": sessionDir, "credentials": creds})
}

// ---------------------------------------------------------------------------
// credential cross-read
// ---------------------------------------------------------------------------

func cmdReadCredential(args []string) {
	fs := flag.NewFlagSet("read-credential", flag.ExitOnError)
	dir := fs.String("dir", "", "credentials base dir")
	keyPath := fs.String("key", "", "path to the fernet key file")
	id := fs.String("id", "", "credential id (empty = list all)")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(*keyPath)
	fail(err)
	store, err := credentials.NewEncryptedFileStorage(*dir, trimSpace(string(raw)))
	fail(err)

	ids, err := store.ListAll()
	fail(err)
	if *id == "" {
		out(map[string]any{"ids": ids})
		return
	}
	cred, err := store.Load(*id)
	if err != nil {
		out(map[string]any{"id": *id, "error": err.Error()})
		return
	}
	if cred == nil {
		out(map[string]any{"id": *id, "missing": true})
		return
	}
	keys := map[string]any{}
	for _, name := range cred.Keys.Names() {
		k := cred.Keys.Get(name)
		var exp any
		if k.ExpiresAt != nil {
			exp = k.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		keys[name] = map[string]any{"value": k.GetSecretValue(), "expires_at": exp, "metadata": k.Metadata}
	}
	var created, updated any
	if cred.CreatedAt != nil {
		created = cred.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if cred.UpdatedAt != nil {
		updated = cred.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	out(map[string]any{
		"id":              cred.ID,
		"credential_type": string(cred.CredentialType),
		"provider_id":     cred.ProviderID,
		"auto_refresh":    cred.AutoRefresh,
		"description":     cred.Description,
		"tags":            cred.Tags,
		"created_at":      created,
		"updated_at":      updated,
		"use_count":       cred.UseCount,
		"keys":            keys,
	})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: storagecompat <migrate|write-probes|read-credential> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "migrate":
		cmdMigrate(os.Args[2:])
	case "write-probes":
		cmdProbes(os.Args[2:])
	case "read-credential":
		cmdReadCredential(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		os.Exit(2)
	}
}
