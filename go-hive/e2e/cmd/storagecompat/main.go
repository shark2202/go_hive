// Command storagecompat is the Go half of the HIVE_HOME storage-compat /
// G2 lossless-migration harness. It is a test harness (not a shipped binary):
// the Python half lives in go_hive/scripts/storage_compat_probes.py and both
// sides are driven by go_hive/scripts/{storage_compat,g2_home_e2e}_run.sh.
//
// Go migration functions are invoked explicitly here with the same sequence
// production `hive serve` runs at startup (SC-01: v2 -> v3 -> colony_sessions
// -> EnsureAllColonyTrackerDBs; see internal/server/storage_startup.go).
//
// Subcommands:
//
//	migrate --home <dir>     run RunMigrationV2 -> V3 -> ColonySessions +
//	                         EnsureAllColonyTrackerDBs (mirrors the Python
//	                         SessionManager.__init__ storage-prep sequence)
//	write-probes --home <d>  write state.json / conversations / checkpoints /
//	                         credentials / fresh tracker.db through the real
//	                         writer APIs for field-level comparison
//	read-probes --home <d>   report what the Go APIs see in a migrated home:
//	                         session lists/histories, colony discovery, tracker
//	                         query output, sidecars, senders, skills, memories,
//	                         credentials
//	read-credential --dir <d> --key <path> --id <id> [--all]   decrypt one
//	                         credential (or the whole store) with Fernet
//	sidecar-write --home <d> rewrite the SC-06 session meta / tasks / colony
//	                         sidecars through the production writers (title,
//	                         queen phase, task store, colony scaffold, tools
//	                         config, trigger store)
//	sidecar-read --home <d>  read the same sidecars through the production
//	                         readers (used on both sides of the cross-
//	                         implementation write-read-rewrite cycle)
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/agentloop"
	"github.com/shark2202/go_hive/go-hive/internal/agents"
	"github.com/shark2202/go_hive/go-hive/internal/colony"
	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/schemas"
	"github.com/shark2202/go_hive/go-hive/internal/senders"
	"github.com/shark2202/go_hive/go-hive/internal/server"
	"github.com/shark2202/go_hive/go-hive/internal/session"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
	"github.com/shark2202/go_hive/go-hive/internal/tasks"
	"github.com/shark2202/go_hive/go-hive/internal/tracker"
	"github.com/shark2202/go_hive/go-hive/internal/triggers"
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
	all := fs.Bool("all", false, "emit every credential using the read-probe shape")
	_ = fs.Parse(args)

	raw, err := os.ReadFile(*keyPath)
	fail(err)
	store, err := credentials.NewEncryptedFileStorage(*dir, trimSpace(string(raw)))
	fail(err)

	if *all {
		ids, err := store.ListAll()
		fail(err)
		sort.Strings(ids)
		creds := []any{}
		for _, cid := range ids {
			cred, err := store.Load(cid)
			if err != nil || cred == nil {
				continue
			}
			creds = append(creds, credentialProbe(cred))
		}
		out(map[string]any{"dir": *dir, "ids": ids, "credentials": creds})
		return
	}

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
// read probes (G2): what each implementation sees in a migrated home
// ---------------------------------------------------------------------------

func fileSHA16(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

func fileInventory(root, homeRoot string) map[string]any {
	out := map[string]any{}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return out
	}
	_ = filepath.Walk(root, func(path string, entry os.FileInfo, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(homeRoot, path)
		if err != nil {
			return nil
		}
		out[rel] = fileSHA16(path)
		return nil
	})
	return out
}

func readJSONOrNil(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return value
}

func probeBool(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return true
}

func probeString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// nilIfEmpty mirrors Python's “str | None“ provider ids on the wire: the
// credential model tags provider_id with omitempty, so "no provider" reads
// back as absent/null rather than an empty string.
func nilIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func probePartSummary(part map[string]any) map[string]any {
	content := "<non-string content>"
	if text, ok := part["content"].(string); ok {
		content = text
	} else if part["content"] == nil {
		content = ""
	}
	if len(content) > 200 {
		content = content[:200]
	}
	toolCalls := []any{}
	if calls, ok := part["tool_calls"].([]any); ok {
		for _, call := range calls {
			entry, _ := call.(map[string]any)
			if entry == nil {
				continue
			}
			fn, _ := entry["function"].(map[string]any)
			if fn == nil {
				continue
			}
			toolCalls = append(toolCalls, fn["name"])
		}
	}
	var reasoning any
	if text := probeString(part["reasoning_content"]); text != "" {
		if len(text) > 120 {
			text = text[:120]
		}
		reasoning = text
	}
	return map[string]any{
		"seq":               intOrNil(part["seq"]),
		"role":              part["role"],
		"content":           content,
		"tool_use_id":       part["tool_use_id"],
		"tool_calls":        toolCalls,
		"is_skill_content":  probeBool(part["is_skill_content"]),
		"reasoning_content": reasoning,
		"spillover_path":    part["spillover_path"],
	}
}

