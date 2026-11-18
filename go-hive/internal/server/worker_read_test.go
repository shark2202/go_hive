package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/maintenance"
)

const deepCleanedWorkerID = "session_20250101_120000_deadbeef"

func serverTestHome(t *testing.T) hivehome.Home {
	t.Helper()
	home, err := hivehome.ResolveAt(t.TempDir())
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	return home
}

func writeServerTestJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeServerTestFile(t, path, string(data))
}

func writeServerTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func ageServerTree(t *testing.T, root string, ageDays float64) {
	t.Helper()
	old := time.Now().Add(-time.Duration(ageDays * 86400 * float64(time.Second)))
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatalf("age %s: %v", root, err)
	}
}

// Parity: core/tests/test_janitor_worker_deepclean.py::test_endpoints_render_after_deep_clean
func TestEndpointsRenderAfterDeepClean(t *testing.T) {
	home := serverTestHome(t)
	wdir := filepath.Join(home.ColoniesDir(), "c1", "workers", deepCleanedWorkerID)
	parts := filepath.Join(wdir, "conversations", "parts")
	writeServerTestJSON(t, filepath.Join(wdir, "meta.json"), map[string]any{
		"worker_id":        deepCleanedWorkerID,
		"queen_session_id": "session_20250101_110000_beefbeef",
		"colony_id":        "c1",
		"task":             "do the thing",
		"profile_name":     "researcher",
	})
	writeServerTestFile(t, filepath.Join(wdir, "tasks.json"), `{"tasks": []}`)
	writeServerTestJSON(t, filepath.Join(parts, "0000000001.json"), map[string]any{"seq": 1, "role": "user", "content": "task"})
	writeServerTestJSON(t, filepath.Join(parts, "0000000002.json"), map[string]any{"seq": 2, "role": "assistant", "content": "working on it"})
	writeServerTestJSON(t, filepath.Join(parts, "0000000003.json"), map[string]any{"seq": 3, "role": "assistant", "content": "final summary of the work"})
	writeServerTestFile(t, filepath.Join(wdir, "data", "web_scrape_1.txt"), "spill")
	ageServerTree(t, wdir, 30)

	cfg := maintenance.DefaultRetentionConfig()
	cfg.ActiveGraceHours = 0
	cfg.IOSleepMS = 0
	cfg.Mode = "delete"
	cfg.WorkerDeepCleanDays = 14
	maintenance.RunOnce(home, maintenance.ForOffline(cfg, float64(time.Now().UnixNano())/float64(time.Second)), &cfg, maintenance.RunOptions{
		Tiers:   map[int]bool{2: true},
		Execute: true,
	})

	detail := ReadWorkerFromDisk(deepCleanedWorkerID, wdir)
	if detail["status"] != "historical" {
		t.Fatalf("status = %v, want historical", detail["status"])
	}
	if detail["task"] != "do the thing" {
		t.Fatalf("task = %v, want do the thing", detail["task"])
	}
	result, ok := detail["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %T, want object", detail["result"])
	}
	if result["summary"] != "final summary of the work" {
		t.Fatalf("result.summary = %v, want the tombstone summary", result["summary"])
	}

	convo := ReadWorkerConversation(wdir)
	want := map[string]any{"messages": []any{}, "total": 0, "truncated": false}
	if !reflect.DeepEqual(convo, want) {
		t.Fatalf("conversation = %#v, want %#v", convo, want)
	}
}
