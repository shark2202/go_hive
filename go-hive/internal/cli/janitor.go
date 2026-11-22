package cli

// Parity: framework/maintenance/cli.py

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/maintenance"
)

// Parity: framework/maintenance/cli.py::register_janitor_commands
func (r *Runner) janitorCommands() []*commandSpec {
	return []*commandSpec{{
		Prog:        "hive janitor",
		Help:        "Data-retention janitor (dry-run by default)",
		Description: "Prune aged debug logs, finished-worker transcripts, and cold-session bloat from HIVE_HOME.",
		Dest:        "janitor_command",
		Subcommands: []*commandSpec{
			{
				Prog: "hive janitor run",
				Help: "Run a janitor pass (dry-run unless --execute)",
				Flags: []argSpec{
					{Dest: "execute", Long: "execute", Kind: kindBool,
						Help: "Actually delete/archive (default: dry-run report only)"},
					{Dest: "tiers", Long: "tiers", Kind: kindString, Default: "1,2,3",
						Help: "Comma-separated tiers to run (default: 1,2,3)"},
					{Dest: "legacy", Long: "legacy", Kind: kindBool,
						Help: "One-time: archive+delete legacy agents/ tree + orphaned index sweep"},
					{Dest: "junk", Long: "junk", Kind: kindBool,
						Help: "Also delete oversized top-level entries outside the known layout"},
					{Dest: "no_archive", Long: "no-archive", Kind: kindBool,
						Help: "Delete outright instead of archiving first"},
					{Dest: "port", Long: "port", Kind: kindInt, Default: 8787,
						Help: "Local runtime port to probe for live-server safety check"},
					{Dest: "verbose", Short: "v", Long: "verbose", Kind: kindBool,
						Help: "Print every manifest line"},
				},
				Run: r.cmdJanitorRun,
			},
			{
				Prog: "hive janitor status",
				Help: "Show the last janitor report",
				Run:  r.cmdJanitorStatus,
			},
		},
	}}
}

// Parity: framework/maintenance/cli.py::cmd_janitor_run
func (r *Runner) cmdJanitorRun(args *parsedArgs) int {
	tiers, message, ok := parseJanitorTiers(args.str("tiers"))
	if !ok {
		fmt.Fprint(r.Stdout, message)
		return 2
	}

	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stderr, "%s\n", err.Error())
		return 1
	}
	cfg := maintenance.LoadRetentionConfig(home)
	if args.boolean("no_archive") {
		cfg.Mode = "delete"
	}

	execute := args.boolean("execute")
	legacy := args.boolean("legacy")
	junk := args.boolean("junk")

	needsSessionSafety := execute && (tiers[2] || tiers[3] || legacy || junk)
	if needsSessionSafety && r.janitorServerIsUp(home, args.intValue("port")) {
		fmt.Fprint(r.Stdout, janitorLiveServerMessage(execute, tiers))
		return 1
	}

	safety := maintenance.ForOffline(cfg, float64(time.Now().UnixNano())/float64(time.Second))
	started := time.Now()
	report := maintenance.RunOnce(home, safety, &cfg, maintenance.RunOptions{
		Tiers:         tiers,
		Execute:       execute,
		IncludeLegacy: legacy,
		IncludeJunk:   junk,
	})

	printJanitorReport(r.Stdout, report.ToMap(), args.boolean("verbose"))
	fmt.Fprintf(r.Stdout, "\nelapsed: %.1fs\n", time.Since(started).Seconds())
	if report.Error != "" {
		fmt.Fprintf(r.Stdout, "ERROR: %s\n", report.Error)
		return 1
	}
	if report.DryRun {
		fmt.Fprint(r.Stdout, "dry-run only — re-run with --execute to apply\n")
	}
	return 0
}

func parseJanitorTiers(raw string) (map[int]bool, string, bool) {
	tiers := map[int]bool{}
	for _, token := range strings.Split(raw, ",") {
		if strings.TrimSpace(token) == "" {
			continue
		}
		value, err := strconv.Atoi(strings.TrimSpace(token))
		if err != nil {
			return nil, fmt.Sprintf("invalid --tiers value: %s\n", pyRepr(raw)), false
		}
		tiers[value] = true
	}
	for tier := range tiers {
		if tier != 1 && tier != 2 && tier != 3 {
			return nil, fmt.Sprintf("invalid --tiers value: %s (allowed: 1,2,3)\n", pyRepr(raw)), false
		}
	}
	return tiers, "", true
}

