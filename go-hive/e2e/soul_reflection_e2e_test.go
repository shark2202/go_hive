//go:build e2e

package e2e

// Soul reflection end-to-end: the real `hive serve` binary against a temporary
// HIVE_HOME, driven through POST /api/queen/{id}/soul/reflect.
//
// The model is the deterministic local OpenAI-compatible gateway in
// e2e/soulgw. A real open-weights model is not available in this environment
// (no ollama install, no provider key), so the gateway plays the reflection
// tool loop; everything else is real: the built binary, the HTTP transport,
// the queen's on-disk session events, the proposal and state.json writes.
//
// Proves: reflect no longer answers 503; review leaves a pending proposal
// (with a verbatim chat quote); auto applies it to soul.json; a second
// in-flight reflect answers 409; a queen without chat sessions fails
// explicitly instead of recording a fake run; a queen without soul config is
// rejected with 409.
//
// Run:
//
//	go test -tags e2e ./e2e -run TestSoulReflectionEndToEnd -v
//	HIVE_SOUL_E2E_HOME=/tmp/soul-e2e go test -tags e2e ./e2e -run ... -v

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aden-hive/hive-go/e2e/soulgw"
)

const (
	soulE2EQueen      = "queen_x"
	soulE2EAutoQueen  = "queen_auto"
	soulE2EEmptyQueen = "queen_empty"
)

const soulE2ESaid = "I stayed up painting again and it felt good."
const soulE2EReplied = "I'm glad you made time for it."

func soulE2EWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func soulE2EConfigEntry(mode string) string {
	return fmt.Sprintf(`{"enabled":true,"mode":%q,"hour":23,"max_edits":3,"sections":[{"key":"now","title":"Now","limit":300}]}`, mode)
}

// soulE2EWriteSession writes one queen session with a said line and her reply.
func soulE2EWriteSession(t *testing.T, homeDir, queenID, sessionID, title, said, replied string) (string, string) {
	t.Helper()
	saidAt := time.Now().Add(-3 * time.Minute).Format("2006-01-02T15:04:05")
	repliedAt := time.Now().Add(-2 * time.Minute).Format("2006-01-02T15:04:05")
	dir := filepath.Join(homeDir, "queens", queenID, "sessions", sessionID)
	soulE2EWriteFile(t, filepath.Join(dir, "meta.json"), fmt.Sprintf(`{"title":%q,"channel":false}`, title))
	events := fmt.Sprintf(
		"{\"type\":\"client_input_received\",\"timestamp\":%q,\"data\":{\"content\":%q}}\n"+
			"{\"type\":\"client_output_delta\",\"timestamp\":%q,\"stream_id\":\"queen\",\"execution_id\":\"e1\",\"data\":{\"snapshot\":%q,\"iteration\":0,\"inner_turn\":0}}\n",
		saidAt, said, repliedAt, replied)
	soulE2EWriteFile(t, filepath.Join(dir, "events.jsonl"), events)
	return saidAt, repliedAt
}

func soulE2EJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return decoded
}

