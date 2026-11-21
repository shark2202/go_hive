package envcontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// supportLedger is go_hive/contracts/config-support-ledger.json: the frozen
// answer to "is this key/variable wired, or is it explicitly not?".
type supportLedger struct {
	Keys []struct {
		Path    string `json:"path"`
		Status  string `json:"status"`
		SameAs  string `json:"same_as"`
		Reason  string `json:"reason"`
		Anchors []struct {
			File     string `json:"file"`
			Contains string `json:"contains"`
		} `json:"anchors"`
		Tests []string `json:"tests"`
	} `json:"keys"`
	EnvVars []struct {
		Names          []string `json:"names"`
		Status         string   `json:"status"`
		Reason         string   `json:"reason"`
		MatchesWildard bool     `json:"matches_wildcard"`
		Anchors        []struct {
			File     string `json:"file"`
			Contains string `json:"contains"`
		} `json:"anchors"`
		Tests []string `json:"tests"`
	} `json:"env_vars"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, src, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	// <repo>/go_hive/go-hive/internal/envcontract -> <repo>
	return filepath.Join(filepath.Dir(src), "..", "..", "..", "..")
}

func readLedger(t *testing.T) (supportLedger, string) {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, "go_hive", "contracts", "config-support-ledger.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var ledger supportLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return ledger, root
}

// ledgerStatuses are the statuses an entry may carry. "wired" claims the
// runtime reads the value and a test proves it; "platform_diff" claims the
// mechanism the name tunes does not exist in the Go port, so the entry owes a
// reason plus the anchor of the substitute mechanism; everything short of a Go
// reader must carry a reason, so an unimplemented item is never silent.
var ledgerStatuses = map[string]bool{
	"wired":         true,
	"partial":       true,
	"unsupported":   true,
	"platform_diff": true,
	"go_extension":  true,
	"unread":        true,
}

// requiresReason reports whether a status must explain itself. Everything that
// stops short of a proven Go reader does, and a platform difference is nothing
// but an explanation: it is the claim that no reader is coming.
func requiresReason(status string) bool {
	switch status {
	case "partial", "unsupported", "platform_diff":
		return true
	}
	return false
}

// TestSupportLedgerCoversEveryGap is the anti-silence check: every key the
// frozen contract calls go_partial or python_only — the two buckets that mean
// "the Go side does not fully honour this" — must appear in the ledger, and
// every recommendation the env audit lists must be covered by name.
//
// Parity-exempt: this is a Go-side governance artifact over generated manifests
// plus a hand-maintained ledger; the Python reference has no equivalent test and
// nothing here exercises runtime behaviour.
func TestSupportLedgerCoversEveryGap(t *testing.T) {
	ledger, root := readLedger(t)
	entries := map[string]bool{}
	for _, key := range ledger.Keys {
		entries[key.Path] = true
		if key.Status == "" || !ledgerStatuses[key.Status] {
			t.Errorf("key %s has an unknown status %q", key.Path, key.Status)
		}
		if requiresReason(key.Status) && strings.TrimSpace(key.Reason) == "" {
			t.Errorf("key %s is %s without a reason", key.Path, key.Status)
		}
		if key.SameAs != "" && !entries[key.SameAs] {
			// same_as may point at an entry declared later in the file, so
			// this is re-checked after the first pass below.
			continue
		}
	}
	for _, key := range ledger.Keys {
		if key.SameAs != "" && !entries[key.SameAs] {
			t.Errorf("key %s points at unknown same_as %q", key.Path, key.SameAs)
		}
	}

	cfgRaw, err := os.ReadFile(filepath.Join(root, "go_hive", "contracts", "config-keys.json"))
	if err != nil {
		t.Fatalf("read config-keys.json: %v", err)
	}
	var cfg configManifest
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("parse config-keys.json: %v", err)
	}
	gaps := 0
	for _, section := range cfg.Sections {
		for _, key := range section.Keys {
			if key.Status != "go_partial" && key.Status != "python_only" {
				continue
			}
			gaps++
			if !entries[key.Path] {
				t.Errorf("%s is %s but has no support-ledger entry", key.Path, key.Status)
			}
		}
	}
	if gaps == 0 {
		t.Fatal("no go_partial/python_only keys found; the manifest no longer measures the gap")
	}

	envRaw, err := os.ReadFile(filepath.Join(root, "go_hive", "contracts", "env-vars.json"))
	if err != nil {
		t.Fatalf("read env-vars.json: %v", err)
	}
	var envDoc struct {
		Recommendations []struct {
			Name string `json:"name"`
		} `json:"recommendations"`
	}
	if err := json.Unmarshal(envRaw, &envDoc); err != nil {
		t.Fatalf("parse env-vars.json: %v", err)
	}
	covered := map[string]bool{}
	for _, entry := range ledger.EnvVars {
		if entry.Status == "" || !ledgerStatuses[entry.Status] {
			t.Errorf("env entry %v has an unknown status %q", entry.Names, entry.Status)
		}
		if requiresReason(entry.Status) && strings.TrimSpace(entry.Reason) == "" {
			t.Errorf("env entry %v is %s without a reason", entry.Names, entry.Status)
		}
		for _, name := range entry.Names {
			covered[name] = true
		}
	}
	for _, rec := range envDoc.Recommendations {
		if !covered[rec.Name] {
			t.Errorf("env recommendation %s has no support-ledger entry", rec.Name)
		}
	}
}

// TestSupportLedgerWiredEntriesResolve checks the "wired" claims: every anchor
// file must exist and contain the quoted code, and every named test must exist
// in the Go tree. An entry that claims consumption must point at the code that
// does the consuming.
//
// Parity-exempt: Go-side governance artifact; see TestSupportLedgerCoversEveryGap.
func TestSupportLedgerWiredEntriesResolve(t *testing.T) {
	ledger, root := readLedger(t)
	tests := collectTestFuncs(t, filepath.Join(root, "go_hive", "go-hive"))

	type anchorSet = []struct {
		File     string `json:"file"`
		Contains string `json:"contains"`
	}
	// same_as aliases inherit the target's anchors and tests: the value is
	// consumed by the same reader, so repeating them per key would drift.
	byPath := map[string]int{}
	for i, key := range ledger.Keys {
		byPath[key.Path] = i
	}
	resolve := func(keyIndex int, anchors anchorSet, names []string) (anchorSet, []string) {
		seen := map[int]bool{}
		for {
			key := ledger.Keys[keyIndex]
			if len(anchors) == 0 {
				anchors = key.Anchors
			}
			if len(names) == 0 {
				names = key.Tests
			}
			target, ok := byPath[key.SameAs]
			if !ok || key.SameAs == "" || seen[target] {
				return anchors, names
			}
			seen[target] = true
			keyIndex = target
		}
	}

	check := func(label string, status, reason string, anchors anchorSet, names []string) {
		t.Helper()
		if status == "wired" {
			if len(anchors) == 0 {
				t.Errorf("%s is wired without anchors", label)
			}
			if len(names) == 0 {
				t.Errorf("%s is wired without a proving test", label)
			}
		}
		if requiresReason(status) && strings.TrimSpace(reason) == "" {
			t.Errorf("%s is %s without a reason", label, status)
		}
		if status == "platform_diff" && len(anchors) == 0 {
			// A platform difference is not a gap left open by accident: it
			// must point at the mechanism that replaces the missing one.
			t.Errorf("%s is platform_diff without a substitute anchor", label)
		}
		for _, anchor := range anchors {
			path := filepath.Join(root, filepath.FromSlash(anchor.File))
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("%s anchor %s: %v", label, anchor.File, err)
				continue
			}
			if !strings.Contains(string(raw), anchor.Contains) {
				t.Errorf("%s anchor %s no longer contains %q", label, anchor.File, anchor.Contains)
			}
		}
		for _, name := range names {
			if !tests[name] {
				t.Errorf("%s names test %s, which does not exist", label, name)
			}
		}
	}

	for i, key := range ledger.Keys {
		anchors, names := resolve(i, key.Anchors, key.Tests)
		check("key "+key.Path, key.Status, key.Reason, anchors, names)
	}
	for _, entry := range ledger.EnvVars {
		check("env "+strings.Join(entry.Names, ","), entry.Status, entry.Reason, entry.Anchors, entry.Tests)
	}
}

var testFuncRe = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)

func collectTestFuncs(t *testing.T, moduleRoot string) map[string]bool {
	t.Helper()
	found := map[string]bool{}
	err := filepath.Walk(moduleRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, match := range testFuncRe.FindAllStringSubmatch(string(raw), -1) {
			found[match[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", moduleRoot, err)
	}
	if len(found) == 0 {
		t.Fatal("no Go test functions found")
	}
	return found
}
