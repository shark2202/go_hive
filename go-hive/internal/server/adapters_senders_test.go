package server

// Ports tools/tests/test_senders_feature_flag.py: the suite is opt-in, and the
// gate is registration itself. With the flag off the model must have no way to
// learn the tool exists — hiding a settings page while the tools stayed live
// would be a fake gate.

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/tools"
)

func senderTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	return hivehome.Home{Root: t.TempDir()}
}

// Parity: tools/tests/test_senders_feature_flag.py::test_senders_absent_by_default
func TestSenderToolsAbsentByDefault(t *testing.T) {
	t.Setenv("HIVE_EMAIL_SENDERS", "")
	registry := tools.NewToolRegistry()

	installed, err := RegisterSenderTools(registry, senderTestHome(t))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if installed != 0 {
		t.Fatalf("installed = %d, want 0 while the feature is off", installed)
	}
	if registry.HasTool("senders") {
		t.Fatal("senders tool is reachable while the feature is off")
	}
}

// Parity: tools/tests/test_senders_feature_flag.py::test_senders_absent_when_disabled
func TestSenderToolsAbsentWhenDisabled(t *testing.T) {
	for _, value := range []string{"0", "false", "no", "off", "FALSE"} {
		t.Setenv("HIVE_EMAIL_SENDERS", value)
		registry := tools.NewToolRegistry()
		installed, err := RegisterSenderTools(registry, senderTestHome(t))
		if err != nil {
			t.Fatalf("%s: register: %v", value, err)
		}
		if installed != 0 || registry.HasTool("senders") {
			t.Fatalf("%s: installed = %d, hasTool = %v; want absent", value, installed, registry.HasTool("senders"))
		}
	}
}

// Parity: tools/tests/test_senders_feature_flag.py::test_senders_present_when_enabled
func TestSenderToolsPresentWhenEnabled(t *testing.T) {
	t.Setenv("HIVE_EMAIL_SENDERS", "1")
	registry := tools.NewToolRegistry()

	installed, err := RegisterSenderTools(registry, senderTestHome(t))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if installed != 1 {
		t.Fatalf("installed = %d, want 1", installed)
	}
	tool, ok := registry.GetTool("senders")
	if !ok {
		t.Fatal("senders tool missing after enabling the feature")
	}
	properties, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema = %#v", tool.Parameters)
	}
	action, ok := properties["action"].(map[string]any)
	if !ok {
		t.Fatalf("action schema = %#v", properties["action"])
	}
	if enum, ok := action["enum"].([]string); !ok || len(enum) != 9 {
		t.Fatalf("action enum = %#v, want nine actions", action["enum"])
	}
}

// Parity: tools/tests/test_senders_feature_flag.py::test_senders_present_when_enabled
func TestSenderToolsFollowsConfigurationFile(t *testing.T) {
	t.Setenv("HIVE_EMAIL_SENDERS", "")
	home := senderTestHome(t)
	if err := WriteHiveConfig(home, map[string]any{"email_senders": true}); err != nil {
		t.Fatalf("write config: %v", err)
	}

	registry := tools.NewToolRegistry()
	installed, err := RegisterSenderTools(registry, home)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if installed != 1 || !registry.HasTool("senders") {
		t.Fatalf("installed = %d, want the config toggle to enable the suite", installed)
	}
}
