package channels

// Python entry-point channels: discovery through importlib.metadata, the
// bridge process, host calls, outputs, stop, and the failure modes. Every
// fixture is a temp package on PYTHONPATH; no third-party plugin or network is
// involved.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const fakePythonChannelSource = `import json
import os

RECORD = os.environ.get("W158_RECORD", "")


def record(event, **fields):
    if not RECORD:
        return
    fields["record"] = event
    with open(RECORD, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(fields, default=str) + "\n")


class FakeChannel:
    def __init__(self, host):
        self.host = host

    async def start(self):
        print("fake channel stdout must become a log line")
        record(
            "start",
            name=self.host.name,
            config=self.host.config,
            state_dir=str(self.host.state_dir),
            token=os.environ.get("W158_TOKEN"),
        )
        session = await self.host.open_session("queen_test", title="From Python")
        record("opened", session=session)
        resumed = await self.host.open_session("queen_test", session_id="session_prev")
        record("resumed", session=resumed)
        self.host.subscribe(session, self.on_output)
        self.host.set_title(session, "Renamed")
        sent = await self.host.send(session, "hello from python", display_text="shown")
        record("sent", session=sent)
        record("judged", probability=await self.host.judge("is it?", {"answer": "yes"}))
        try:
            await self.host.send("missing_session", "boom")
        except Exception as error:
            record(
                "refused",
                type=type(error).__name__,
                message=str(error),
                kind=getattr(error, "kind", None),
            )
        record("started")

    async def on_output(self, output):
        record(
            "output",
            kind=output.kind,
            text=output.text,
            session=output.session_id,
            files=[str(path) for path in output.files],
            questions=output.questions,
        )

    async def stop(self):
        record("stop")


def Factory(host):
    return FakeChannel(host)
`

const hangingStopPythonSource = `import json
import os

RECORD = os.environ.get("W158_RECORD", "")


def record(event, **fields):
    if not RECORD:
        return
    fields["record"] = event
    with open(RECORD, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(fields) + "\n")


class HangingChannel:
    def __init__(self, host):
        self.host = host

    async def start(self):
        record("start")

    async def stop(self):
        record("stop")
        import asyncio

        await asyncio.Event().wait()


def Factory(host):
    return HangingChannel(host)
