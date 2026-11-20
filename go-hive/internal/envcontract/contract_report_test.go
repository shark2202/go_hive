package envcontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type envManifest struct {
	Verification struct {
		CountsMatch bool           `json:"counts_match"`
		Reproduced  map[string]int `json:"reproduced"`
	} `json:"verification"`
	Variables []struct {
		Name            string `json:"name"`
		Status          string `json:"status"`
		AuditMembership string `json:"audit_membership"`
	} `json:"variables"`
}

type configManifest struct {
	Summary struct {
		Keys         int            `json:"keys"`
		StatusCounts map[string]int `json:"status_counts"`
	} `json:"summary"`
	Sections []struct {
		Name string `json:"name"`
		Keys []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"keys"`
	} `json:"sections"`
}

func contractDir(t *testing.T) string {
	t.Helper()
	_, src, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	// <repo>/go_hive/go-hive/internal/envcontract -> <repo>/go_hive/contracts
	return filepath.Join(filepath.Dir(src), "..", "..", "..", "contracts")
}

// Parity-exempt: report-only gate over generated JSON manifests; the Python
// reference has no equivalent test and this test asserts no runtime behaviour.
func TestFrozenContractManifests(t *testing.T) {
	dir := contractDir(t)
	envPath := filepath.Join(dir, "env-vars.json")
	cfgPath := filepath.Join(dir, "config-keys.json")

	envRaw, envErr := os.ReadFile(envPath)
	cfgRaw, cfgErr := os.ReadFile(cfgPath)
	if os.IsNotExist(envErr) && os.IsNotExist(cfgErr) {
		t.Skip("contract manifests not present (standalone checkout); run go_hive/scripts/freeze_env_config_contract.py")
	}
	if envErr != nil {
		t.Fatalf("read %s: %v", envPath, envErr)
	}
	if cfgErr != nil {
		t.Fatalf("read %s: %v", cfgPath, cfgErr)
	}

	var env envManifest
	if err := json.Unmarshal(envRaw, &env); err != nil {
		t.Fatalf("parse %s: %v", envPath, err)
	}
	var cfg configManifest
	if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
		t.Fatalf("parse %s: %v", cfgPath, err)
	}

	// REPORT ONLY: log the frozen numbers and the current gap buckets. The
	// manifests are a measurement, not an acceptance gate; make this fatal
	// only when the team decides the gaps are closed.
	t.Logf("env audit: python=%d go=%d common=%d python_only=%d go_only=%d (matches published audit: %v)",
		env.Verification.Reproduced["python"], env.Verification.Reproduced["go"],
		env.Verification.Reproduced["common"], env.Verification.Reproduced["python_only"],
		env.Verification.Reproduced["go_only"], env.Verification.CountsMatch)

	envStatus := map[string]int{}
	for _, v := range env.Variables {
		envStatus[v.Status]++
	}
	t.Logf("env per-name status: %v (variables=%d)", envStatus, len(env.Variables))

	cfgStatus := cfg.Summary.StatusCounts
	t.Logf("configuration.json keys=%d status=%v", cfg.Summary.Keys, cfgStatus)
	t.Logf("contract gaps to close: python_only=%d go_only=%d runtime_partial=%d type/default/parse=%d",
		cfgStatus["python_only"], cfgStatus["go_only"], cfgStatus["go_partial"],
		cfgStatus["type_differs"]+cfgStatus["default_differs"]+cfgStatus["parse_differs"])

	var missing, extra []string
	for _, section := range cfg.Sections {
		for _, key := range section.Keys {
			switch key.Status {
			case "python_only":
				missing = append(missing, key.Path)
			case "go_only":
				extra = append(extra, key.Path)
			}
		}
	}
	t.Logf("config missing in go (python_only): %v", missing)
	t.Logf("config extra in go (go_only): %v", extra)
}
