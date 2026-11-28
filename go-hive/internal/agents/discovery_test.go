package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func writeDiscoveryFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins colony grouping, metadata, worker parsing, counts and exclusion rules.
func TestDiscoverAgentsReadsColonies(t *testing.T) {
	root := t.TempDir()
	colony := filepath.Join(root, "my_colony")
	mustMkdir(t, colony)
	writeDiscoveryFile(t, colony, "metadata.json",
		`{"queen_name":"queen_alpha","created_at":"2026-01-02T03:04:05+00:00","icon":"crown"}`)
	writeDiscoveryFile(t, colony, "first.json",
		`{"name":"first","description":"first worker","tools":["a","b"],"goal":{"description":"do it"},"spawned_at":"2026-01-01T00:00:00"}`)
	writeDiscoveryFile(t, colony, "second.json",
		`{"name":"second","description":"","tools":["a","c","d"],"goal":{"description":"later"}}`)
	writeDiscoveryFile(t, colony, "agent.json", `{"name":"ignored","tools":["x"]}`)
	writeDiscoveryFile(t, colony, "notes.txt", "ignored")
	mustMkdir(t, filepath.Join(colony, "workers", "session_one"))
	mustMkdir(t, filepath.Join(colony, "workers", "session_two"))
	mustMkdir(t, filepath.Join(colony, "queens", "q1", "sessions", "s1"))
	writeDiscoveryFile(t, colony, "workers/session_one/state.json",
		`{"timestamps":{"updated_at":"2026-02-01T00:00:00"}}`)
	writeDiscoveryFile(t, colony, "workers/session_one/ws/runs.jsonl", "{\"run_id\":\"r1\"}\n{\"run_id\":\"r2\"}\n\n")
	writeDiscoveryFile(t, colony, "workers/session_one/ws/deep/runs.jsonl", "{\"run_id\":\"r2\"}\n")
	queenSession := filepath.Join(colony, "queens", "q1", "sessions", "s1")
	old := time.Unix(1577836800, 0)
	if err := os.Chtimes(queenSession, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	mustMkdir(t, filepath.Join(root, "not_a_colony"))

	groups := DiscoverAgents(root)
	entries, ok := groups["Your Agents"]
	if !ok || len(entries) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
	entry := entries[0]
	if entry.Name != "My Colony" {
		t.Fatalf("name = %q", entry.Name)
	}
	if entry.Description != "first worker" {
		t.Fatalf("description = %q", entry.Description)
	}
	if entry.Category != "Your Agents" {
		t.Fatalf("category = %q", entry.Category)
	}
	if entry.NodeCount != 2 || entry.ToolCount != 3 {
		t.Fatalf("node/tool = %d/%d", entry.NodeCount, entry.ToolCount)
	}
	if entry.SessionCount != 3 || entry.RunCount != 2 {
		t.Fatalf("sessions/runs = %d/%d", entry.SessionCount, entry.RunCount)
	}
	if entry.CreatedAt != "2026-01-02T03:04:05+00:00" || entry.Icon != "crown" {
		t.Fatalf("created_at/icon = %#v/%#v", entry.CreatedAt, entry.Icon)
	}
	if entry.LastActive == nil || *entry.LastActive != "2026-02-01T00:00:00" {
		t.Fatalf("last_active = %#v", entry.LastActive)
	}
	wantPath, err := ResolvePath(colony)
	if err != nil {
		t.Fatalf("resolve colony: %v", err)
	}
	if entry.Path != wantPath {
		t.Fatalf("path = %q, want %q", entry.Path, wantPath)
	}
	if len(entry.Workers) != 2 {
		t.Fatalf("workers = %d", len(entry.Workers))
	}
	first := entry.Workers[0]
	if first.Name != "first" || first.Description != "first worker" || first.ToolCount != 2 {
		t.Fatalf("first worker = %#v", first)
	}
	if first.Task != "do it" || first.SpawnedAt != "2026-01-01T00:00:00" {
		t.Fatalf("first worker task = %#v", first)
	}
	if first.QueenName != "queen_alpha" || first.ColonyID != "my_colony" {
		t.Fatalf("first worker provenance = %#v", first)
	}
	if filepath.Base(first.ConfigPath) != "first.json" {
		t.Fatalf("config_path = %q", first.ConfigPath)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the soft-delete skip and the empty result when the base dir is missing.
func TestDiscoverAgentsSkipsDeletedAndMissingDir(t *testing.T) {
	root := t.TempDir()
	deleted := filepath.Join(root, "gone")
	mustMkdir(t, deleted)
	writeDiscoveryFile(t, deleted, "metadata.json", `{"deleted":true}`)
	writeDiscoveryFile(t, deleted, "worker.json", `{"name":"w","tools":[]}`)

	if groups := DiscoverAgents(root); len(groups) != 0 {
		t.Fatalf("deleted colony was discovered: %#v", groups)
	}
	if groups := DiscoverAgents(filepath.Join(root, "missing")); len(groups) != 0 {
		t.Fatalf("missing root produced entries: %#v", groups)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the dir-name title fallback and the birthtime created_at fallback.
func TestDiscoverAgentsFallsBackToDirNameAndBirthTime(t *testing.T) {
	root := t.TempDir()
	colony := filepath.Join(root, "plain_agent")
	mustMkdir(t, colony)
	writeDiscoveryFile(t, colony, "metadata.json", `{"created_at":""}`)
	writeDiscoveryFile(t, colony, "worker.json", `{"name":"w"}`)

	entries := DiscoverAgents(root)["Your Agents"]
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].Name != "Plain Agent" {
		t.Fatalf("name = %q", entries[0].Name)
	}
	if entries[0].ToolCount != 0 || entries[0].NodeCount != 1 {
		t.Fatalf("node/tool = %d/%d", entries[0].NodeCount, entries[0].ToolCount)
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if _, ok := entries[0].CreatedAt.(string); !ok {
			t.Fatalf("created_at fallback = %#v", entries[0].CreatedAt)
		}
	} else if entries[0].CreatedAt != nil {
		t.Fatalf("created_at on %s = %#v, want nil", runtime.GOOS, entries[0].CreatedAt)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the len() quirk that drops null tools/goals but counts strings and objects.
func TestDiscoverAgentsSkipsMalformedWorkerConfigs(t *testing.T) {
	root := t.TempDir()
	colony := filepath.Join(root, "quirky")
	mustMkdir(t, colony)
	writeDiscoveryFile(t, colony, "bad_goal.json", `{"name":"bad_goal","goal":null,"tools":[]}`)
	writeDiscoveryFile(t, colony, "bad_tools.json", `{"name":"bad_tools","tools":null}`)
	writeDiscoveryFile(t, colony, "obj_tools.json", `{"name":"obj_tools","tools":{"a":1,"b":2}}`)
	writeDiscoveryFile(t, colony, "str_tools.json", `{"name":"str_tools","tools":"abc"}`)
	writeDiscoveryFile(t, colony, "broken.json", `not json`)

	entries := DiscoverAgents(root)["Your Agents"]
	if len(entries) != 1 {
		t.Fatalf("entries = %#v", entries)
	}
	if entries[0].NodeCount != 2 || entries[0].ToolCount != 3 {
		t.Fatalf("node/tool = %d/%d", entries[0].NodeCount, entries[0].ToolCount)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins worker-plus-queen session counting.
func TestCountSessionsCountsWorkerAndQueenDirs(t *testing.T) {
	colony := t.TempDir()
	mustMkdir(t, filepath.Join(colony, "workers", "a"))
	mustMkdir(t, filepath.Join(colony, "workers", "b"))
	writeDiscoveryFile(t, colony, "workers/plain.txt", "not a dir")
	mustMkdir(t, filepath.Join(colony, "queens", "q", "sessions", "s1"))
	mustMkdir(t, filepath.Join(colony, "queens", "q", "sessions", "s2"))
	mustMkdir(t, filepath.Join(colony, "queens", "q", "other"))

	if got := CountSessions(colony); got != 4 {
		t.Fatalf("CountSessions = %d, want 4", got)
	}
	if got := CountSessions(filepath.Join(colony, "missing")); got != 0 {
		t.Fatalf("missing CountSessions = %d", got)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the session_ prefix filter, recursive runs.jsonl search and unique run_ids.
func TestCountRunsUniqueRunIDs(t *testing.T) {
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "workers/session_a/runs.jsonl", "{\"run_id\":\"r1\"}\n{\"run_id\":\"r2\"}\n")
	writeDiscoveryFile(t, colony, "workers/session_a/deep/runs.jsonl", "{\"run_id\":\"r2\"}\n{\"run_id\":\"r3\"}\n")
	writeDiscoveryFile(t, colony, "workers/not_a_session/runs.jsonl", "{\"run_id\":\"r4\"}\n")
	writeDiscoveryFile(t, colony, "workers/session_b/runs.jsonl", "{\"run_id\":0}\n\n")

	if got := CountRuns(colony); got != 3 {
		t.Fatalf("CountRuns = %d, want 3", got)
	}
	if got := CountRuns(filepath.Join(colony, "missing")); got != 0 {
		t.Fatalf("missing CountRuns = %d", got)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the
// exact run-event record shape execution_manager._write_run_event appends, so
// Go keeps reading Python telemetry after a runtime switch.
func TestCountRunsReadsPythonRunEventRecords(t *testing.T) {
	colony := t.TempDir()
	records := "" +
		`{"run_id": "run_py_1", "event": "run_started", "timestamp": "2026-11-25T10:00:00.123456", "created_at": 1764064800.123456}` + "\n" +
		`{"run_id": "run_py_1", "event": "run_completed", "timestamp": "2026-11-25T10:00:05.654321", "created_at": 1764064805.654321}` + "\n" +
		`{"run_id": "run_py_2", "event": "run_failed", "timestamp": "2026-11-25T10:01:00", "created_at": 1764064860, "error": "boom"}` + "\n"
	writeDiscoveryFile(t, colony, "workers/session_one/ws/runs.jsonl", records)

	if got := CountRuns(colony); got != 2 {
		t.Fatalf("CountRuns = %d, want 2", got)
	}
}

// Parity-exempt: negative regression for the session_* filter the Python
// scanner applies; upstream has no test for a non-session worker directory.
// The picture only counts directories named session_*, so a runs.jsonl under a
// Go worker_* dir (or any other name) is not counted and cannot inflate the
// Python-facing run count.
func TestCountRunsIgnoresNonSessionDirectories(t *testing.T) {
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "workers/worker_abc/runs.jsonl", `{"run_id": "run_go_1"}`+"\n")
	writeDiscoveryFile(t, colony, "workers/session_one/runs.jsonl", `{"run_id": "run_py_1"}`+"\n")

	if got := CountRuns(colony); got != 1 {
		t.Fatalf("CountRuns = %d, want 1 (only session_one)", got)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the lexicographic max of state.json timestamps over queen-dir mtimes.
func TestLastActivePicksLatestTimestamp(t *testing.T) {
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "workers/s1/state.json", `{"timestamps":{"updated_at":"2026-02-01T00:00:00"}}`)
	writeDiscoveryFile(t, colony, "workers/s2/state.json", `{"timestamps":{"updated_at":"2026-03-01T00:00:00"}}`)
	writeDiscoveryFile(t, colony, "workers/s3/state.json", `{"timestamps":null}`)
	writeDiscoveryFile(t, colony, "workers/s4/state.json", `not json`)
	queenSession := filepath.Join(colony, "queens", "q", "sessions", "s1")
	mustMkdir(t, queenSession)
	old := time.Unix(1577836800, 0)
	if err := os.Chtimes(queenSession, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	latest := LastActive(colony)
	if latest == nil || *latest != "2026-03-01T00:00:00" {
		t.Fatalf("LastActive = %#v", latest)
	}
	if got := LastActive(filepath.Join(colony, "missing")); got != nil {
		t.Fatalf("missing LastActive = %#v", got)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the mtime branch format when no worker timestamp exists.
func TestLastActiveUsesQueenSessionMTime(t *testing.T) {
	colony := t.TempDir()
	sessionDir := filepath.Join(colony, "queens", "q", "sessions", "s1")
	mustMkdir(t, sessionDir)
	stamp := time.Unix(1700000000, 0)
	if err := os.Chtimes(sessionDir, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	latest := LastActive(colony)
	if latest == nil {
		t.Fatal("LastActive returned nil")
	}
	if *latest != isoformatLocal(stamp) {
		t.Fatalf("LastActive = %q, want %q", *latest, isoformatLocal(stamp))
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins sorted order, the excluded stems and the case-sensitive .json suffix.
func TestFindWorkerConfigsFiltersStems(t *testing.T) {
	colony := t.TempDir()
	for _, name := range []string{"b_worker.json", "a_worker.json", "metadata.json", "tasks.json", "upper.JSON", "notes.txt"} {
		writeDiscoveryFile(t, colony, name, `{}`)
	}
	mustMkdir(t, filepath.Join(colony, "sneaky.json"))

	configs := FindWorkerConfigs(colony)
	names := make([]string, 0, len(configs))
	for _, config := range configs {
		names = append(names, filepath.Base(config))
	}
	want := []string{"a_worker.json", "b_worker.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("configs = %v, want %v", names, want)
	}
	if got := FindWorkerConfigs(filepath.Join(colony, "missing")); got != nil {
		t.Fatalf("missing configs = %v", got)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins that only regular json files make a directory a colony.
func TestIsColonyDir(t *testing.T) {
	empty := t.TempDir()
	mustMkdir(t, filepath.Join(empty, "nested"))
	if IsColonyDir(empty) {
		t.Fatal("empty dir treated as colony")
	}
	excluded := t.TempDir()
	writeDiscoveryFile(t, excluded, "agent.json", `{}`)
	writeDiscoveryFile(t, excluded, "flowchart.json", `{}`)
	if IsColonyDir(excluded) {
		t.Fatal("excluded stems treated as workers")
	}
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "worker.json", `{}`)
	if !IsColonyDir(colony) {
		t.Fatal("worker config not detected")
	}
	file := filepath.Join(t.TempDir(), "file.json")
	writeDiscoveryFile(t, filepath.Dir(file), "file.json", `{}`)
	if IsColonyDir(file) {
		t.Fatal("regular file treated as colony")
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the set semantics for unique tool values across worker configs.
func TestExtractAgentStatsCountsUniqueTools(t *testing.T) {
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "one.json", `{"tools":["a","b",1]}`)
	writeDiscoveryFile(t, colony, "two.json", `{"tools":["b","c",1.0,true]}`)
	writeDiscoveryFile(t, colony, "broken.json", `{"tools":"not a list"}`)

	workers, tools, tags := ExtractAgentStats(colony)
	if workers != 3 || tools != 4 {
		t.Fatalf("workers/tools = %d/%d", workers, tools)
	}
	if tags == nil || len(tags) != 0 {
		t.Fatalf("tags = %#v", tags)
	}
	empty := t.TempDir()
	if workers, tools, tags := ExtractAgentStats(empty); workers != 0 || tools != 0 || len(tags) != 0 {
		t.Fatalf("empty stats = %d/%d/%v", workers, tools, tags)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins str.title() word boundaries, including digits splitting words.
func TestPythonTitleMatchesTitleCasing(t *testing.T) {
	cases := map[string]string{
		"my_agent":            "My_Agent",
		"2nd coLONY":          "2Nd Colony",
		"already Titled":      "Already Titled",
		"":                    "",
		"HELLO-world_again42": "Hello-World_Again42",
		"abc42def":            "Abc42Def",
		"x_y42z":              "X_Y42Z",
		"中a":                  "中A",
		"ǳa":                  "ǲa",
		"don't":               "Don'T",
	}
	for input, want := range cases {
		if got := pythonTitle(input); got != want {
			t.Fatalf("pythonTitle(%q) = %q, want %q", input, got, want)
		}
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the WorkerEntry.to_dict key set and value types.
func TestWorkerEntryToDict(t *testing.T) {
	worker := WorkerEntry{
		Name:        "w",
		ConfigPath:  "/tmp/colonies/c/w.json",
		Description: "desc",
		ToolCount:   2,
		Task:        "task",
		SpawnedAt:   "2026-01-01T00:00:00",
		QueenName:   "q",
		ColonyID:    "c",
	}
	got := worker.ToDict()
	want := map[string]any{
		"name":        "w",
		"config_path": "/tmp/colonies/c/w.json",
		"description": "desc",
		"tool_count":  2,
		"task":        "task",
		"spawned_at":  "2026-01-01T00:00:00",
		"queen_name":  "q",
		"colony_id":   "c",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToDict = %#v, want %#v", got, want)
	}
}

// Parity-exempt: framework/agents/discovery.py has no Python tests; pins the per-file abort on malformed lines, Python set equality of run_id values, and the splitlines/strip quirks.
func TestCountRunsAbortsFileOnMalformedLine(t *testing.T) {
	colony := t.TempDir()
	writeDiscoveryFile(t, colony, "workers/session_a/runs.jsonl",
		"{\"run_id\":1}\n{\"run_id\":1.0}\n{\"run_id\":true}\n")
	writeDiscoveryFile(t, colony, "workers/session_b/runs.jsonl",
		"{\"run_id\":\"x\"}\nnot json\n{\"run_id\":\"y\"}\n")
	writeDiscoveryFile(t, colony, "workers/session_c/runs.jsonl",
		"{\"run_id\":\"z\"}\n{\"run_id\":[1]}\n{\"run_id\":\"w\"}\n")
	writeDiscoveryFile(t, colony, "workers/session_d/runs.jsonl",
		"null\n{\"run_id\":\"q\"}\n")
	writeDiscoveryFile(t, colony, "workers/session_e/runs.jsonl",
		"{\"run_id\":[]}\n{\"run_id\":\"0\"}\n{\"run_id\":false}\n")
	writeDiscoveryFile(t, colony, "workers/session_f/runs.jsonl",
		"{\"run_id\":\"crlf\"}\r\n{\"run_id\":\"cr\"}\r")
	writeDiscoveryFile(t, colony, "workers/session_g/runs.jsonl",
		"{\"run_id\":\"nel\"}\u0085{\"run_id\":\"us\"}\x1f")

	if got := CountRuns(colony); got != 8 {
		t.Fatalf("CountRuns = %d, want 8", got)
	}
}
