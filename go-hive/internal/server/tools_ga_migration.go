package server

// One-shot migration that appends newly-GA tools to existing agent sidecars.
//
// Parity: framework/agents/queen/tools_ga_migration.py::run_ga_tool_migration
// Parity: framework/agents/queen/tools_ga_migration.py::migrate_queen_sidecars
// Parity: framework/agents/queen/tools_ga_migration.py::migrate_colony_sidecars
// Parity: framework/agents/queen/tools_ga_migration.py::_apply_to_sidecar
//
// Runs at runtime startup (internal/cli/loader.go, mirroring create_app). For
// each queen/colony tools.json sidecar, grants every tool listed in
// tools.CategoryAdditions whose category sits in the queen's role default AND
// whose GA-promotion version is newer than the sidecar's saved_on_version.
// Sidecars that gain tools are rewritten atomically with saved_on_version
// advanced to the current app version; unchanged sidecars are left alone so
// re-running is a no-op until a new entry lands in CategoryAdditions.
//
// Idempotency is per-sidecar via its own saved_on_version — no global version
// stamp is needed. Custom queen IDs (not in the role-default table) are
// skipped to avoid over-granting; the load-time fallback
// (tools.InferCategoryAdditions) still heals those in memory.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

// gaSidecarTimestamp renders updated_at the way Python's datetime.isoformat
// does on a UTC timestamp (mirrors writeQueenToolsConfig's format).
func gaSidecarTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
}

// gaAddedTools mirrors “sorted(set(granted) - set(raw))“: the tools the
// grant added on top of the saved list, deduplicated and sorted.
func gaAddedTools(granted, previous []string) []string {
	have := make(map[string]struct{}, len(previous))
	for _, name := range previous {
		have[name] = struct{}{}
	}
	added := make([]string, 0)
	seen := make(map[string]struct{})
	for _, name := range granted {
		if _, already := have[name]; already {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		added = append(added, name)
	}
	sort.Strings(added)
	return added
}

// gaApplyToSidecar parses one sidecar and applies role-default grants.
//
// Mirror of tools_ga_migration._apply_to_sidecar. An empty result means no
// change (nothing to grant, or the sidecar was empty/null/malformed); writes
// happen only when the saved list actually grew. Read/JSON errors collapse to
// the upstream "unreadable sidecar" warning; a write error is returned to the
// caller, which logs it per-sidecar so a single file cannot block boot.
func gaApplyToSidecar(path, queenID string, warn func(string)) ([]string, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		gaWarn(warn, "ga_migration: unreadable sidecar at "+path)
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		gaWarn(warn, "ga_migration: unreadable sidecar at "+path)
		return nil, nil
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return nil, nil
	}
	raw, present := object["enabled_mcp_tools"]
	// null (default-allow) and [] (explicit disable-all) carry no list to
	// grow — only an explicit allowlist is migrated.
	if !present || raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, nil
	}
	saved := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, nil
		}
		saved = append(saved, text)
	}

	savedOnVersion, _ := object["saved_on_version"].(string)
	granted := tools.GrantRoleDefaultAdditions(queen.DefaultToolsFor(queenID), saved, savedOnVersion)
	added := gaAddedTools(granted, saved)
	if len(added) == 0 {
		return nil, nil
	}
	payload := map[string]any{
		"enabled_mcp_tools": granted,
		"updated_at":        gaSidecarTimestamp(),
		"saved_on_version":  appVersionFromEnv(),
	}
	if err := writeJSONObjectAtomic(path, payload, ".tools."); err != nil {
		return nil, err
	}
	return added, nil
}

func gaWarn(warn func(string), message string) {
	if warn != nil {
		warn(message)
	}
}

// MigrateQueenSidecars mirrors migrate_queen_sidecars: apply GA grants to every
// queen's tools.json under the home's queens dir. Returns {queen_id: [newly
// granted tools]} for queens that changed. Queens without a role default
// (custom IDs) are skipped — the grant helper returns the list unchanged.
func MigrateQueenSidecars(home hivehome.Home, warn func(string)) map[string][]string {
	results := map[string][]string{}
	entries, err := os.ReadDir(home.QueensDir())
	if err != nil {
		return results
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, queenID := range names {
		if len(queen.DefaultToolsFor(queenID)) == 0 {
			continue
		}
		sidecar := filepath.Join(home.QueensDir(), queenID, "tools.json")
		added, applyErr := gaApplyToSidecar(sidecar, queenID, warn)
		if applyErr != nil {
			gaWarn(warn, "ga_migration: failed for queen "+queenID+" ("+applyErr.Error()+")")
			continue
		}
		if len(added) > 0 {
			results[queenID] = added
		}
	}
	return results
}

// listColonyIDs mirrors colony_metadata.list_colony_ids: the names of every
// colony that has a metadata.json on disk, sorted.
//
// Parity: framework/host/colony_metadata.py::list_colony_ids
func listColonyIDs(home hivehome.Home) []string {
	entries, err := os.ReadDir(home.ColoniesDir())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(home.ColoniesDir(), entry.Name(), "metadata.json")); err != nil {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// MigrateColonySidecars mirrors migrate_colony_sidecars: apply GA grants to
// every colony's tools.json. A colony resolves its role categories via
// metadata.json -> queen_name -> the role-default table; colonies forked from
// custom queens are skipped for the same reason as the queen path. Returns
// {colony_id: [newly granted tools]} for colonies that changed.
func MigrateColonySidecars(home hivehome.Home, warn func(string)) map[string][]string {
	results := map[string][]string{}
	for _, colonyID := range listColonyIDs(home) {
		metadata := loadColonyMetadata(home, colonyID)
		queenName, _ := metadata["queen_name"].(string)
		if queenName == "" || len(queen.DefaultToolsFor(queenName)) == 0 {
			continue
		}
		sidecar := filepath.Join(home.ColoniesDir(), colonyID, "tools.json")
		added, applyErr := gaApplyToSidecar(sidecar, queenName, warn)
		if applyErr != nil {
			gaWarn(warn, "ga_migration: failed for colony "+colonyID+" ("+applyErr.Error()+")")
			continue
		}
		if len(added) > 0 {
			results[colonyID] = added
		}
	}
	return results
}

// RunGAToolMigration mirrors run_ga_tool_migration: migrate queens then
// colonies. Never raises — per-sidecar errors are swallowed (reported through
// warn) so a single malformed file cannot block runtime boot. Returns the
// changes per pass so the caller can log summaries.
func RunGAToolMigration(home hivehome.Home, warn func(string)) (map[string][]string, map[string][]string) {
	queenChanges := MigrateQueenSidecars(home, warn)
	colonyChanges := MigrateColonySidecars(home, warn)
	return queenChanges, colonyChanges
}
