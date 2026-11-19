package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	colonyhost "github.com/aden-hive/hive-go/internal/host"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// preloadedCatalogLLM answers every preloaded-session turn (the queen's chat,
// the overseer's boot message) with plain text; auxiliary calls get text too.
type preloadedCatalogLLM struct {
	mu    sync.Mutex
	calls int
}

func (m *preloadedCatalogLLM) AComplete(_ context.Context, _ llm.CompletionRequest) (llm.Completion, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	return llm.Completion{Content: "ready", StopReason: "stop"}, nil
}

// Parity: core/tests/test_skill_integration.py::test_skills_catalog_in_prompt
func TestPreloadedColonySessionInjectsSkillsCatalog(t *testing.T) {
	root := t.TempDir()
	home, err := hivehome.ResolveAt(filepath.Join(root, "hive"))
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	colonyPath := writeColonySpecFixture(t, home, "honeycomb",
		`{"name":"worker","description":"bee counter","system_prompt":"count bees carefully",`+
			`"tools":["read_file"],"goal":{"description":"count the bees"}}`)
	// writeQueenSkill lives in loader_reminders_test.go: the preloaded colony
	// session runs under the default queen scope.
	writeQueenSkill(t, home, "default", "preload-catalog-probe", "A probe skill.")

	provider := &preloadedCatalogLLM{}
	previous := chatProviderFactory
	chatProviderFactory = func(hivehome.Home, string) (llm.Completer, error) { return provider, nil }
	t.Cleanup(func() { chatProviderFactory = previous })

	var stdout bytes.Buffer
	runner := &Runner{Stdout: &stdout, Stderr: &stdout}
	manager := hiveserver.NewSessionManager()
	var liveMu sync.Mutex
	live := make([]*hiveserver.Session, 0)
	record := runner.loadPreloadedColonySession(colonyPath, manager, &liveMu, &live, home, "")
	if record == nil {
		t.Fatalf("preloaded session is nil; output: %s", stdout.String())
	}
	if record.Chat == nil {
		t.Fatalf("preloaded session has no chat runtime; output: %s", stdout.String())
	}
	t.Cleanup(func() {
		_ = record.Chat.Close()
		if host, ok := record.Colony.(*colonyhost.Host); ok {
			host.Stop()
		}
	})

	if err := record.Chat.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		parts, err := record.Chat.Transcript()
		if err != nil {
			t.Fatalf("Transcript: %v", err)
		}
		for _, part := range parts {
			content, _ := part["content"].(string)
			if strings.Contains(content, "<name>preload-catalog-probe</name>") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("preloaded session transcript never carried the queen skills catalog; output: %s", stdout.String())
}
