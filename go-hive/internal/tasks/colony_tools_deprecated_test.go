package tasks

import (
	"strings"
	"testing"
)

// TestColonyTemplateToolsAreNotRegistered pins the deprecation decision for
// the colony_template_* family (colony_tools.go): it is unregistered legacy
// code, and re-wiring it should be a deliberate change that updates this test
// and the removal checklist in go_hive/docs/delivery-backlog.json.
// Parity-exempt: Go-only deprecation guard; Python has no colony_template_* registration.
func TestColonyTemplateToolsAreNotRegistered(t *testing.T) {
	registry := NewRegistry()
	RegisterTaskTools(registry, NewTaskStore(t.TempDir()), nil)

	for _, name := range registry.Names() {
		if strings.HasPrefix(name, "colony_template_") {
			t.Fatalf("colony template tool %q was registered; it is deprecated and scheduled for removal", name)
		}
	}
}