func soulE2EWaitFor(t *testing.T, timeout time.Duration, what string, probe func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		ok, detail := probe()
		if ok {
			return
		}
		last = detail
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last: %s", what, last)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Parity-exempt: Go-side end-to-end; no upstream test runs the real serve binary against a
// temporary HIVE_HOME, so there is nothing to map to.
func TestSoulReflectionEndToEnd(t *testing.T) {
	gateway := soulgw.New(soulgw.Options{Delay: 750 * time.Millisecond})
	gatewayServer := httptest.NewServer(gateway.Handler())
	defer gatewayServer.Close()

	root := t.TempDir()
	keep := os.Getenv("HIVE_SOUL_E2E_HOME")
	if keep != "" {
		root = filepath.Join(keep, "soul-e2e")
	}
	homeDir := filepath.Join(root, "hive-home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatalf("create hive home: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	configuration := fmt.Sprintf(
		`{"llm":{"provider":"custom","model":"ollama/soul-e2e","api_base":%q},"soul":{"queens":{%q:%s,%q:%s,%q:%s}}}`,
		gatewayServer.URL+"/v1",
		soulE2EQueen, soulE2EConfigEntry("review"),
		soulE2EAutoQueen, soulE2EConfigEntry("auto"),
		soulE2EEmptyQueen, soulE2EConfigEntry("review"),
	)
	soulE2EWriteFile(t, filepath.Join(homeDir, "configuration.json"), configuration)
	for _, queenID := range []string{soulE2EQueen, soulE2EAutoQueen, soulE2EEmptyQueen, "queen_plain"} {
		soulE2EWriteFile(t, filepath.Join(homeDir, "queens", queenID, "profile.yaml"), "name: Fox\n")
	}
	// Pre-seed last_run so the boot-time daily scheduler does not race the
	// manual reflect this test drives.
	for _, queenID := range []string{soulE2EQueen, soulE2EAutoQueen, soulE2EEmptyQueen} {
		soulE2EWriteFile(t, filepath.Join(homeDir, "queens", queenID, "soul", "state.json"),
			fmt.Sprintf(`{"last_run":%q,"last_result":"pre-seeded"}`, today))
	}
	_, repliedAt := soulE2EWriteSession(t, homeDir, soulE2EQueen, "s1", "Painting at night", soulE2ESaid, soulE2EReplied)
	soulE2EWriteSession(t, homeDir, soulE2EAutoQueen, "s1", "The mural", "I finally asked for help with the mural.", "It sounds like that changed the work.")

	moduleRoot := memModuleRoot(t)
	binary := filepath.Join(t.TempDir(), "hive")
	build := exec.Command("go", "build", "-o", binary, "./cmd/hive")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/hive: %v\n%s", err, output)
	}

	port := credFreePort(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := &credLogBuffer{}
	serverCmd := exec.Command(binary, "serve", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	serverCmd.Dir = t.TempDir()
	serverCmd.Env = memChildEnv(homeDir, nil)
	serverCmd.Stdout = logs
	serverCmd.Stderr = logs
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start hive serve: %v", err)
	}
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = serverCmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() { credStopHive(t, serverCmd, exited, logs) })

	client := &http.Client{Timeout: 30 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	healthy := false
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			t.Fatalf("hive serve exited before /api/health (err=%v); logs:\n%s", exitErr, logs.String())
		default:
		}
		resp, err := client.Get(baseURL + "/api/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				healthy = true
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatalf("hive serve did not become healthy; logs:\n%s", logs.String())
	}

	postReflect := func(target string) (int, map[string]any) {
		status, raw := credDoJSON(t, client, http.MethodPost, baseURL+target, map[string]any{})
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		return status, body
	}
	getSoul := func(queenID string) map[string]any {
		status, raw := credDoJSON(t, client, http.MethodGet, baseURL+"/api/queen/"+queenID+"/soul", nil)
		if status != http.StatusOK {
			t.Fatalf("GET soul(%s) = %d; body: %s", queenID, status, raw)
		}
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		return body
	}

	// 1) review reflect: 202, and a second request while it is in flight 409s.
	status, body := postReflect("/api/queen/" + soulE2EQueen + "/soul/reflect")
	if status != http.StatusAccepted || body["started"] != true {
		t.Fatalf("review reflect = %d %v, want 202 started", status, body)
	}
	status, body = postReflect("/api/queen/" + soulE2EQueen + "/soul/reflect")
	if status != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "reflecting already") {
		t.Fatalf("second reflect = %d %v, want 409 reflecting already", status, body)
	}

	soulE2EWaitFor(t, 30*time.Second, "queen_x's pending proposal", func() (bool, string) {
		view := getSoul(soulE2EQueen)
		if view["proposal"] == nil {
			return false, fmt.Sprintf("view = %v; logs:\n%s", view, logs.String())
		}
		return true, ""
	})
	view := getSoul(soulE2EQueen)
	proposal, _ := view["proposal"].(map[string]any)
	if proposal["status"] != "pending" {
		t.Fatalf("proposal status = %v, want pending", proposal["status"])
	}
	edits, _ := proposal["edits"].([]any)
	if len(edits) != 1 {
		t.Fatalf("edits = %#v, want 1", proposal["edits"])
	}
	edit := edits[0].(map[string]any)
	if edit["op"] != "add" || edit["section"] != "now" || edit["text"] != "I want to keep painting." {
		t.Fatalf("edit = %#v", edit)
	}
	evidence, _ := edit["evidence"].([]any)
	first, _ := evidence[0].(map[string]any)
	if first["quote"] != soulE2ESaid {
		t.Fatalf("evidence quote = %#v, want %q", evidence, soulE2ESaid)
	}
	if view["last_run"] != today || view["last_result"] != "1 changes" {
		t.Fatalf("soul view last_run/last_result = %v/%v, want %s/1 changes", view["last_run"], view["last_result"], today)
	}

	// state.json on disk: cursor at the newest event, run recorded.
	state := soulE2EJSONFile(t, filepath.Join(homeDir, "queens", soulE2EQueen, "soul", "state.json"))
	cursors, _ := state["cursors"].(map[string]any)
	if cursors["s1"] != repliedAt {
		t.Fatalf("cursor = %v, want %q", cursors["s1"], repliedAt)
	}
	if state["last_run"] != today || state["last_result"] != "1 changes" {
		t.Fatalf("state = %#v", state)
	}

	// 2) auto reflect applies straight into soul.json.
	status, body = postReflect("/api/queen/" + soulE2EAutoQueen + "/soul/reflect")
	if status != http.StatusAccepted {
		t.Fatalf("auto reflect = %d %v, want 202", status, body)
	}
	soulE2EWaitFor(t, 30*time.Second, "queen_auto's applied soul", func() (bool, string) {
		raw, err := os.ReadFile(filepath.Join(homeDir, "queens", soulE2EAutoQueen, "soul", "soul.json"))
		if err != nil {
			return false, err.Error()
		}
		return strings.Contains(string(raw), "I want to keep painting."), string(raw)
	})
	autoView := getSoul(soulE2EAutoQueen)
	sections, _ := autoView["sections"].([]any)
	section, _ := sections[0].(map[string]any)
	items, _ := section["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("auto items = %#v", section["items"])
	}
	item, _ := items[0].(map[string]any)
	if item["text"] != "I want to keep painting." {
		t.Fatalf("auto item = %#v", item)
	}
	proposalFiles, err := filepath.Glob(filepath.Join(homeDir, "queens", soulE2EAutoQueen, "soul", "proposals", "*.json"))
	if err != nil || len(proposalFiles) != 1 {
		t.Fatalf("auto proposal files = %v, err = %v", proposalFiles, err)
	}
	autoProposal := soulE2EJSONFile(t, proposalFiles[0])
	if autoProposal["status"] != "applied" {
		t.Fatalf("auto proposal status = %v, want applied", autoProposal["status"])
	}

	// 3) A queen with no chat sessions fails explicitly: the manual run
	// returns an error, nothing is recorded as a run.
	status, body = postReflect("/api/queen/" + soulE2EEmptyQueen + "/soul/reflect")
	if status != http.StatusAccepted {
		t.Fatalf("empty-queen reflect = %d %v, want 202", status, body)
	}
	soulE2EWaitFor(t, 20*time.Second, "the no-sessions error in the runtime log", func() (bool, string) {
		if strings.Contains(logs.String(), "has no chat sessions to read") {
			return true, ""
		}
		return false, logs.String()
	})
	emptyState := soulE2EJSONFile(t, filepath.Join(homeDir, "queens", soulE2EEmptyQueen, "soul", "state.json"))
	if emptyState["last_result"] != "pre-seeded" {
		t.Fatalf("empty-queen state was rewritten: %#v", emptyState)
	}
	if _, present := emptyState["last_error"]; present {
		t.Fatalf("empty-queen state = %#v", emptyState)
	}

	// 4) A queen without soul config is rejected before any run.
	status, body = postReflect("/api/queen/queen_plain/soul/reflect")
	if status != http.StatusConflict || !strings.Contains(fmt.Sprint(body["error"]), "has no soul") {
		t.Fatalf("soulless reflect = %d %v, want 409 has no soul", status, body)
	}

	// The gateway saw the real reflection prompts over HTTP: the four tools,
	// the chat text, and the soul system prompt.
	requests := gateway.ReflectionRequests()
	if len(requests) < 4 {
		t.Fatalf("reflection requests = %d, want at least 4 (two calls per reflected queen); logs:\n%s", len(requests), logs.String())
	}
	firstRequest := requests[0].Body
	for _, needle := range []string{"add_item", "update_item", "remove_item", "finish", soulE2ESaid, "read back what was said in your chats"} {
		if !strings.Contains(firstRequest, needle) {
			t.Errorf("first reflection request is missing %q", needle)
		}
	}

	if keep != "" {
		records, err := json.MarshalIndent(gateway.Requests(), "", "  ")
		if err == nil {
			soulE2EWriteFile(t, filepath.Join(root, "gateway-requests.json"), string(records))
		}
		soulE2EWriteFile(t, filepath.Join(root, "evidence.txt"), fmt.Sprintf(
			"proposal=%#v\nstate=%#v\nauto_proposal=%#v\nauto_soul_item=%#v\nlogs_tail=\n%s",
			proposal, state, autoProposal, item, tailLines(logs.String(), 12)))
	}
}

// tailLines keeps the last n log lines for the evidence file.
func tailLines(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
