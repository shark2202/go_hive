package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/colony"
	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/skills"
	"github.com/aden-hive/hive-go/internal/tools/builtin"
	"github.com/aden-hive/hive-go/internal/tracker"
)

// fakePlaybookSpawner is the run_playbook slice of a colony runtime: the
// run_worker spawner plus report waiting and the tracker binding. It lets the
// session-catalogue tests exercise the tool without a live agent loop.
type fakePlaybookSpawner struct {
	*fakeRunWorkerSpawner
	binding *tracker.Binding
}

func (f *fakePlaybookSpawner) WaitForWorkerReports(_ context.Context, ids []string, _ time.Duration) ([]map[string]any, error) {
	reports := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		reports = append(reports, map[string]any{"worker_id": id, "status": "success", "data": map[string]any{}})
	}
	return reports, nil
}

func (f *fakePlaybookSpawner) TrackerBinding() *tracker.Binding { return f.binding }

// Parity-exempt: assembly guard. The queen session must advertise the skill
// tools and run_playbook; the execution gate (not the def) enforces the colony
// binding because the catalogue is built before a colony attaches.
func TestSessionToolCatalogueAdvertisesSkillAndPlaybookTools(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	defined := map[string]struct{}{}
	for _, def := range catalogue.Defs() {
		defined[def.Name] = struct{}{}
	}
	for _, name := range []string{
		skills.WriteSkillToolName,
		skills.SkillViewToolName,
		skills.ListSkillsToolName,
		colony.RunPlaybookToolName,
	} {
		if _, ok := defined[name]; !ok {
			t.Fatalf("tool %q missing from the session catalogue", name)
		}
		if catalogue.registry.HasTool(name) {
			t.Fatalf("tool %q should be a catalogue tool, not an integration", name)
		}
	}
}

// Parity: tests/test_write_skill_tool.py::test_without_a_colony_it_writes_the_queens_own_skill_into_her_live_catalog
func TestSessionToolCatalogueWriteSkillQueenScope(t *testing.T) {
	homeRoot := t.TempDir()
	sessionDir := hivehome.Home{Root: homeRoot}.QueenSessionDir("q1", "s1")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), hivehome.Home{Root: homeRoot})

	content, isError, err := catalogue.Execute(context.Background(), skills.WriteSkillToolName, map[string]any{
		"skill_name":        "house-style",
		"skill_description": "How Ava formats reports.",
		"skill_body":        "# House style\n\nLead with the answer.\n",
	})
	if err != nil || isError {
		t.Fatalf("write_skill: err=%v isError=%v content=%s", err, isError, content)
	}
	target := filepath.Join(homeRoot, "queens", "q1", "skills", "house-style", "SKILL.md")
	if _, statErr := os.Stat(target); statErr != nil {
		t.Fatalf("queen skill not written to %s: %v", target, statErr)
	}
	view, viewErr, err := catalogue.Execute(context.Background(), skills.SkillViewToolName, map[string]any{
		"skill_name": "house-style",
	})
	if err != nil || viewErr {
		t.Fatalf("skill_view: err=%v isError=%v content=%s", err, viewErr, view)
	}
	if !strings.Contains(view, "Lead with the answer.") {
		t.Fatalf("skill_view = %q", view)
	}
}

// Parity: tests/test_playbook_tools.py::test_playbook_tools_are_colony_queen_only
func TestSessionToolCatalogueRunPlaybookRejectsWithoutColony(t *testing.T) {
	root := t.TempDir()
	catalogue := newSessionToolCatalogue(root, builtin.New(root), hivehome.Home{Root: root})
	content, isError, err := catalogue.Execute(context.Background(), colony.RunPlaybookToolName, map[string]any{
		"playbook_name": "anything",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !isError {
		t.Fatalf("run_playbook without a colony reported success: %s", content)
	}
	if !strings.Contains(content, "no colony is bound") {
		t.Fatalf("error = %q, want an actionable no-colony message", content)
	}
}

// Parity: tests/test_write_skill_tool.py::test_write_skill_happy_path
func TestSessionToolCatalogueWriteSkillColonyScope(t *testing.T) {
	homeRoot := t.TempDir()
	colonyDir := filepath.Join(homeRoot, "colonies", "c1")
	if err := os.MkdirAll(colonyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binding, err := tracker.BindingForColonyDir(colonyDir, "c1")
	if err != nil || binding == nil {
		t.Fatalf("binding: %v", err)
	}
	sessionDir := filepath.Join(homeRoot, "sessions", "s1")
	catalogue := newSessionToolCatalogue(sessionDir, builtin.New(sessionDir), hivehome.Home{Root: homeRoot})
	ctx := colony.WithRunWorkerSpawner(context.Background(), &fakePlaybookSpawner{
		fakeRunWorkerSpawner: &fakeRunWorkerSpawner{},
		binding:              binding,
	})
	content, isError, err := catalogue.Execute(ctx, skills.WriteSkillToolName, map[string]any{
		"skill_name":        "colony-protocol",
		"skill_description": "shared protocol",
		"skill_body":        "# Protocol\n",
	})
	if err != nil || isError {
		t.Fatalf("write_skill: err=%v isError=%v content=%s", err, isError, content)
	}
	if _, statErr := os.Stat(filepath.Join(colonyDir, "skills", "colony-protocol", "SKILL.md")); statErr != nil {
		t.Fatalf("colony skill not written: %v", statErr)
	}
}
