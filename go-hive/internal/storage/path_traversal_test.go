package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shark2202/go_hive/go-hive/internal/schemas"
)

func newTraversalStorage(t *testing.T) *ConcurrentStorage {
	t.Helper()
	return NewDefaultConcurrentStorage(t.TempDir())
}

func expectInvalidKey(t *testing.T, key, wantSubstring string) {
	t.Helper()
	err := ValidateKey(key)
	if err == nil {
		t.Fatalf("ValidateKey(%q) = nil, want error", key)
	}
	if _, ok := err.(*InvalidKeyError); !ok {
		t.Fatalf("ValidateKey(%q) error type = %T, want *InvalidKeyError", key, err)
	}
	if wantSubstring != "" && !strings.Contains(err.Error(), wantSubstring) {
		t.Fatalf("ValidateKey(%q) error = %q, want substring %q", key, err.Error(), wantSubstring)
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_valid_alphanumeric_key
func TestPathTraversalValidAlphanumericKey(t *testing.T) {
	for _, key := range []string{"goal_123", "run_abc_def", "status_completed"} {
		if err := ValidateKey(key); err != nil {
			t.Fatalf("ValidateKey(%q) = %v, want nil", key, err)
		}
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_valid_key_with_hyphens_underscores
func TestPathTraversalValidKeyWithHyphensUnderscores(t *testing.T) {
	for _, key := range []string{"goal-123", "run_id_456", "completed-nodes_list"} {
		if err := ValidateKey(key); err != nil {
			t.Fatalf("ValidateKey(%q) = %v, want nil", key, err)
		}
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_parent_directory_traversal
func TestPathTraversalBlocksParentDirectoryTraversal(t *testing.T) {
	for _, key := range []string{
		"../../../etc/passwd",
		`..\..\windows\system32`,
		"goal/../../../.env",
	} {
		expectInvalidKey(t, key, "")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_leading_dot
func TestPathTraversalBlocksLeadingDot(t *testing.T) {
	expectInvalidKey(t, ".env", "path traversal detected")
	expectInvalidKey(t, ".ssh/id_rsa", "")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_absolute_paths_unix
func TestPathTraversalBlocksAbsolutePathsUnix(t *testing.T) {
	expectInvalidKey(t, "/etc/passwd", "")
	expectInvalidKey(t, "/var/www/html/shell.php", "")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_absolute_paths_windows
func TestPathTraversalBlocksAbsolutePathsWindows(t *testing.T) {
	expectInvalidKey(t, `C:\Windows\System32`, "")
	expectInvalidKey(t, `D:\config\database.yaml`, "")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_path_separators
func TestPathTraversalBlocksPathSeparators(t *testing.T) {
	expectInvalidKey(t, "goal/subdir/id", "path separators not allowed")
	expectInvalidKey(t, `goal\subdir\id`, "path separators not allowed")
	expectInvalidKey(t, "some/path/to/../../.env", "path separators not allowed")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_null_bytes
func TestPathTraversalBlocksNullBytes(t *testing.T) {
	expectInvalidKey(t, "goal\x00passwd", "null bytes not allowed")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_dangerous_shell_chars
func TestPathTraversalBlocksDangerousShellChars(t *testing.T) {
	for _, key := range []string{"goal`whoami`", "goal$(cat)", "goal|nc", "goal&& rm"} {
		expectInvalidKey(t, key, "dangerous characters")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_empty_key
func TestPathTraversalBlocksEmptyKey(t *testing.T) {
	expectInvalidKey(t, "", "empty")
	expectInvalidKey(t, "   ", "empty")
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_load_run_blocks_traversal
func TestPathTraversalLoadRunBlocksTraversal(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.LoadRun("../../../.env", true); err == nil {
		t.Fatal("LoadRun accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_load_run_valid_id_returns_none
func TestPathTraversalLoadRunValidIDReturnsNone(t *testing.T) {
	store := newTraversalStorage(t)
	result, err := store.LoadRun("legitimate_run_id", false)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if result != nil {
		t.Fatalf("LoadRun returned %+v, want nil", result)
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_delete_run_blocks_traversal
func TestPathTraversalDeleteRunBlocksTraversal(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.DeleteRun("../etc/passwd"); err == nil {
		t.Fatal("DeleteRun accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_load_summary_blocks_traversal
func TestPathTraversalLoadSummaryBlocksTraversal(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.LoadSummary("../../../.env", true); err == nil {
		t.Fatal("LoadSummary accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_load_run_sync_blocks_traversal
func TestPathTraversalLoadRunSyncBlocksTraversal(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.LoadRunSync("../../../.env"); err == nil {
		t.Fatal("LoadRunSync accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_save_run_sync_blocks_traversal
func TestPathTraversalSaveRunSyncBlocksTraversal(t *testing.T) {
	store := newTraversalStorage(t)
	run := &schemas.Run{ID: "../../../.env", GoalID: "test", GoalDescription: "", InputData: map[string]any{}}
	if err := store.SaveRunSync(run); err == nil {
		t.Fatal("SaveRunSync accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_load_run_sync_valid_id_returns_none
func TestPathTraversalLoadRunSyncValidIDReturnsNone(t *testing.T) {
	store := newTraversalStorage(t)
	result, err := store.LoadRunSync("legitimate_run_id")
	if err != nil {
		t.Fatalf("LoadRunSync: %v", err)
	}
	if result != nil {
		t.Fatalf("LoadRunSync returned %+v, want nil", result)
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_env_file_escape_via_load_sync
func TestPathTraversalBlocksEnvFileEscapeViaLoadSync(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.LoadRunSync("../../../.env"); err == nil {
		t.Fatal("LoadRunSync accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_config_file_escape_via_load_sync
func TestPathTraversalBlocksConfigFileEscapeViaLoadSync(t *testing.T) {
	store := newTraversalStorage(t)
	if _, err := store.LoadRunSync("../../../../etc/aden/database.yaml"); err == nil {
		t.Fatal("LoadRunSync accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalProtection::test_blocks_arbitrary_write_via_save_sync
func TestPathTraversalBlocksArbitraryWriteViaSaveSync(t *testing.T) {
	store := newTraversalStorage(t)
	run := &schemas.Run{ID: "../../var/www/html/shell", GoalID: "test", GoalDescription: "", InputData: map[string]any{}}
	if err := store.SaveRunSync(run); err == nil {
		t.Fatal("SaveRunSync accepted traversal run id")
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalWithActualFiles::test_cannot_escape_storage_directory
func TestPathTraversalCannotEscapeStorageDirectory(t *testing.T) {
	tmp := t.TempDir()
	storageDir := filepath.Join(tmp, "storage")
	if err := os.Mkdir(storageDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := filepath.Join(tmp, "secret.txt")
	if err := os.WriteFile(secret, []byte("SENSITIVE_DATA"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	store := NewDefaultConcurrentStorage(storageDir)
	if _, err := store.LoadRunSync("../secret"); err == nil {
		t.Fatal("LoadRunSync accepted traversal run id")
	}

	data, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(data) != "SENSITIVE_DATA" {
		t.Fatalf("secret file changed: %q", string(data))
	}
}

// Parity: tests/test_path_traversal_fix.py::TestPathTraversalWithActualFiles::test_save_and_load_roundtrip
func TestPathTraversalSaveAndLoadRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	store := NewDefaultConcurrentStorage(tmp)
	run := &schemas.Run{
		ID:              "run_test_123",
		GoalID:          "goal_abc",
		GoalDescription: "Integration test",
		InputData:       map[string]any{},
	}
	run.Complete(schemas.RunCompleted, "done")

	if err := store.SaveRunSync(run); err != nil {
		t.Fatalf("SaveRunSync: %v", err)
	}

	loaded, err := store.LoadRunSync("run_test_123")
	if err != nil {
		t.Fatalf("LoadRunSync: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadRunSync returned nil")
	}
	if loaded.ID != "run_test_123" {
		t.Fatalf("id = %q, want run_test_123", loaded.ID)
	}
	if loaded.Status != schemas.RunCompleted {
		t.Fatalf("status = %q, want %q", loaded.Status, schemas.RunCompleted)
	}

	runFile := filepath.Join(tmp, "runs", "run_test_123.json")
	if _, err := os.Stat(runFile); err != nil {
		t.Fatalf("run file missing at %s: %v", runFile, err)
	}
}

// Parity: tests/test_path_traversal_fix.py::TestSessionStorePathTraversal::test_valid_session_id
func TestPathTraversalSessionStoreValidSessionID(t *testing.T) {
	store := NewSessionStore(t.TempDir())
	path, err := store.GetSessionPath("session_20260206_143022_abc12345")
	if err != nil {
		t.Fatalf("GetSessionPath: %v", err)
	}
	if filepath.Base(path) != "session_20260206_143022_abc12345" {
		t.Fatalf("path base = %q", filepath.Base(path))
	}
}

// Parity: tests/test_path_traversal_fix.py::TestSessionStorePathTraversal::test_blocks_parent_traversal
func TestPathTraversalSessionStoreBlocksParentTraversal(t *testing.T) {
	store := NewSessionStore(t.TempDir())
	_, err := store.GetSessionPath("../../etc/passwd")
	if err == nil {
		t.Fatal("GetSessionPath accepted traversal session id")
	}
	if !strings.Contains(err.Error(), "Invalid session ID") {
		t.Fatalf("error = %q, want Invalid session ID", err.Error())
	}
}

// Parity: tests/test_path_traversal_fix.py::TestSessionStorePathTraversal::test_delete_session_blocks_traversal
func TestPathTraversalSessionStoreDeleteSessionBlocksTraversal(t *testing.T) {
	store := NewSessionStore(t.TempDir())
	_, err := store.DeleteSession("../../package")
	if err == nil {
		t.Fatal("DeleteSession accepted traversal session id")
	}
	if !strings.Contains(err.Error(), "Invalid session ID") {
		t.Fatalf("error = %q, want Invalid session ID", err.Error())
	}
}
