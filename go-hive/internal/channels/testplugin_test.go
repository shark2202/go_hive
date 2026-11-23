package channels

// A re-exec fake channel plugin for the host tests. TestMain intercepts the
// helper environment variable before the testing package writes anything to
// stdout, so the child speaks only the stdio protocol.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

const fakePluginEnv = "GO_WANT_CHANNEL_PLUGIN_HELPER"

// Parity-exempt: TestMain only re-execs this test binary as the fake stdio
// plugin; the protocol cases are the actual tests below.
func TestMain(m *testing.M) {
	if os.Getenv(fakePluginEnv) == "1" {
		runFakePlugin()
		return
	}
	os.Exit(m.Run())
}

type fakePlugin struct {
	reader       *bufio.Reader
	writer       *os.File
	recordPath   string
	config       map[string]any
	mode         string
	nextID       int
	queue        []map[string]any
	subscribed   string
	unsubscribed bool
}

func runFakePlugin() {
	plugin := &fakePlugin{
		reader: bufio.NewReaderSize(os.Stdin, 64*1024),
		writer: os.Stdout,
	}
	for {
		frame, err := plugin.readFrame()
		if err != nil {
			return
		}
		plugin.handleHostFrame(frame)
	}
}

func (p *fakePlugin) readFrame() (map[string]any, error) {
	line, err := p.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var frame map[string]any
	if err := json.Unmarshal(line, &frame); err != nil {
		fmt.Fprintf(os.Stderr, "fake plugin: bad host frame: %v\n", err)
		return nil, err
	}
	return frame, nil
}

func (p *fakePlugin) handleHostFrame(frame map[string]any) {
	method, _ := frame["method"].(string)
	id, hasID := frame["id"]
	if method == "" {
		// A response to a request this plugin sent while scripting.
		return
	}
	if method == "output" {
		params, _ := frame["params"].(map[string]any)
		p.record(map[string]any{"record": "output", "params": params})
		if p.mode == "unsubscribe_after_first" && !p.unsubscribed {
			p.unsubscribed = true
			if subscriptionID, _ := params["subscription_id"].(string); subscriptionID != "" {
				p.recordCall("unsubscribe", p.callHost("unsubscribe", map[string]any{"subscription_id": subscriptionID}))
			}
		}
		return
	}
	if !hasID {
		return
	}
	switch method {
	case "hello":
		params, _ := frame["params"].(map[string]any)
		p.config, _ = params["config"].(map[string]any)
		p.mode, _ = p.config["mode"].(string)
		if p.mode == "" {
			p.mode = "recorder"
		}
		if path, _ := p.config["record_file"].(string); path != "" {
			p.recordPath = path
		}
		p.record(map[string]any{"record": "hello", "params": params})
		p.reply(id, map[string]any{
			"plugin_version": "0.1.0",
			"capabilities":   []string{"send", "files", "questions"},
		})
	case "start":
		p.record(map[string]any{"record": "start"})
		switch p.mode {
		case "fail_start":
			p.replyError(id, -32000, "start refused by fake", KindInternal)
			return
		case "hang_start":
			return
		case "crash":
			os.Exit(3)
		}
		p.reply(id, map[string]any{})
		switch p.mode {
		case "bad_stdout":
			fmt.Fprintln(os.Stdout, "this is not a protocol frame")
		case "exit_after_start":
			time.Sleep(100 * time.Millisecond)
			os.Exit(0)
		case "requests":
			p.scriptRequests()
		case "recorder", "unsubscribe_after_first":
			p.subscribeIfConfigured()
		}
	case "stop":
		if p.mode == "ignore_stop" {
			return
		}
		p.reply(id, map[string]any{})
	}
}

func (p *fakePlugin) subscribeIfConfigured() {
	sessionID, _ := p.config["subscribe_session"].(string)
	if sessionID == "" {
		return
	}
	result := p.callHost("subscribe", map[string]any{"session_id": sessionID})
	p.recordCall("subscribe", result)
	if object, ok := result["result"].(map[string]any); ok {
		p.subscribed, _ = object["subscription_id"].(string)
	}
}

func (p *fakePlugin) scriptRequests() {
	queenID, _ := p.config["queen_id"].(string)
	sessionID, _ := p.config["session_id"].(string)
	text, _ := p.config["send_text"].(string)
	title, _ := p.config["title"].(string)
	images, _ := p.config["images"].([]any)

	p.recordCall("open_session", p.callHost("open_session", map[string]any{
		"queen_id":   queenID,
		"session_id": sessionID,
		"title":      title,
	}))
	sendParams := map[string]any{"session_id": sessionID, "text": text}
	if len(images) > 0 {
		sendParams["images"] = images
	}
	if display, _ := p.config["display_text"].(string); display != "" {
		sendParams["display_text"] = display
	}
	if correlation, _ := p.config["correlation_id"].(string); correlation != "" {
		sendParams["correlation_id"] = correlation
	}
	p.recordCall("send", p.callHost("send", sendParams))
	p.recordCall("subscribe", p.callHost("subscribe", map[string]any{"session_id": sessionID}))
	p.recordCall("set_title", p.callHost("set_title", map[string]any{"session_id": sessionID, "title": title}))
	p.recordCall("judge", p.callHost("judge", map[string]any{
		"question": "is it so?",
		"state":    map[string]any{"answer": "yes"},
		"criteria": map[string]any{"true": "yes", "false": "no"},
	}))
	p.recordCall("unknown_method", p.callHost("unknown_method", map[string]any{}))
}

// callHost sends one request and reads until its response, handling any host
// notification that arrives in between.
func (p *fakePlugin) callHost(method string, params map[string]any) map[string]any {
	p.nextID++
	id := fmt.Sprintf("plugin-%d", p.nextID)
	p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	for {
		frame, err := p.readFrame()
		if err != nil {
			return map[string]any{"error": map[string]any{"message": err.Error()}}
		}
		if frameID, ok := frame["id"].(string); ok && frameID == id {
			return frame
		}
		if incoming, _ := frame["method"].(string); incoming != "" {
			p.handleHostFrame(frame)
		}
	}
}

func (p *fakePlugin) recordCall(method string, response map[string]any) {
	record := map[string]any{"record": "call", "method": method}
	if result, present := response["result"]; present {
		record["result"] = result
	}
	if rpcErr, present := response["error"]; present {
		record["error"] = rpcErr
	}
	p.record(record)
}

func (p *fakePlugin) reply(id any, result any) {
	p.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (p *fakePlugin) replyError(id any, code int, message string, kind ErrorKind) {
	p.write(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
			"data":    map[string]any{"kind": string(kind)},
		},
	})
}

func (p *fakePlugin) write(frame map[string]any) {
	encoded, err := json.Marshal(frame)
	if err != nil {
		return
	}
	_, _ = p.writer.Write(append(encoded, '\n'))
}

func (p *fakePlugin) record(value map[string]any) {
	if p.recordPath == "" {
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return
	}
	file, err := os.OpenFile(p.recordPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	_, _ = file.Write(append(encoded, '\n'))
	_ = file.Close()
}