func intOrNil(value any) any {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return nil
}

func probeSessionDirs(h hivehome.Home) []string {
	seen := map[string]struct{}{}
	var dirs []string
	for _, pattern := range []string{
		"queens/*/sessions/*",
		"colonies/*/queens/*/sessions/*",
		"colonies/*/workers/*/sessions/*",
	} {
		matches, _ := filepath.Glob(filepath.Join(h.Root, pattern))
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || !info.IsDir() {
				continue
			}
			if _, ok := seen[match]; ok {
				continue
			}
			seen[match] = struct{}{}
			dirs = append(dirs, match)
		}
	}
	sort.Strings(dirs)
	return dirs
}

func probeState(sessionDir string) any {
	store := storage.NewSessionStore(filepath.Dir(filepath.Dir(sessionDir)))
	state, err := store.ReadState(filepath.Base(sessionDir))
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if state == nil {
		return nil
	}
	var updated any
	if !state.Timestamps.UpdatedAt.IsZero() {
		updated = state.Timestamps.UpdatedAt.Time.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"status":         string(state.Status),
		"goal_id":        state.GoalID,
		"agent_id":       state.AgentID,
		"updated_at":     updated,
		"steps_executed": state.Progress.StepsExecuted,
	}
}

func readHistories(h hivehome.Home) map[string]any {
	out := map[string]any{}
	for _, dir := range probeSessionDirs(h) {
		rel, err := filepath.Rel(h.Root, dir)
		if err != nil {
			continue
		}
		entry := map[string]any{"state": probeState(dir)}
		summary := readJSONOrNil(filepath.Join(dir, "summary.json"))
		entry["summary"] = summary
		entry["tasks"] = readJSONOrNil(filepath.Join(dir, "tasks.json"))
		parts := []any{}
		convs := filepath.Join(dir, "conversations")
		if info, err := os.Stat(convs); err == nil && info.IsDir() {
			store := agentloop.NewFileConversationStore(convs)
			if rows, err := store.ReadParts(); err != nil {
				parts = append(parts, map[string]any{"error": err.Error()})
			} else {
				for _, row := range rows {
					parts = append(parts, probePartSummary(row))
				}
			}
		}
		entry["parts"] = parts
		out[rel] = entry
	}
	return out
}

func readDiscover(h hivehome.Home) []any {
	out := []any{}
	groups := agents.DiscoverAgents(h.ColoniesDir())
	categories := make([]string, 0, len(groups))
	for category := range groups {
		categories = append(categories, category)
	}
	sort.Strings(categories)
	for _, category := range categories {
		for _, entry := range groups[category] {
			rel := entry.Path
			roots := []string{h.Root}
			if resolved, err := filepath.EvalSymlinks(h.Root); err == nil {
				roots = append(roots, resolved)
			}
			for _, root := range roots {
				if r, err := filepath.Rel(root, entry.Path); err == nil && !strings.HasPrefix(r, "..") {
					rel = r
					break
				}
			}
			workers := []any{}
			for _, worker := range entry.Workers {
				workers = append(workers, map[string]any{
					"name":        worker.Name,
					"description": worker.Description,
					"tool_count":  worker.ToolCount,
					"task":        worker.Task,
				})
			}
			var lastActive any
			if entry.LastActive != nil {
				lastActive = *entry.LastActive
			}
			out = append(out, map[string]any{
				"category":      category,
				"name":          entry.Name,
				"path":          rel,
				"description":   entry.Description,
				"session_count": entry.SessionCount,
				"run_count":     entry.RunCount,
				"node_count":    entry.NodeCount,
				"tool_count":    entry.ToolCount,
				"last_active":   lastActive,
				"created_at":    entry.CreatedAt,
				"icon":          entry.Icon,
				"workers":       workers,
			})
		}
	}
	return out
}

func probeTrackerQuery(dbPath, sqlText string) map[string]any {
	result, err := tracker.ExecuteSQL(dbPath, sqlText, 1000)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	return result
}

