package channels

// Host behaviour against a real re-exec plugin subprocess and an in-memory
// session backend.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCall struct {
	Method        string
	QueenID       string
	SessionID     string
	Title         string
	Text          string
	DisplayText   string
	CorrelationID string
	Images        [][]byte
	Question      string
	State         map[string]any
	Criteria      map[string]string
}

type fakeBackend struct {
	mu     sync.Mutex
	calls  []fakeCall
	subs   map[string]func(Output)
	judge  *float64
	failed *fakeFailure
}

type fakeFailure struct {
	open *ChannelError
	send *ChannelError
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{subs: map[string]func(Output){}}
}

func (b *fakeBackend) OpenSession(_ context.Context, queenID, sessionID, title string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{Method: "open_session", QueenID: queenID, SessionID: sessionID, Title: title})
	failure := b.failed
	b.mu.Unlock()
	if failure != nil && failure.open != nil {
		return "", failure.open
	}
	if sessionID == "" {
		return "session_new", nil
	}
	return sessionID, nil
}

func (b *fakeBackend) Send(_ context.Context, sessionID, text string, images [][]byte, displayText, correlationID string) (string, error) {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{
		Method: "send", SessionID: sessionID, Text: text, Images: images,
		DisplayText: displayText, CorrelationID: correlationID,
	})
	failure := b.failed
	b.mu.Unlock()
	if failure != nil && failure.send != nil {
		return "", failure.send
	}
	if sessionID == "missing_session" {
		// The Python bridge fixture sends here to observe a typed ChannelError
		// crossing the process boundary; no other test uses this id.
		return "", Errorf(KindNoSession, "No session '%s'", sessionID)
	}
	return sessionID, nil
}

func (b *fakeBackend) Subscribe(sessionID string, fn func(Output)) (func(), error) {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{Method: "subscribe", SessionID: sessionID})
	b.subs[sessionID] = fn
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		delete(b.subs, sessionID)
		b.mu.Unlock()
	}, nil
}

func (b *fakeBackend) SetTitle(_ context.Context, sessionID, title string) error {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{Method: "set_title", SessionID: sessionID, Title: title})
	b.mu.Unlock()
	return nil
}

func (b *fakeBackend) Judge(_ context.Context, question string, state map[string]any, criteria map[string]string) (*float64, error) {
	b.mu.Lock()
	b.calls = append(b.calls, fakeCall{Method: "judge", Question: question, State: state, Criteria: criteria})
	value := b.judge
	b.mu.Unlock()
	if value == nil {
		return nil, nil
	}
	return value, nil
}

func (b *fakeBackend) callFor(method string) (fakeCall, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, call := range b.calls {
		if call.Method == method {
			return call, true
		}
	}
	return fakeCall{}, false
}

func (b *fakeBackend) subscriptionCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

func (b *fakeBackend) emit(sessionID string, output Output) bool {
	b.mu.Lock()
	handler := b.subs[sessionID]
	b.mu.Unlock()
	if handler == nil {
		return false
	}
	handler(output)
	return true
}

func fakeSection(mode, recordPath string, extra map[string]any) map[string]any {
	section := map[string]any{
		"enabled":     true,
		"command":     []any{os.Args[0]},
		"mode":        mode,
		"record_file": recordPath,
	}
	for key, value := range extra {
		section[key] = value
	}
	return section
}

func startFakeHost(t *testing.T, config map[string]any, backend SessionBackend, mutate func(*HostOptions)) *Host {
	t.Helper()
	t.Setenv(fakePluginEnv, "1")
	options := HostOptions{
		Config:     config,
		StateRoot:  t.TempDir(),
		BackendFor: func(string) SessionBackend { return backend },
		Logf:       t.Logf,
	}
	if mutate != nil {
		mutate(&options)
	}
	host := NewHost(options)
	host.Start(context.Background())
	t.Cleanup(host.Stop)
	return host
}

func waitFor(t *testing.T, timeout time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func readRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			// A concurrent append can leave a torn tail; the next poll sees
			// the complete line.
			continue
		}
		out = append(out, record)
	}
	return out
}

