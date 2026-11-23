package agentloop

// Parity: framework/agent_loop/internals/compaction.py::write_compaction_debug_log
//
// Opt-in compaction forensics: with HIVE_COMPACTION_DEBUG set to a non-empty
// value (upstream's `if os.environ.get("HIVE_COMPACTION_DEBUG")` gate), every
// compaction writes a markdown report to $HIVE_HOME/compaction_log/
// <utc-stamp>_<node>.md holding the usage before/after, the level that fired
// and the pre-compaction message inventory ranked by size.
//
// The write is best-effort: a broken path logs nothing and never breaks the
// turn (upstream catches OSError).

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

// envNonEmpty is the package-local spelling of the upstream string gate
// (`if os.environ.get(name)`), kept scanner-visible like every other reader.
func envNonEmpty(name string) bool { return pyenv.NonEmpty(name) }

// compactionDebugEnabled mirrors upstream's truthiness gate: any non-empty
// value enables the log.
func compactionDebugEnabled() bool { return envNonEmpty("HIVE_COMPACTION_DEBUG") }

// CompactionLevel labels what a compaction actually did, using upstream's rule:
// a sub-1-point drop is prune-only, a drop to <= 60% is an LLM summary, and
// anything else is structural.
func CompactionLevel(beforeRatio, afterRatio float64) string {
	beforePct := compactionPercent(beforeRatio)
	afterPct := compactionPercent(afterRatio)
	switch {
	case afterPct >= beforePct-1:
		return "prune_only"
	case afterRatio <= 0.6:
		return "llm"
	default:
		return "structural"
	}
}

// WriteCompactionDebugLog writes one report and returns its path. It is a
// no-op returning "" when the env gate is off; write failures are swallowed.
func WriteCompactionDebugLog(homeRoot, agentID, agentName, streamID string, beforeRatio, afterRatio float64, level string, inventory []map[string]any) string {
	if !compactionDebugEnabled() {
		return ""
	}
	logDir := filepath.Join(homeRoot, "compaction_log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return ""
	}
	now := time.Now().UTC()
	stamp := fmt.Sprintf("%s_%06d", now.Format("20060102T150405"), now.Nanosecond()/1000)
	nodeLabel := strings.ReplaceAll(agentID, "/", "_")
	logPath := filepath.Join(logDir, fmt.Sprintf("%s_%s.md", stamp, nodeLabel))

	lines := []string{
		fmt.Sprintf("# Compaction Debug — %s", agentID),
		fmt.Sprintf("**Time:** %s", now.Format(time.RFC3339)),
		fmt.Sprintf("**Node:** %s (`%s`)", agentName, agentID),
	}
	if streamID != "" {
		lines = append(lines, fmt.Sprintf("**Stream:** %s", streamID))
	}
	lines = append(lines,
		fmt.Sprintf("**Level:** %s", level),
		fmt.Sprintf("**Usage:** %d%% → %d%%", int(beforeRatio*100+0.5), int(afterRatio*100+0.5)),
		"",
	)
	lines = append(lines, inventorySection(inventory)...)
	lines = append(lines, "")

	if err := os.WriteFile(logPath, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return ""
	}
	return logPath
}

// inventorySection renders the ranked inventory table plus previews.
func inventorySection(inventory []map[string]any) []string {
	if len(inventory) == 0 {
		return nil
	}
	chars := func(entry map[string]any) int {
		return intValue(entry["content_chars"]) + intValue(entry["tool_call_args_chars"])
	}
	total := 0
	for _, entry := range inventory {
		total += chars(entry)
	}
	ranked := append([]map[string]any(nil), inventory...)
	sort.SliceStable(ranked, func(i, j int) bool { return chars(ranked[i]) > chars(ranked[j]) })

	lines := []string{
		fmt.Sprintf("## Pre-Compaction Message Inventory (%d messages, %s total chars)", len(inventory), withThousands(total)),
		"",
		"| # | seq | role | tool | chars | % of total | flags |",
		"|---|-----|------|------|------:|------------|-------|",
	}
	for i, entry := range ranked {
		entryChars := chars(entry)
		pct := 0.0
		if total > 0 {
			pct = float64(entryChars) / float64(total) * 100
		}
		flags := []string{}
		if truthy(entry["is_error"]) {
			flags = append(flags, "error")
		}
		if phase, ok := entry["phase"].(string); ok && phase != "" {
			flags = append(flags, "phase="+phase)
		}
		lines = append(lines, fmt.Sprintf("| %d | %v | %v | %v | %s | %.1f%% | %s |",
			i+1, entry["seq"], entry["role"], stringValue(entry["tool"]),
			withThousands(entryChars), pct, strings.Join(flags, ", ")))
	}
	previews := []map[string]any{}
	for _, entry := range ranked {
		if preview, ok := entry["preview"].(string); ok && preview != "" {
			previews = append(previews, entry)
		}
	}
	if len(previews) > 0 {
		lines = append(lines, "", "### Large message previews")
		for _, entry := range previews {
			lines = append(lines,
				fmt.Sprintf("\n**seq=%v** (%v, %v):", entry["seq"], entry["role"], stringValue(entry["tool"])),
				fmt.Sprintf("```\n%v\n```", entry["preview"]),
			)
		}
	}
	return lines
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	}
	return 0
}

// withThousands renders an int with comma separators (Python's `{:,}`).
func withThousands(value int) string {
	digits := fmt.Sprintf("%d", value)
	if value < 0 {
		return "-" + withThousands(-value)
	}
	out := ""
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out += ","
		}
		out += string(r)
	}
	return out
}

// compactionLogDir is $HIVE_HOME/compaction_log (hivehome already knows it).
func compactionLogDir(home hivehome.Home) string { return home.CompactionLogDir() }