func readTrackerProbe(h hivehome.Home, colonyDir string) any {
	dbPath := filepath.Join(colonyDir, "tracker", "tracker.db")
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	rel, err := filepath.Rel(h.Root, dbPath)
	if err != nil {
		rel = dbPath
	}
	probe := map[string]any{"db": rel}
	probe["tables"] = probeTrackerQuery(dbPath, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	probe["registry"] = probeTrackerQuery(dbPath, "SELECT table_name, write_columns, key_columns, mode FROM _tracker_registry ORDER BY table_name")
	probe["meta"] = probeTrackerQuery(dbPath, "SELECT key, value FROM _tracker_meta ORDER BY key")
	probe["changes"] = probeTrackerQuery(dbPath, "SELECT table_name, pk, op FROM _tracker_changes ORDER BY id")

	names := []string{}
	if tables, ok := probe["tables"].(map[string]any); ok && tables["kind"] == "rows" {
		if rows, ok := tables["rows"].([]any); ok {
			for _, row := range rows {
				values, _ := row.([]any)
				if len(values) == 1 {
					if name, ok := values[0].(string); ok {
						names = append(names, name)
					}
				}
			}
		}
	}
	userRows := map[string]any{}
	for _, name := range names {
		if strings.HasPrefix(name, "_") {
			continue
		}
		userRows[name] = probeTrackerQuery(dbPath, fmt.Sprintf("SELECT * FROM %q ORDER BY rowid", name))
	}
	probe["user_rows"] = userRows
	hasLead, hasInvoice := false, false
	for _, name := range names {
		if name == "leads" {
			hasLead = true
		}
		if name == "invoices" {
			hasInvoice = true
		}
	}
	if hasLead && hasInvoice {
		probe["join"] = probeTrackerQuery(dbPath, "SELECT l.id, l.name, COUNT(i.id) AS invoice_count FROM leads l LEFT JOIN invoices i ON i.lead_id = l.id GROUP BY l.id, l.name ORDER BY l.id")
	} else {
		probe["join"] = nil
	}
	return probe
}

func readColoniesProbe(h hivehome.Home) map[string]any {
	out := map[string]any{}
	entries, err := os.ReadDir(h.ColoniesDir())
	if err != nil {
		return out
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		colonyDir := filepath.Join(h.ColoniesDir(), name)
		item := map[string]any{"colony": name}
		item["metadata"] = readJSONOrNil(filepath.Join(colonyDir, "metadata.json"))

		workers := map[string]any{}
		configs, err := os.ReadDir(colonyDir)
		if err == nil {
			for _, cfg := range configs {
				if cfg.IsDir() || !strings.HasSuffix(cfg.Name(), ".json") {
					continue
				}
				data, ok := readJSONOrNil(filepath.Join(colonyDir, cfg.Name())).(map[string]any)
				if !ok {
					continue
				}
				if _, has := data["system_prompt"]; !has {
					continue
				}
				workers[cfg.Name()] = map[string]any{
					"name":       data["name"],
					"input_data": data["input_data"],
					"tools":      data["tools"],
				}
			}
		}
		item["workers"] = workers
		item["triggers"] = readJSONOrNil(filepath.Join(colonyDir, "triggers.json"))

		skills := []any{}
		skillsDir := filepath.Join(colonyDir, "skills")
		if info, err := os.Stat(skillsDir); err == nil && info.IsDir() {
			_ = filepath.Walk(skillsDir, func(path string, fi os.FileInfo, walkErr error) error {
				if walkErr != nil || fi.IsDir() || filepath.Base(path) != "SKILL.md" {
					return nil
				}
				if rel, err := filepath.Rel(h.Root, path); err == nil {
					skills = append(skills, rel)
				}
				return nil
			})
			sort.Slice(skills, func(i, j int) bool { return skills[i].(string) < skills[j].(string) })
		}
		item["skills"] = skills

		var runs any
		dbPath := filepath.Join(colonyDir, "colony.db")
		if _, err := os.Stat(dbPath); err == nil {
			rows, err := colony.ListPlaybookRuns(dbPath, 1000)
			if err == nil {
				list := []any{}
				for _, run := range rows {
					list = append(list, map[string]any{"run_id": run.RunID, "name": run.Name, "status": run.Status, "error": run.Error})
				}
				sort.Slice(list, func(i, j int) bool {
					return list[i].(map[string]any)["run_id"].(string) < list[j].(map[string]any)["run_id"].(string)
				})
				runs = list
			} else {
				runs = map[string]any{"error": err.Error()}
			}
		}
		item["playbook_runs"] = runs
		item["tracker"] = readTrackerProbe(h, colonyDir)
		out[name] = item
	}
	return out
}

func readSendersProbe(h hivehome.Home) map[string]any {
	path := filepath.Join(h.Root, "senders", "senders.json")
	if _, err := os.Stat(path); err != nil {
		return map[string]any{"present": false, "senders": []any{}}
	}
	var raw struct {
		Version int              `json:"version"`
		Senders []senders.Sender `json:"senders"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &raw) != nil {
		return map[string]any{"present": true, "error": "unreadable"}
	}
	views := []any{}
	for _, row := range raw.Senders {
		views = append(views, row.PublicView())
	}
	return map[string]any{"present": true, "version": raw.Version, "senders": views}
}

func credentialProbe(cred *credentials.CredentialObject) map[string]any {
	keys := map[string]any{}
	for _, keyName := range cred.Keys.Names() {
		key := cred.Keys.Get(keyName)
		var expires any
		if key.ExpiresAt != nil {
			expires = key.ExpiresAt.UTC().Format(time.RFC3339Nano)
		}
		keys[keyName] = map[string]any{
			"value":      key.GetSecretValue(),
			"expires_at": expires,
			"metadata":   key.Metadata,
		}
	}
	return map[string]any{
		"id":              cred.ID,
		"credential_type": string(cred.CredentialType),
		"provider_id":     nilIfEmpty(cred.ProviderID),
		"auto_refresh":    cred.AutoRefresh,
		"description":     cred.Description,
		"tags":            cred.Tags,
		"keys":            keys,
	}
}

func readCredentialsProbe(h hivehome.Home) []any {
	keyPath := filepath.Join(h.SecretsDir(), "credential_key")
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return []any{}
	}
	store, err := credentials.NewEncryptedFileStorage(h.CredentialsDir(), trimSpace(string(raw)))
	if err != nil {
		return []any{map[string]any{"error": err.Error()}}
	}
	ids, err := store.ListAll()
	if err != nil {
		return []any{map[string]any{"error": err.Error()}}
	}
	sort.Strings(ids)
	out := []any{}
	for _, id := range ids {
		cred, err := store.Load(id)
		if err != nil || cred == nil {
			continue
		}
		out = append(out, credentialProbe(cred))
	}
	return out
}

func cmdReadProbes(args []string) {
	fs := flag.NewFlagSet("read-probes", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root")
	_ = fs.Parse(args)
	h := mustHome(*home)

	report := map[string]any{"home": h.Root}
	report["cold_sessions"] = session.ListColdSessions(h.QueensDir())

	colonySessions := map[string]any{}
	if entries, err := os.ReadDir(h.ColoniesDir()); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				colonySessions[entry.Name()] = session.ListColonySessions(filepath.Join(h.ColoniesDir(), entry.Name(), "queens"))
			}
		}
	}
	report["colony_sessions"] = colonySessions

	report["histories"] = readHistories(h)
	report["discover"] = readDiscover(h)
	report["colonies"] = readColoniesProbe(h)
	report["senders"] = readSendersProbe(h)
	report["skills_user"] = fileInventory(filepath.Join(h.Root, "skills"), h.Root)

	queenSkills := map[string]any{}
	if entries, err := os.ReadDir(h.QueensDir()); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join(h.QueensDir(), entry.Name(), "skills")
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				queenSkills[entry.Name()] = fileInventory(dir, h.Root)
			}
		}
	}
	report["skills_queens"] = queenSkills
	report["memories"] = fileInventory(h.MemoriesDir(), h.Root)
	report["credentials"] = readCredentialsProbe(h)
	out(report)
}

// ---------------------------------------------------------------------------
// sidecar cycle (SC-06): session meta / tasks / colony sidecars
// ---------------------------------------------------------------------------

const (
	cycleQueen    = "queen_growth"
	cycleSession  = "session_sidecar_cycle"
	cycleColony   = "cycle_colony"
	cycleGoColony = "cycle_colony_go"
)

func cycleSessionDir(h hivehome.Home) string {
	return filepath.Join(h.QueensDir(), cycleQueen, "sessions", cycleSession)
}

func cycleColonyDir(h hivehome.Home, colony string) string {
	return filepath.Join(h.ColoniesDir(), colony)
}

// readJSONMap is the same read the production meta/sidecar readers use
// (session_titles.go / queen_phase_state.go / adapters_qmail.go): a tolerant
// JSON-object read that returns nil for missing or malformed files.
func readJSONMap(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	value := map[string]any{}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return value
}

func cycleMetaView(sessionDir string) map[string]any {
	var title any
	if current, ok := server.ReadTitle(sessionDir); ok {
		title = current
	}
	phase := server.ReadSessionPhase(sessionDir)
	var phaseView any
	if phase != "" {
		phaseView = phase
	}
	return map[string]any{
		"title":  title,
		"phase":  phaseView,
		"fields": readJSONMap(filepath.Join(sessionDir, "meta.json")),
	}
}

func cycleTasksView(h hivehome.Home) map[string]any {
	store := tasks.NewTaskStore(h.Root)
	view := map[string]any{"meta": store.GetMeta(cycleSession)}
	records := []any{}
	for _, record := range store.ListTasks(cycleSession, true) {
		records = append(records, record)
	}
	view["tasks"] = records
	return view
}

func cycleColonyView(h hivehome.Home, colony string) map[string]any {
	dir := cycleColonyDir(h, colony)
	enabled := server.LoadColonyToolsConfig(h, colony)
	var enabledView any
	if enabled != nil {
		enabledView = enabled
	}
	return map[string]any{
		"metadata":      readJSONMap(filepath.Join(dir, "metadata.json")),
		"worker":        readJSONMap(filepath.Join(dir, "worker.json")),
		"tools":         readJSONMap(filepath.Join(dir, "tools.json")),
		"tools_enabled": enabledView,
		"triggers":      triggers.ReadAgentTriggersJSON(dir),
	}
}

func cycleView(h hivehome.Home) map[string]any {
	return map[string]any{
		"meta":  cycleMetaView(cycleSessionDir(h)),
		"tasks": cycleTasksView(h),
		"colonies": map[string]any{
			cycleColony:   cycleColonyView(h, cycleColony),
			cycleGoColony: cycleColonyView(h, cycleGoColony),
		},
	}
}

func cycleFileHashes(h hivehome.Home) map[string]any {
	paths := map[string]string{
		"session/meta.json":  filepath.Join(cycleSessionDir(h), "meta.json"),
		"session/tasks.json": filepath.Join(cycleSessionDir(h), "tasks.json"),
	}
	for _, colony := range []string{cycleColony, cycleGoColony} {
		for _, name := range []string{"metadata.json", "worker.json", "tools.json", "triggers.json"} {
			paths["colonies/"+colony+"/"+name] = filepath.Join(cycleColonyDir(h, colony), name)
		}
	}
	out := map[string]any{}
	for key, path := range paths {
		if _, err := os.Stat(path); err != nil {
			out[key] = nil
			continue
		}
		out[key] = fileSHA16(path)
	}
	return out
}

func cycleTrigger(id, name, task string) map[string]any {
	return map[string]any{
		"id":             id,
		"name":           name,
		"trigger_type":   "interval",
		"trigger_config": map[string]any{"minutes": 60},
		"task":           task,
		"enabled":        true,
		"last_fired_at":  nil,
		"next_due_at":    nil,
	}
}

// seedSidecars writes the same base artifacts the Python sidecar-write probe
// seeds, through the Go production writers. It backs the SC-04 permission
// comparison (one home written by each implementation) and doubles as the
// "Go writes a session from scratch" leg of the SC-06 matrix.
func seedSidecars(h hivehome.Home, sessionDir string) {
	fail(server.MergeSessionMeta(filepath.Join(sessionDir, "meta.json"), map[string]any{
		"queen_id":   cycleQueen,
		"created_at": 1767225600.0,
		"agent_name": "Growth",
		"colony_id":  cycleColony,
		"phase":      server.QueenPhaseIndependent,
	}))
	if _, err := server.WriteTitle(sessionDir, "Sidecar cycle (go seed)", "user"); err != nil {
		fail(err)
	}

	creator := cycleQueen
	store := tasks.NewTaskStore(h.Root)
	if _, err := store.EnsureTaskList(cycleSession, &creator); err != nil {
		fail(err)
	}
	draftActive := "drafting plan"
	if _, err := store.CreateTask(cycleSession, tasks.CreateTaskSpec{
		Subject:     "draft plan",
		Description: "write the plan",
		ActiveForm:  &draftActive,
		Metadata:    map[string]any{"phase": "draft"},
	}); err != nil {
		fail(err)
	}
	owner := cycleQueen
	if _, err := store.CreateTask(cycleSession, tasks.CreateTaskSpec{
		Subject: "review plan",
		Owner:   &owner,
	}); err != nil {
		fail(err)
	}

	fail(server.EnsureMinimalColony(h, cycleColony, cycleQueen))
	fail(server.UpdateColonyToolsConfig(h, cycleColony, []string{"pdf_read"}))
	fail(triggers.WriteAgentTriggersJSON(cycleColonyDir(h, cycleColony), []map[string]any{
		cycleTrigger("trg_go", "go probe", "go cycle task"),
	}))
}

func cmdSidecarWrite(args []string) {
	fs := flag.NewFlagSet("sidecar-write", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root")
	mode := fs.String("mode", "rewrite", "seed (write the base artifacts) or rewrite (cross-implementation rewrite)")
	_ = fs.Parse(args)
	h := mustHome(*home)

	sessionDir := cycleSessionDir(h)
	fail(os.MkdirAll(sessionDir, 0o755))
	if *mode == "seed" {
		seedSidecars(h, sessionDir)
		out(map[string]any{"phase": "go-seed", "view": cycleView(h), "files": cycleFileHashes(h)})
		return
	}

	// meta.json: production queen-phase writer (queen_phase_state.go) plus the
	// production title writer (session_titles.go).
	phaseState := server.NewQueenPhaseState(server.QueenPhaseColony, cycleQueen, nil, filepath.Join(sessionDir, "meta.json"), nil)
	fail(phaseState.PersistPhase())
	if _, err := server.WriteTitle(sessionDir, "Sidecar cycle (go rewritten)", "auto"); err != nil {
		fail(err)
	}

	// tasks.json: create a third task and complete the second one through the
	// same store the task tools use.
	creator := cycleQueen
	store := tasks.NewTaskStore(h.Root)
	if _, err := store.EnsureTaskList(cycleSession, &creator); err != nil {
		fail(err)
	}
	if _, err := store.CreateTask(cycleSession, tasks.CreateTaskSpec{
		Subject:     "ship plan",
		Description: "hand the plan over",
		Metadata:    map[string]any{"phase": "ship"},
	}); err != nil {
		fail(err)
	}
	completed := tasks.TaskStatusCompleted
	if _, changed := store.UpdateTask(cycleSession, 2, tasks.UpdateTaskOptions{Status: &completed}); len(changed) == 0 {
		fail(errors.New("sidecar-write: update task 2 changed nothing"))
	}

	// Colony sidecars: scaffold a Go-written colony, then rewrite the
	// Python-written colony's tools.json and append a trigger to its
	// triggers.json through the production writers.
	fail(server.EnsureMinimalColony(h, cycleGoColony, cycleQueen))
	fail(server.UpdateColonyToolsConfig(h, cycleColony, []string{"pdf_read", "excel_read"}))
	fail(server.UpdateColonyToolsConfig(h, cycleGoColony, []string{"pdf_read", "excel_read"}))

	pyColonyTriggers := triggers.ReadAgentTriggersJSON(cycleColonyDir(h, cycleColony))
	pyColonyTriggers = append(pyColonyTriggers, cycleTrigger("trg_go", "go probe", "go cycle task"))
	fail(triggers.WriteAgentTriggersJSON(cycleColonyDir(h, cycleColony), pyColonyTriggers))
	fail(triggers.WriteAgentTriggersJSON(cycleColonyDir(h, cycleGoColony), []map[string]any{
		cycleTrigger("trg_go", "go probe", "go cycle task"),
	}))

	out(map[string]any{"phase": "go-write", "view": cycleView(h), "files": cycleFileHashes(h)})
}

func cmdSidecarRead(args []string) {
	fs := flag.NewFlagSet("sidecar-read", flag.ExitOnError)
	home := fs.String("home", "", "HIVE_HOME root")
	phase := fs.String("phase", "read", "label recorded in the probe output")
	_ = fs.Parse(args)
	h := mustHome(*home)
	out(map[string]any{"phase": *phase, "view": cycleView(h), "files": cycleFileHashes(h)})
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
	case "read-probes":
		cmdReadProbes(os.Args[2:])
	case "sidecar-write":
		cmdSidecarWrite(os.Args[2:])
	case "sidecar-read":
		cmdSidecarRead(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown subcommand:", os.Args[1])
		os.Exit(2)
	}
}