func waitRecords(t *testing.T, path string, want int) []map[string]any {
	t.Helper()
	var records []map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records = readRecords(t, path)
		if len(records) >= want {
			return records
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d records in %s; have %d: %#v", want, path, len(records), records)
	return records
}

func recordByKind(t *testing.T, records []map[string]any, kind string) map[string]any {
	t.Helper()
	for _, record := range records {
		if record["record"] == kind {
			return record
		}
	}
	t.Fatalf("no %q record in %#v", kind, records)
	return nil
}

func outputRecords(records []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if record["record"] != "output" {
			continue
		}
		params, _ := record["params"].(map[string]any)
		out = append(out, params)
	}
	return out
}

// TestGoPluginConfigExtendsChannelsSection covers the Go-only command
// extension over upstream's enabled gate.
//
// Parity: core/framework/server/channels.py::start_channels
func TestGoPluginConfigExtendsChannelsSection(t *testing.T) {
	plugins := Plugins(map[string]any{"channels": map[string]any{
		"alpha": map[string]any{
			"enabled": true,
			"command": []any{"/bin/alpha", "--mode=one"},
			"args":    []any{"tail"},
		},
		"stringcmd": map[string]any{"enabled": "true", "command": "/bin/str", "args": []any{"-x"}},
		"bad":       map[string]any{"enabled": 1, "command": []any{"ok", 2}},
		"off":       map[string]any{"enabled": false},
		"missing":   map[string]any{},
		"scalar":    "not an object",
	}})
	if len(plugins) != 5 {
		t.Fatalf("plugins = %#v, want five object entries", plugins)
	}
	byName := map[string]PluginConfig{}
	for _, plugin := range plugins {
		byName[plugin.Name] = plugin
	}
	alpha := byName["alpha"]
	if !alpha.Enabled {
		t.Fatal("alpha.Enabled = false")
	}
	if got := strings.Join(alpha.Argv(), "|"); got != "/bin/alpha|--mode=one|tail" {
		t.Fatalf("alpha argv = %q", got)
	}
	if got := strings.Join(byName["stringcmd"].Argv(), "|"); got != "/bin/str|-x" {
		t.Fatalf("stringcmd argv = %q", got)
	}
	if byName["bad"].ConfigError == "" {
		t.Fatal("a non-string command element must set ConfigError")
	}
	if byName["off"].Enabled {
		t.Fatal("off.Enabled = true, want false")
	}
	if byName["missing"].Enabled {
		t.Fatal("missing.Enabled = true, want false")
	}
	if _, present := byName["scalar"]; present {
		t.Fatal("a non-object channel section must not be listed")
	}
	if plugins[0].Name != "alpha" {
		t.Fatalf("Plugins not sorted: first = %q", plugins[0].Name)
	}
}