`

// requirePython3 resolves a Python 3 interpreter for the fixtures, skipping
// the test when the machine has none. The bridge itself supports Python 3.9+.
//
// Parity: core/framework/server/channels.py::start_channels
func requirePython3(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"python3", "python"} {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		output, err := exec.Command(path, "-c", "import sys; print(sys.version_info[0])").Output()
		if err == nil && strings.TrimSpace(string(output)) == "3" {
			return path
		}
	}
	t.Skip("no Python 3 interpreter on PATH")
	return ""
}

// fakePythonSite is one temp distribution exposing a hive.channels entry point.
type fakePythonSite struct {
	Root       string
	EntryPoint string
}

// writeFakePythonPackage lays out what importlib.metadata scans for: an
// importable module plus a .dist-info with entry_points.txt. Nothing is
// installed and nothing leaves the temp dir.
func writeFakePythonPackage(t *testing.T, entryPoint, module, source string) fakePythonSite {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, module+".py"), []byte(source), 0o644); err != nil {
		t.Fatalf("write %s.py: %v", module, err)
	}
	distInfo := filepath.Join(root, module+"-0.1.0.dist-info")
	if err := os.MkdirAll(distInfo, 0o755); err != nil {
		t.Fatalf("create dist-info: %v", err)
	}
	metadata := "Metadata-Version: 2.1\nName: " + module + "\nVersion: 0.1.0\n"
	if err := os.WriteFile(filepath.Join(distInfo, "METADATA"), []byte(metadata), 0o644); err != nil {
		t.Fatalf("write METADATA: %v", err)
	}
	entryPoints := "[hive.channels]\n" + entryPoint + " = " + module + ":Factory\n"
	if err := os.WriteFile(filepath.Join(distInfo, "entry_points.txt"), []byte(entryPoints), 0o644); err != nil {
		t.Fatalf("write entry_points.txt: %v", err)
	}
	return fakePythonSite{Root: root, EntryPoint: entryPoint}
}

// pythonSection builds an enabled section for one fixture channel.
func pythonSection(t *testing.T, site fakePythonSite, record string, extra map[string]any) map[string]any {
	t.Helper()
	section := map[string]any{
		"enabled":     true,
		"entry_point": site.EntryPoint,
		"interpreter": []any{requirePython3(t)},
		"python_path": []any{site.Root},
		"env":         []any{"W158_RECORD=" + record, "W158_TOKEN=from-section"},
	}
	for key, value := range extra {
		section[key] = value
	}
	return section
}

func hasRecord(records []map[string]any, kind string) bool {
	for _, record := range records {
		if record["record"] == kind {
			return true
		}
	}
	return false
}

func pythonOutputRecords(records []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record["record"] == "output" {
			out = append(out, record)
		}
	}
	return out
}

func waitOutputRecords(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	var outputs []map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		outputs = pythonOutputRecords(readRecords(t, path))
		if len(outputs) >= want {
			return outputs
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d output records in %s; have %#v", want, path, outputs)
	return outputs
}

// TestPythonEntryPointDiscovery covers importlib.metadata discovery in the
// configured interpreter, including the bridge's --list output shape.
//
// Parity: core/framework/server/channels.py::_installed
func TestPythonEntryPointDiscovery(t *testing.T) {
	interpreter := requirePython3(t)
	site := writeFakePythonPackage(t, "w158fake", "w158fake", fakePythonChannelSource)
	// An entry point in another group must not leak into hive.channels.
	otherGroup := filepath.Join(site.Root, "w158other-0.1.0.dist-info")
	if err := os.MkdirAll(otherGroup, 0o755); err != nil {
		t.Fatalf("create other dist-info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherGroup, "entry_points.txt"),
		[]byte("[other.channels]\nw158other = w158fake:Factory\n"), 0o644); err != nil {
		t.Fatalf("write other entry_points.txt: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	entries, err := DiscoverEntryPoints(ctx, []string{interpreter}, []string{site.Root})
	if err != nil {
		t.Fatalf("DiscoverEntryPoints: %v", err)
	}
	var found *EntryPointInfo
	for index := range entries {
		if entries[index].Name == "w158fake" {
			found = &entries[index]
		}
		if entries[index].Name == "w158other" {
			t.Fatalf("discovery crossed entry-point groups: %#v", entries)
		}
	}
	if found == nil {
		t.Fatalf("discovery did not see w158fake: %#v", entries)
	}
	if found.Value != "w158fake:Factory" {
		t.Fatalf("entry point value = %q, want w158fake:Factory", found.Value)
	}

	// A missing interpreter is a discovery error, not an empty success.
	if _, err := DiscoverEntryPoints(ctx, []string{filepath.Join(t.TempDir(), "no-python")}, nil); err == nil {
		t.Fatal("discovery with a missing interpreter succeeded")
	}
}

// TestPythonBridgeRunsAnInstalledChannel is the end-to-end contract: factory
// (host), config/env/state_dir, open/send/subscribe/set_title/judge, output
// delivery, ChannelError framing and stop.
//
// Parity: core/framework/server/channels.py::start_channels
func TestPythonBridgeRunsAnInstalledChannel(t *testing.T) {
	site := writeFakePythonPackage(t, "w158fake", "w158fake", fakePythonChannelSource)
	record := filepath.Join(t.TempDir(), "python.jsonl")
	report := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(report, []byte("report"), 0o644); err != nil {
		t.Fatalf("write report: %v", err)
	}

	backend := newFakeBackend()
	judge := 0.75
	backend.judge = &judge
	config := map[string]any{"channels": map[string]any{
		"pychat": pythonSection(t, site, record, map[string]any{"marker": "kept", "record_file": record}),
	}}
	var logMu sync.Mutex
	var logs []string
	host := startFakeHost(t, config, backend, func(options *HostOptions) {
		options.Logf = func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			logMu.Lock()
			logs = append(logs, line)
			logMu.Unlock()
			t.Log(line)
		}
	})
	if state := host.States()["pychat"]; state["state"] != "running" {
		t.Fatalf("state = %#v, want running", state)
	}
	waitFor(t, 3*time.Second, "the plugin's stdout line in the host log", func() bool {
		logMu.Lock()
		defer logMu.Unlock()
		for _, line := range logs {
			if strings.Contains(line, "fake channel stdout must become a log line") {
				return true
			}
		}
		return false
	})

	waitFor(t, 5*time.Second, "the python channel to subscribe", func() bool {
		return backend.subscriptionCount() == 1
	})
	backend.emit("session_new", Output{SessionID: "session_new", Kind: OutputMessage, Text: "hello back"})
	backend.emit("session_new", Output{
		SessionID: "session_new",
		Kind:      OutputFiles,
		Files:     []string{report},
	})
	backend.emit("session_new", Output{SessionID: "session_new", Kind: OutputIdle})

	records := waitRecords(t, record, 9)
	start := recordByKind(t, records, "start")
	if start["name"] != "pychat" {
		t.Fatalf("host.name = %v, want pychat", start["name"])
	}
	pluginConfig, _ := start["config"].(map[string]any)
	if pluginConfig["marker"] != "kept" {
		t.Fatalf("host.config did not carry the section: %#v", pluginConfig)
	}
	if start["token"] != "from-section" {
		t.Fatalf("section env did not reach the plugin: %v", start["token"])
	}
	stateDir, _ := start["state_dir"].(string)
	if info, err := os.Stat(stateDir); err != nil || !info.IsDir() {
		t.Fatalf("host.state_dir %q not created: %v", stateDir, err)
	}
	opened := recordByKind(t, records, "opened")
	if opened["session"] != "session_new" {
		t.Fatalf("open_session result = %#v", opened)
	}
	resumed := recordByKind(t, records, "resumed")
	if resumed["session"] != "session_prev" {
		t.Fatalf("open_session(session_id=...) result = %#v", resumed)
	}
	backend.mu.Lock()
	var openCalls []fakeCall
	for _, call := range backend.calls {
		if call.Method == "open_session" {
			openCalls = append(openCalls, call)
		}
	}
	backend.mu.Unlock()
	if len(openCalls) != 2 || openCalls[1].SessionID != "session_prev" {
		t.Fatalf("backend open_session calls = %#v", openCalls)
	}
	backend.mu.Lock()
	calls := append([]fakeCall(nil), backend.calls...)
	backend.mu.Unlock()
	subscribeIndex, sendIndex := -1, -1
	for index, call := range calls {
		if call.Method == "subscribe" && subscribeIndex < 0 {
			subscribeIndex = index
		}
		if call.Method == "send" && sendIndex < 0 {
			sendIndex = index
		}
	}
	if subscribeIndex < 0 || sendIndex < 0 || subscribeIndex > sendIndex {
		t.Fatalf("subscribe must reach the host before send: %#v", calls)
	}
	sent := recordByKind(t, records, "sent")
	if sent["session"] != "session_new" {
		t.Fatalf("send result = %#v", sent)
	}
	judged := recordByKind(t, records, "judged")
	if judged["probability"] != 0.75 {
		t.Fatalf("judge result = %#v", judged)
	}
	refused := recordByKind(t, records, "refused")
	if refused["type"] != "ChannelError" || refused["kind"] != "no_session" ||
		!strings.Contains(refused["message"].(string), "No session 'missing_session'") {
		t.Fatalf("ChannelError framing = %#v", refused)
	}
	recordByKind(t, records, "started")

	sendCall, _ := backend.callFor("send")
	if sendCall.Text != "hello from python" || sendCall.DisplayText != "shown" {
		t.Fatalf("backend send = %#v", sendCall)
	}
	openCall, _ := backend.callFor("open_session")
	if openCall.QueenID != "queen_test" || openCall.Title != "From Python" {
		t.Fatalf("backend open_session = %#v", openCall)
	}
	titleCall, _ := backend.callFor("set_title")
	if titleCall.Title != "Renamed" {
		t.Fatalf("backend set_title = %#v", titleCall)
	}
	judgeCall, _ := backend.callFor("judge")
	if judgeCall.Question != "is it?" || judgeCall.State["answer"] != "yes" {
		t.Fatalf("backend judge = %#v", judgeCall)
	}

	outputs := waitOutputRecords(t, record, 3)
	if outputs[0]["kind"] != "message" || outputs[0]["text"] != "hello back" {
		t.Fatalf("message output = %#v", outputs[0])
	}
	files, _ := outputs[1]["files"].([]any)
	if len(files) != 1 || files[0] != report {
		t.Fatalf("files output = %#v", outputs[1])
	}
	if outputs[2]["kind"] != "idle" {
		t.Fatalf("idle output = %#v", outputs[2])
	}

	host.Stop()
	waitFor(t, 3*time.Second, "channel.stop()", func() bool {
		return hasRecord(readRecords(t, record), "stop")
	})
}

// TestPythonBridgeReportsMissingEntryPoint reports an installed-entry-point
// miss as a failed channel with upstream's LookupError wording.
//
// Parity: core/framework/server/channels.py::start_channels
func TestPythonBridgeReportsMissingEntryPoint(t *testing.T) {
	site := writeFakePythonPackage(t, "w158fake", "w158fake", fakePythonChannelSource)
	section := pythonSection(t, site, filepath.Join(t.TempDir(), "unused.jsonl"), nil)
	section["entry_point"] = "not-installed"
	config := map[string]any{"channels": map[string]any{"pychat": section}}
	host := startFakeHost(t, config, newFakeBackend(), nil)

	state := host.States()["pychat"]
	if state["state"] != "failed" {
		t.Fatalf("state = %#v, want failed", state)
	}
	message, _ := state["error"].(string)
	if !strings.Contains(message, "LookupError") ||
		!strings.Contains(message, "no installed 'hive.channels' entry point named 'not-installed'") {
		t.Fatalf("error = %q", message)
	}
}

// TestPythonBridgeReportsBrokenEntryPoint surfaces a factory import failure as
// a failed state instead of a silent off channel.
//
// Parity: core/framework/server/channels.py::start_channels
func TestPythonBridgeReportsBrokenEntryPoint(t *testing.T) {
	site := writeFakePythonPackage(t, "w158broken", "w158broken", `raise RuntimeError("import blew up")`)
	config := map[string]any{"channels": map[string]any{
		"pychat": pythonSection(t, site, filepath.Join(t.TempDir(), "unused.jsonl"), nil),
	}}
	host := startFakeHost(t, config, newFakeBackend(), nil)

	state := host.States()["pychat"]
	if state["state"] != "failed" {
		t.Fatalf("state = %#v, want failed", state)
	}
	if message, _ := state["error"].(string); !strings.Contains(message, "RuntimeError: import blew up") {
		t.Fatalf("error = %q", message)
	}
}

// TestPythonBridgeStopBudgetKillsAHangingChannel keeps the stop budget bounded
// when a channel ignores stop().
//
// Parity: core/framework/server/channels.py::stop_channels
func TestPythonBridgeStopBudgetKillsAHangingChannel(t *testing.T) {
	site := writeFakePythonPackage(t, "w158hang", "w158hang", hangingStopPythonSource)
	record := filepath.Join(t.TempDir(), "hang.jsonl")
	config := map[string]any{"channels": map[string]any{
		"pychat": pythonSection(t, site, record, nil),
	}}
	host := startFakeHost(t, config, newFakeBackend(), func(options *HostOptions) {
		options.StopTimeout = 200 * time.Millisecond
	})
	if state := host.States()["pychat"]; state["state"] != "running" {
		t.Fatalf("state = %#v, want running", state)
	}
	recordByKind(t, waitRecords(t, record, 1), "start")

	started := time.Now()
	host.Stop()
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("Stop took %s with a 200ms budget", elapsed)
	}
	waitFor(t, 3*time.Second, "the hanging channel's stop record", func() bool {
		return hasRecord(readRecords(t, record), "stop")
	})
	host.Stop()
}

// TestPythonBridgeConfigIsOptIn keeps the stdio contract untouched: a command
// section behaves exactly as before, entry_point selects the bridge, and mixed
// or misplaced fields fail loudly.
//
// Parity: core/framework/server/channels.py::start_channels
func TestPythonBridgeConfigIsOptIn(t *testing.T) {
	plugins := Plugins(map[string]any{"channels": map[string]any{
		"stdio": map[string]any{"enabled": true, "command": []any{"/bin/std"}},
		"py": map[string]any{
			"enabled":     true,
			"entry_point": "w158fake",
			"interpreter": "python3",
			"python_path": []any{"/tmp/site"},
		},
		"both":        map[string]any{"enabled": true, "command": "/bin/std", "entry_point": "w158fake"},
		"badtype":     map[string]any{"enabled": true, "entry_point": 3},
		"empty":       map[string]any{"enabled": true, "entry_point": "  "},
		"orphaninter": map[string]any{"enabled": true, "command": "/bin/std", "interpreter": "python3"},
		"orphanpath":  map[string]any{"enabled": true, "command": "/bin/std", "python_path": []any{"/tmp"}},
		"args":        map[string]any{"enabled": true, "entry_point": "w158fake", "args": []any{"-x"}},
	}})
	byName := map[string]PluginConfig{}
	for _, plugin := range plugins {
		byName[plugin.Name] = plugin
	}

	stdio := byName["stdio"]
	if stdio.EntryPoint != "" || stdio.ConfigError != "" || strings.Join(stdio.Argv(), "|") != "/bin/std" {
		t.Fatalf("stdio plugin changed: %#v", stdio)
	}
	python := byName["py"]
	if python.EntryPoint != "w158fake" || python.ConfigError != "" {
		t.Fatalf("python plugin = %#v", python)
	}
	if strings.Join(python.Interpreter, "|") != "python3" || strings.Join(python.PythonPath, "|") != "/tmp/site" {
		t.Fatalf("python plumbing = %#v", python)
	}
	for name, want := range map[string]string{
		"both":        "mutually exclusive",
		"badtype":     "must be a string",
		"empty":       "entry_point is empty",
		"orphaninter": "interpreter is only valid with entry_point",
		"orphanpath":  "python_path is only valid with entry_point",
		"args":        "args is only valid with command",
	} {
		if plugin := byName[name]; !strings.Contains(plugin.ConfigError, want) {
			t.Fatalf("%s.ConfigError = %q, want %q", name, plugin.ConfigError, want)
		}
	}
}

// TestStdioAndPythonChannelsRunSideBySide is the regression guard: adding the
// bridge leaves the existing stdio plugin contract working, in the same host.
//
// Parity-exempt: the stdio plugin path is the Go extension; upstream has only
// in-process channels.
func TestStdioAndPythonChannelsRunSideBySide(t *testing.T) {
	site := writeFakePythonPackage(t, "w158fake", "w158fake", fakePythonChannelSource)
	pythonRecord := filepath.Join(t.TempDir(), "python-side.jsonl")
	stdioRecord := filepath.Join(t.TempDir(), "stdio-side.jsonl")
	config := map[string]any{"channels": map[string]any{
		"pychat": pythonSection(t, site, pythonRecord, nil),
		"stdio":  fakeSection("recorder", stdioRecord, map[string]any{"subscribe_session": "session_1"}),
	}}
	host := startFakeHost(t, config, newFakeBackend(), nil)

	states := host.States()
	if states["pychat"]["state"] != "running" || states["stdio"]["state"] != "running" {
		t.Fatalf("states = %#v", states)
	}
	stdioRecords := waitRecords(t, stdioRecord, 1)
	if recordByKind(t, stdioRecords, "hello") == nil {
		t.Fatal("stdio plugin did not greet")
	}
	waitRecords(t, pythonRecord, 1)
	if recordByKind(t, readRecords(t, pythonRecord), "start") == nil {
		t.Fatal("python plugin did not start")
	}
}
