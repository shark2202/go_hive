package tasks

// Parity: core/framework/tasks/reminders.py::_save_state
// Parity: core/framework/tasks/reminders.py::_env_int

import (
	"os"
	"runtime"
	"testing"
)

// Parity-exempt: 0600 reminder_state.json mirrors framework/utils/io.py::atomic_write; no upstream pytest asserts the mode.
func TestReminderStateFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not model POSIX file modes")
	}
	store := reminderTaskStore(t)
	state := NewReminderState()
	state.TurnsTotal = 4
	saveReminderStateFor(store, "s1", state)

	path, ok := reminderStatePathFor(store, "s1")
	if !ok {
		t.Fatal("reminder state path not resolved")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	// Python writes through atomic_write (mkstemp -> 0600), and the counter
	// carries per-session review behaviour, so it must not be group/world
	// readable.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("reminder_state.json permission = %#o, want 0600", perm)
	}
}

// Parity-exempt: Go envInt matches Python int(); no upstream pytest exercises reminders._env_int parsing.
func TestEnvIntMatchesPythonIntSemantics(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		def  int
		want int
	}{
		{"plain", "42", 7, 42},
		{"surrounding whitespace", " 42 \t", 7, 42},
		{"digit-group underscores", "1_000", 7, 1000},
		{"signed with underscores", "+1_0", 7, 10},
		{"negative with underscores", "-1_0", 7, -10},
		{"leading underscore rejected", "_10", 7, 7},
		{"trailing underscore rejected", "10_", 7, 7},
		{"double underscore rejected", "1__0", 7, 7},
		{"inner whitespace rejected", "4 2", 7, 7},
		{"empty falls back", "", 7, 7},
		{"blank falls back", "   ", 7, 7},
		{"float text rejected", "12.5", 7, 7},
		{"hex text rejected", "0x10", 7, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HIVE_TEST_REMINDER_TURNS", tc.raw)
			if got := envInt("HIVE_TEST_REMINDER_TURNS", tc.def); got != tc.want {
				t.Fatalf("envInt(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