// TestProtocolFramesRoundTrip covers the JSON-lines codec and the image limits.
//
// Parity-exempt: the stdio transport has no upstream counterpart.
func TestProtocolFramesRoundTrip(t *testing.T) {
	request, err := marshalRequest("7", "send", map[string]any{"text": "a\nb"})
	if err != nil {
		t.Fatalf("marshalRequest: %v", err)
	}
	if strings.Count(string(request), "\n") != 1 || !strings.HasSuffix(string(request), "\n") {
		t.Fatalf("frame = %q, want exactly one trailing newline", request)
	}
	frame, err := decodeEnvelope(request)
	if err != nil {
		t.Fatalf("decodeEnvelope: %v", err)
	}
	if frame.Method != "send" || string(frame.ID) != `"7"` {
		t.Fatalf("decoded = %#v", frame)
	}
	var params map[string]any
	if err := json.Unmarshal(frame.Params, &params); err != nil || params["text"] != "a\nb" {
		t.Fatalf("params = %#v (%v)", params, err)
	}

	for _, bad := range []string{
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"1.0","id":1,"method":"hello"}`,
		`not json`,
		`[]`,
	} {
		if _, err := decodeEnvelope([]byte(bad)); err == nil {
			t.Fatalf("decodeEnvelope(%q) accepted an invalid frame", bad)
		}
	}

	encoded := base64.StdEncoding.EncodeToString([]byte("hello"))
	if images, err := decodeSendImages([]string{encoded}); err != nil || len(images) != 1 || string(images[0]) != "hello" {
		t.Fatalf("decodeSendImages = %#v (%v)", images, err)
	}
	if _, err := decodeSendImages([]string{"%,&"}); err == nil {
		t.Fatal("invalid base64 accepted")
	}
	tooMany := make([]string, MaxImagesPerSend+1)
	for index := range tooMany {
		tooMany[index] = encoded
	}
	if _, err := decodeSendImages(tooMany); err == nil {
		t.Fatalf("%d images accepted", len(tooMany))
	}
	oversize := base64.StdEncoding.EncodeToString(make([]byte, MaxImageBytes+1))
	if _, err := decodeSendImages([]string{oversize}); err == nil {
		t.Fatal("an oversized image was accepted")
	}
	if images, err := decodeSendImages([]string{"data:image/png;base64," + encoded}); err != nil || string(images[0]) != "hello" {
		t.Fatalf("data URI: %#v (%v)", images, err)
	}
}

// TestHostDefaultsMatchTheUpstreamBudget pins the start/stop and transport
// defaults.
//
// Parity: core/framework/server/channels.py::start_channels
func TestHostDefaultsMatchTheUpstreamBudget(t *testing.T) {
	host := NewHost(HostOptions{})
	if host.options.StartTimeout != 30*time.Second {
		t.Fatalf("StartTimeout = %s, want 30s", host.options.StartTimeout)
	}
	if host.options.StopTimeout != 30*time.Second {
		t.Fatalf("StopTimeout = %s, want 30s", host.options.StopTimeout)
	}
	if host.options.HandshakeTimeout != 10*time.Second {
		t.Fatalf("HandshakeTimeout = %s", host.options.HandshakeTimeout)
	}
	if host.options.QueueSize != DefaultQueueSize || host.options.MaxFrameBytes != DefaultMaxFrameBytes {
		t.Fatalf("queue/frame defaults = %d/%d", host.options.QueueSize, host.options.MaxFrameBytes)
	}
}

// TestHostStartsAPluginAndReportsRunning covers the hello/start handshake and
// the running state.
//
// Parity: core/framework/server/channels.py::start_channels
func TestHostStartsAPluginAndReportsRunning(t *testing.T) {
	record := filepath.Join(t.TempDir(), "plugin.jsonl")
	backend := newFakeBackend()
	config := map[string]any{"channels": map[string]any{
		"fake": fakeSection("recorder", record, map[string]any{"token": "kept", "subscribe_session": "session_1"}),
	}}
	host := startFakeHost(t, config, backend, nil)

	if got := host.States()["fake"]; got["state"] != "running" {
		t.Fatalf("state = %#v, want running", got)
	}
	records := waitRecords(t, record, 3)
	hello := recordByKind(t, records, "hello")
	helloParams, _ := hello["params"].(map[string]any)
	if helloParams["protocol"] != ProtocolVersion || helloParams["name"] != "fake" {
		t.Fatalf("hello params = %#v", helloParams)
	}
	pluginConfig, _ := helloParams["config"].(map[string]any)
	if pluginConfig["token"] != "kept" {
		t.Fatalf("hello config = %#v, want the section passed through", pluginConfig)
	}
	stateDir, _ := helloParams["state_dir"].(string)
	if info, err := os.Stat(stateDir); err != nil || !info.IsDir() {
		t.Fatalf("state_dir %q not created: %v", stateDir, err)
	}
	recordByKind(t, records, "start")
	callRecords := records[len(records)-3:]
	var subscribe map[string]any
	for _, record := range records {
		if record["record"] == "call" && record["method"] == "subscribe" {
			subscribe = record
		}
	}
	if subscribe == nil {
		t.Fatalf("no subscribe call record in %#v", callRecords)
	}
	result, _ := subscribe["result"].(map[string]any)
	if result["subscription_id"] == "" {
		t.Fatalf("subscribe result = %#v", subscribe)
	}
}

// TestHostReportsEnabledWithoutCommandFailed covers the failure instead of
// silently ignoring an enabled channel.
//
// Parity: core/framework/server/channels.py::start_channels
func TestHostReportsEnabledWithoutCommandFailed(t *testing.T) {
	config := map[string]any{"channels": map[string]any{
		"nocommand": map[string]any{"enabled": true},
		"off":       map[string]any{"enabled": false},
		"badcmd":    map[string]any{"enabled": true, "command": []any{1}},
		"../evil":   map[string]any{"enabled": true, "command": []any{os.Args[0]}},
	}}
	host := startFakeHost(t, config, newFakeBackend(), nil)

	states := host.States()
	nocommand := states["nocommand"]
	if nocommand["state"] != "failed" {
		t.Fatalf("nocommand state = %#v", nocommand)
	}
	message, _ := nocommand["error"].(string)
	if !strings.Contains(message, "no command or entry_point configured") ||
		!strings.Contains(message, "Python hive.channels plugin") {
		t.Fatalf("nocommand error = %q", message)
	}
	if states["off"]["state"] != "off" {
		t.Fatalf("off state = %#v", states["off"])
	}
	badcmd := states["badcmd"]
	if badcmd["state"] != "failed" || !strings.Contains(badcmd["error"].(string), "must be a string") {
		t.Fatalf("badcmd state = %#v", badcmd)
	}
	evil := states["../evil"]
	if evil["state"] != "failed" || !strings.Contains(evil["error"].(string), "invalid channel name") {
		t.Fatalf("evil state = %#v", evil)
	}
}

// TestHostIsolatesStartFailureAndKeepsOthersRunning starts a failing plugin
// next to a working one.
//
// Parity: core/framework/server/channels.py::start_channels
func TestHostIsolatesStartFailureAndKeepsOthersRunning(t *testing.T) {
	record := filepath.Join(t.TempDir(), "ok.jsonl")
	config := map[string]any{"channels": map[string]any{
		"broken":   fakeSection("fail_start", "", nil),
		"healthy":  fakeSection("recorder", record, map[string]any{"subscribe_session": "session_1"}),
		"also-bad": fakeSection("hang_start", "", nil),
	}}
	host := startFakeHost(t, config, newFakeBackend(), func(options *HostOptions) {
		options.StartTimeout = 120 * time.Millisecond
		options.HandshakeTimeout = 2 * time.Second
	})
	states := host.States()
	if states["healthy"]["state"] != "running" {
		t.Fatalf("healthy state = %#v", states["healthy"])
	}
	broken := states["broken"]
	if broken["state"] != "failed" || !strings.Contains(broken["error"].(string), "start refused by fake") {
		t.Fatalf("broken state = %#v", broken)
	}
	timeout := states["also-bad"]
	if timeout["state"] != "failed" || !strings.Contains(timeout["error"].(string), "timed out") {
		t.Fatalf("hang_start state = %#v", timeout)
	}
	records := waitRecords(t, record, 1)
	if recordByKind(t, records, "hello") == nil {
		t.Fatal("healthy plugin never greeted")
	}
}

// TestHostMarksFailedOnPluginExit marks a running channel failed when its
// process dies.
//
// Parity: core/framework/server/channels.py::start_channels
func TestHostMarksFailedOnPluginExit(t *testing.T) {
	host := startFakeHost(t, map[string]any{"channels": map[string]any{
		"fake": fakeSection("exit_after_start", "", nil),
	}}, newFakeBackend(), nil)
	if host.States()["fake"]["state"] != "running" {
		t.Fatalf("first state = %#v", host.States()["fake"])
	}
	waitFor(t, 3*time.Second, "failed state", func() bool {
		state := host.States()["fake"]
		message, _ := state["error"].(string)
		return state["state"] == "failed" && strings.Contains(message, "exited")
	})
}

// TestHostRejectsStdoutThatIsNotProtocol treats a stray stdout line as a
// protocol violation.
//
// Parity-exempt: stdout purity is a stdio-transport rule with no upstream
// counterpart.
func TestHostRejectsStdoutThatIsNotProtocol(t *testing.T) {
	host := startFakeHost(t, map[string]any{"channels": map[string]any{
		"fake": fakeSection("bad_stdout", "", nil),
	}}, newFakeBackend(), nil)
	waitFor(t, 3*time.Second, "failed state", func() bool {
		state := host.States()["fake"]
		message, _ := state["error"].(string)
		return state["state"] == "failed" && strings.Contains(message, "non-protocol line on stdout")
	})
}

// TestHostStopsHangingPluginWithinBudget bounds stop with the configured
// budget and kills a plugin that ignores the request.
//
// Parity: core/framework/server/channels.py::stop_channels
func TestHostStopsHangingPluginWithinBudget(t *testing.T) {
	host := startFakeHost(t, map[string]any{"channels": map[string]any{
		"fake": fakeSection("ignore_stop", "", nil),
	}}, newFakeBackend(), nil)
	if host.States()["fake"]["state"] != "running" {
		t.Fatalf("state = %#v", host.States()["fake"])
	}
	host.options.StopTimeout = 120 * time.Millisecond
	started := time.Now()
	host.Stop()
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Stop took %s with a 120ms budget", elapsed)
	}
	// Idempotent second call.
	host.Stop()
}

// TestHostServesPluginSessionRequests exercises every plugin→host method.
//
// Parity: core/framework/server/channels.py::ChannelHost
func TestHostServesPluginSessionRequests(t *testing.T) {
	record := filepath.Join(t.TempDir(), "requests.jsonl")
	backend := newFakeBackend()
	judge := 0.5
	backend.judge = &judge
	image := base64.StdEncoding.EncodeToString([]byte("hello"))
	config := map[string]any{"channels": map[string]any{
		"fake": fakeSection("requests", record, map[string]any{
			"queen_id":       "queen_test",
			"session_id":     "session_test",
			"send_text":      "hi there",
			"display_text":   "shown",
			"correlation_id": "corr-1",
			"title":          "A title",
			"images":         []any{image},
		}),
	}}
	host := startFakeHost(t, config, backend, nil)
	if host.States()["fake"]["state"] != "running" {
		t.Fatalf("state = %#v", host.States()["fake"])
	}
	records := waitRecords(t, record, 8)
	calls := map[string]map[string]any{}
	for _, record := range records {
		if record["record"] == "call" {
			method, _ := record["method"].(string)
			calls[method] = record
		}
	}
	for _, method := range []string{"open_session", "send", "subscribe", "set_title", "judge", "unknown_method"} {
		if calls[method] == nil {
			t.Fatalf("missing %s call record in %#v", method, records)
		}
	}
	open := calls["open_session"]["result"].(map[string]any)
	if open["session_id"] != "session_test" {
		t.Fatalf("open_session result = %#v", open)
	}
	send := calls["send"]["result"].(map[string]any)
	if send["session_id"] != "session_test" {
		t.Fatalf("send result = %#v", send)
	}
	judgeResult := calls["judge"]["result"].(map[string]any)
	if judgeResult["probability"] != 0.5 {
		t.Fatalf("judge result = %#v", judgeResult)
	}
	unknown := calls["unknown_method"]["error"].(map[string]any)
	if int(unknown["code"].(float64)) != CodeMethodNotFound {
		t.Fatalf("unknown_method error = %#v", unknown)
	}

	sendCall, _ := backend.callFor("send")
	if sendCall.Text != "hi there" || sendCall.DisplayText != "shown" || sendCall.CorrelationID != "corr-1" {
		t.Fatalf("backend send call = %#v", sendCall)
	}
	if len(sendCall.Images) != 1 || string(sendCall.Images[0]) != "hello" {
		t.Fatalf("backend images = %#v", sendCall.Images)
	}
	titleCall, _ := backend.callFor("set_title")
	if titleCall.Title != "A title" {
		t.Fatalf("set_title call = %#v", titleCall)
	}
}

// TestHostMapsBackendErrorsToKinds maps ChannelError kinds onto JSON-RPC
// error data.
//
// Parity: core/framework/server/channels.py::ChannelError
func TestHostMapsBackendErrorsToKinds(t *testing.T) {
	record := filepath.Join(t.TempDir(), "errors.jsonl")
	backend := newFakeBackend()
	backend.failed = &fakeFailure{
		open: Errorf(KindNoQueen, "No queen 'missing': check the queen id in the 'fake' channel's settings"),
		send: Errorf(KindNoSession, "No session 'missing'"),
	}
	config := map[string]any{"channels": map[string]any{
		"fake": fakeSection("requests", record, map[string]any{
			"queen_id":   "missing",
			"session_id": "missing",
			"send_text":  "hello",
		}),
	}}
	startFakeHost(t, config, backend, nil)
	records := waitRecords(t, record, 6)
	var openError, sendError map[string]any
	for _, record := range records {
		if record["record"] != "call" {
			continue
		}
		if record["method"] == "open_session" {
			openError, _ = record["error"].(map[string]any)
		}
		if record["method"] == "send" {
			sendError, _ = record["error"].(map[string]any)
		}
	}
	if openError == nil || sendError == nil {
		t.Fatalf("missing error records in %#v", records)
	}
	openData, _ := openError["data"].(map[string]any)
	if openData["kind"] != string(KindNoQueen) || !strings.Contains(openError["message"].(string), "No queen 'missing'") {
		t.Fatalf("open error = %#v", openError)
	}
	sendData, _ := sendError["data"].(map[string]any)
	if sendData["kind"] != string(KindNoSession) {
		t.Fatalf("send error = %#v", sendError)
	}
}

// TestHostStreamsOutputsInOrder delivers QueenOutputs in order with a
// per-subscription sequence.
//
// Parity: core/framework/server/channels.py::QueenOutput
func TestHostStreamsOutputsInOrder(t *testing.T) {
	record := filepath.Join(t.TempDir(), "outputs.jsonl")
	backend := newFakeBackend()
	config := map[string]any{"channels": map[string]any{
		"fake": fakeSection("recorder", record, map[string]any{"subscribe_session": "session_1"}),
	}}
	host := startFakeHost(t, config, backend, nil)
	if host.States()["fake"]["state"] != "running" {
		t.Fatalf("state = %#v", host.States()["fake"])
	}
	waitFor(t, 3*time.Second, "subscription", func() bool { return backend.subscriptionCount() == 1 })

	const count = 25
	go func() {
		for index := 0; index < count; index++ {
			kind := OutputMessage
			text := "message-" + itoa(index)
			switch index {
			case 5:
				kind = OutputQuestion
				text = ""
			case 10:
				kind = OutputFiles
				text = ""
			case 15:
				kind = OutputIdle
				text = ""
			}
			backend.emit("session_1", Output{
				SessionID: "session_1",
				Kind:      kind,
				Text:      text,
				Questions: func() []map[string]any {
					if index == 5 {
						return []map[string]any{{"id": "q1", "prompt": "why?", "options": []any{"a", "b"}}}
					}
					return nil
				}(),
				Files: func() []string {
					if index == 10 {
						return []string{"/tmp/report.txt"}
					}
					return nil
				}(),
			})
		}
	}()

	records := waitRecords(t, record, 3+count)
	outputs := outputRecords(records)
	if len(outputs) != count {
		t.Fatalf("outputs = %d, want %d", len(outputs), count)
	}
	var subscriptionID string
	for index, output := range outputs {
		if output["subscription_id"] == "" {
			t.Fatalf("output %d has no subscription_id: %#v", index, output)
		}
		if subscriptionID == "" {
			subscriptionID = output["subscription_id"].(string)
		}
		if output["subscription_id"] != subscriptionID {
			t.Fatalf("output %d subscription = %v, want %s", index, output["subscription_id"], subscriptionID)
		}
		if int(output["seq"].(float64)) != index+1 {
			t.Fatalf("output %d seq = %v, want %d", index, output["seq"], index+1)
		}
		if output["session_id"] != "session_1" {
			t.Fatalf("output %d session = %v", index, output["session_id"])
		}
	}
	if outputs[5]["kind"] != string(OutputQuestion) {
		t.Fatalf("output 5 kind = %v", outputs[5]["kind"])
	}
	questions, _ := outputs[5]["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("output 5 questions = %#v", outputs[5]["questions"])
	}
	files, _ := outputs[10]["files"].([]any)
	if len(files) != 1 || files[0] != "/tmp/report.txt" {
		t.Fatalf("output 10 files = %#v", outputs[10]["files"])
	}
	if outputs[15]["kind"] != string(OutputIdle) || outputs[15]["text"] != "" {
		t.Fatalf("output 15 = %#v", outputs[15])
	}
}

// TestHostUnsubscribeStopsTheStream stops outputs after a plugin unsubscribe.
//
// Parity: core/framework/server/channels.py::ChannelHost.unsubscribe
func TestHostUnsubscribeStopsTheStream(t *testing.T) {
	record := filepath.Join(t.TempDir(), "unsub.jsonl")
	backend := newFakeBackend()
	config := map[string]any{"channels": map[string]any{
		"fake": fakeSection("unsubscribe_after_first", record, map[string]any{"subscribe_session": "session_1"}),
	}}
	host := startFakeHost(t, config, backend, nil)
	waitFor(t, 3*time.Second, "subscription", func() bool { return backend.subscriptionCount() == 1 })
	if !backend.emit("session_1", Output{SessionID: "session_1", Kind: OutputMessage, Text: "one"}) {
		t.Fatal("first emit found no subscriber")
	}
	records := waitRecords(t, record, 4)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && backend.subscriptionCount() != 0 {
		time.Sleep(10 * time.Millisecond)
		records = readRecords(t, record)
	}
	if backend.subscriptionCount() != 0 {
		t.Fatalf("unsubscribe did not reach the backend; records=%#v", records)
	}
	if len(outputRecords(records)) < 1 {
		t.Fatalf("no output recorded before unsubscribe: %#v", records)
	}
	backend.emit("session_1", Output{SessionID: "session_1", Kind: OutputMessage, Text: "two"})
	time.Sleep(100 * time.Millisecond)
	outputs := outputRecords(readRecords(t, record))
	if len(outputs) != 1 || outputs[0]["text"] != "one" {
		t.Fatalf("outputs after unsubscribe = %#v", outputs)
	}
	if host.States()["fake"]["state"] != "running" {
		t.Fatalf("host state = %#v", host.States())
	}
}

// TestSubscriptionQueueDropsOldest is the explicit bounded-queue policy.
//
// Parity-exempt: upstream's asyncio queue is unbounded; the bounded drop is a
// Go stdio-host choice, observable as a seq gap.
func TestSubscriptionQueueDropsOldest(t *testing.T) {
	sub := &subscription{
		queue: make(chan Output, 2),
		stop:  make(chan struct{}),
	}
	sub.enqueue(Output{Kind: OutputMessage, Text: "one"})
	sub.enqueue(Output{Kind: OutputMessage, Text: "two"})
	sub.enqueue(Output{Kind: OutputMessage, Text: "three"})
	sub.mu.Lock()
	drops := sub.drops
	sub.mu.Unlock()
	if drops != 1 {
		t.Fatalf("drops = %d, want 1", drops)
	}
	if len(sub.queue) != 2 {
		t.Fatalf("queue length = %d, want 2", len(sub.queue))
	}
	first := <-sub.queue
	second := <-sub.queue
	if first.Text != "two" || second.Text != "three" {
		t.Fatalf("queue kept %q then %q, want the newest two", first.Text, second.Text)
	}
}

// itoa avoids importing strconv just for test loop labels.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