func (r *Runner) janitorServerIsUp(home hivehome.Home, port int) bool {
	if maintenance.LiveServerOwnsHiveHome(home) {
		return true
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/health", port))
	if err != nil {
		return response != nil
	}
	_ = response.Body.Close()
	return true
}

func janitorLiveServerMessage(execute bool, tiers map[int]bool) string {
	executeLiteral := "false"
	if execute {
		executeLiteral = "true"
	}
	return "A live Hive runtime owns this HIVE_HOME (server marker or port probe).\n" +
		"Tiers 2/3 and --legacy/--junk must run through the server so live sessions\n" +
		"are protected — POST /api/maintenance/janitor/run on the runtime's port, e.g.:\n" +
		"  curl -X POST http://127.0.0.1:<port>/api/maintenance/janitor/run " +
		`-H "Content-Type: application/json" ` +
		"-d '{\"execute\": " + executeLiteral + ", \"tiers\": " + janitorTiersRepr(tiers) + "}'\n" +
		"Or stop the desktop app / server and re-run this command.\n"
}

// Parity: framework/maintenance/cli.py::cmd_janitor_status
func (r *Runner) cmdJanitorStatus(args *parsedArgs) int {
	home, err := r.home()
	if err != nil {
		fmt.Fprintf(r.Stderr, "%s\n", err.Error())
		return 1
	}
	raw, ok := maintenance.LoadLastReport(home)
	report, isMap := raw.(map[string]any)
	if !ok || !isMap {
		fmt.Fprint(r.Stdout, "no janitor run recorded\n")
		return 1
	}
	printJanitorReport(r.Stdout, report, false)
	return 0
}

// Parity: framework/maintenance/cli.py::_print_report
func printJanitorReport(out io.Writer, report map[string]any, verbose bool) {
	header := "DRY-RUN (nothing deleted)"
	if !janitorTruthy(report["dry_run"]) {
		header = "EXECUTED (mode=" + pyStr(report["mode"]) + ")"
	}
	fmt.Fprintf(out, "janitor report — %s\n", header)
	fmt.Fprintf(out, "  tiers: %s   started: %s\n",
		janitorTiersFromReport(report["tiers"]), janitorCtime(report["started_at"]))

	for _, target := range janitorTargets(report["targets"]) {
		files := janitorInt(target["files"])
		bytesFreed := janitorInt(target["bytes_freed"])
		skipped := janitorInt(target["skipped"])
		errs := janitorErrors(target["errors"])
		if files == 0 && bytesFreed == 0 && skipped == 0 && len(errs) == 0 {
			continue
		}
		line := fmt.Sprintf("  %-22s files=%-7d freed=%-10s skipped=%d",
			pyStr(target["name"]), files, janitorHuman(bytesFreed), skipped)
		if len(errs) > 0 {
			line += fmt.Sprintf(" errors=%d", len(errs))
		}
		fmt.Fprintf(out, "%s\n", line)
	}

	fmt.Fprintf(out, "  TOTAL: %s across %d files\n",
		janitorHuman(janitorInt(report["total_bytes"])), janitorInt(report["total_files"]))

	if archive, ok := report["archive_path"].(string); ok && archive != "" {
		fmt.Fprintf(out, "  archive: %s\n", archive)
	}
	if manifest, ok := report["manifest_path"].(string); ok && manifest != "" {
		fmt.Fprintf(out, "  manifest: %s\n", manifest)
		if verbose {
			printJanitorManifest(out, manifest)
		}
	}
}

func printJanitorManifest(out io.Writer, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, line := range lines {
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return
		}
		outcome, okOutcome := item["outcome"].(string)
		action, okAction := item["action"].(string)
		bytesValue, okBytes := item["bytes"]
		itemPath, okPath := item["path"].(string)
		reason, okReason := item["reason"].(string)
		if !okOutcome || !okAction || !okBytes || !okPath || !okReason {
			return
		}
		fmt.Fprintf(out, "    [%-9s] %-9s %-10s %s  (%s)\n",
			outcome, action, janitorHuman(janitorInt(bytesValue)), itemPath, reason)
	}
}

func janitorHuman(n int64) string {
	value := float64(n)
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if math.Abs(value) < 1024 {
			if unit == "B" {
				return fmt.Sprintf("%.0f%s", value, unit)
			}
			return fmt.Sprintf("%.1f%s", value, unit)
		}
		value /= 1024
	}
	return fmt.Sprintf("%.1fTB", value)
}

func janitorTiersRepr(tiers map[int]bool) string {
	values := make([]int, 0, len(tiers))
	for tier := range tiers {
		values = append(values, tier)
	}
	sort.Ints(values)
	items := make([]any, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	return pyRepr(items)
}

func janitorTiersFromReport(value any) string {
	switch typed := value.(type) {
	case []int:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return pyRepr(items)
	case []any:
		return pyRepr(typed)
	case nil:
		return "None"
	}
	return pyStr(value)
}

func janitorCtime(value any) string {
	seconds, ok := janitorFloatValue(value)
	if !ok {
		seconds = 0
	}
	whole := math.Trunc(seconds)
	nanos := int64((seconds - whole) * 1e9)
	return time.Unix(int64(whole), nanos).Local().Format("Mon Jan _2 15:04:05 2006")
}

func janitorTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case float64:
		return typed != 0
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	}
	return true
}

func janitorTargets(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	targets := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if target, ok := item.(map[string]any); ok {
			targets = append(targets, target)
		}
	}
	return targets
}

func janitorErrors(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []string:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return items
	}
	return nil
}

func janitorInt(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	}
	return 0
}

func janitorFloatValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	}
	return 0, false
}
