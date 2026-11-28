package cli

// The LongMemEval queen asker: one question through a real queen chat
// session, built exactly the way POST /api/sessions builds one (same tool
// catalogue, same ChatRuntime wiring, same provider resolution), with the
// queen's clock frozen at the question's date.
//
// Parity-exempt: upstream drives this from the pytest fixture
// `tests/e2e/conftest.py::run_queen` (a Python `_QueenDriver` around
// `create_queen`); this port reuses the loader's own session assembly so the
// Go harness exercises the same production path as the HTTP API.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/longmemeval"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
	hivesession "github.com/shark2202/go_hive/go-hive/internal/session"
	"github.com/shark2202/go_hive/go-hive/internal/tools/builtin"
)

// EnvFakeNow freezes the dated surfaces the queen sees (message stamps and
// the get_current_time tool) at an RFC3339 instant. It exists for the
// LongMemEval harness, which must ask each question on its own date; unset,
// nothing changes.
const EnvFakeNow = "HIVE_FAKE_NOW"

const searchMessagesTool = "search_messages"
const searchTimelineTool = "search_timeline"

// LMERunnerOptions configures the production asker.
type LMERunnerOptions struct {
	Queen string
	// Model is the queen's answering model (empty uses the home's config).
	Model string
	// APIBase, when set, is written into each question home's configuration
	// so the provider resolves to the benchmark endpoint.
	APIBase string
	// APIKeyEnvVar names the environment variable holding the endpoint key,
	// written into the home's configuration (resolveChatProvider reads it).
	APIKeyEnvVar string
	// Out receives diagnostics; nil silences them.
	Out io.Writer
}

// LMERunner runs one question per fresh queen chat session.
type LMERunner struct {
	opts LMERunnerOptions
	out  io.Writer
}

// NewLMERunner builds the production asker.
func NewLMERunner(opts LMERunnerOptions) *LMERunner {
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	if strings.TrimSpace(opts.Queen) == "" {
		opts.Queen = "queen_technology"
	}
	return &LMERunner{opts: opts, out: out}
}

// Ask seeds nothing itself: the orchestrator has already written the
// question's history into req.HomeRoot. It boots one live queen session,
// delivers the question, waits for the turn to finish, and returns the
// transcript-derived answer.
func (r *LMERunner) Ask(ctx context.Context, req longmemeval.AskRequest) (longmemeval.Answer, error) {
	home, err := hivehome.ResolveAt(req.HomeRoot)
	if err != nil {
		return longmemeval.Answer{}, err
	}
	queenID := r.opts.Queen
	queenScope := chatSessionQueenScope(queenID)
	if err := ensureBenchmarkLLMConfig(home, r.opts.Model, r.opts.APIBase, r.opts.APIKeyEnvVar); err != nil {
		return longmemeval.Answer{}, err
	}

	sessionID, err := hivesession.NewStore("").GenerateID(time.Now())
	if err != nil {
		return longmemeval.Answer{}, err
	}
	sessionDir := chatSessionDir(home, queenScope, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return longmemeval.Answer{}, err
	}

	record := hiveserver.NewSession(sessionID, eventbus.NewEventBus())
	record.QueenName = queenScope
	record.QueenDir = sessionDir
	record.LoadedAt = float64(time.Now().UnixNano()) / 1e9

	provider, err := chatProviderFactory(home, r.opts.Model)
	if err != nil {
		return longmemeval.Answer{}, fmt.Errorf("queen provider for %s: %w", sessionID, err)
	}
	// Same fileops root as a live chat session: the session directory.
	toolbox := builtin.New(sessionDir)
	tools := newSessionToolCatalogue(sessionDir, toolbox, home)
	manager := hiveserver.NewSessionManager()
	onTurnStart := enableQueenSessionTools(tools, manager, sessionID, record, home)
	alwaysOnTools, deferredTools := tools.LazySurfaceDeps()
	runtime, err := hiveserver.NewChatRuntime(hiveserver.ChatRuntimeDeps{
		LLM:               provider,
		Tools:             tools.ModelDefs(),
		AlwaysOnToolNames: alwaysOnTools,
		DeferredTools:     deferredTools,
		Execute:           tools.Execute,
		ExecuteBlocks:     tools.ExecuteBlocks,
		Root:              sessionDir,
		Bus:               record.EventBus,
		SessionID:         sessionID,
		QueenName:         queenScope,
		HomeRoot:          home.Root,
		SkillsCatalog:     queenSkillsCatalogProvider(home, queenScope),
		OnTurnStart:       onTurnStart,
	})
	if err != nil {
		return longmemeval.Answer{}, fmt.Errorf("queen runtime for %s: %w", sessionID, err)
	}
	record.Chat = runtime
	record.LLM = provider
	record.QueenExecutor = &hiveserver.QueenExecutor{
		NodeRegistry: map[string]hiveserver.QueenNode{"queen": runtime},
	}
	defer func() { _ = runtime.Close() }()

	// The turn is over when the runtime publishes its execution boundary;
	// handlers run under the bus's own synchronization, so a single buffered
	// channel is enough for one question.
	turnDone := make(chan error, 1)
	sub := record.EventBus.Subscribe(
		[]eventbus.EventType{eventbus.EventExecutionCompleted, eventbus.EventExecutionFailed},
		func(event *eventbus.AgentEvent) {
			switch event.Type {
			case eventbus.EventExecutionFailed:
				message, _ := event.Data["error"].(string)
				if strings.TrimSpace(message) == "" {
					message = "queen turn failed"
				}
				select {
				case turnDone <- errors.New(message):
				default:
				}
			default:
				select {
				case turnDone <- nil:
				default:
				}
			}
		},
	)
	defer record.EventBus.Unsubscribe(sub)

	restoreClock := freezeQueenClock(req.When)
	defer restoreClock()

	started := time.Now()
	if err := runtime.Deliver(req.Question); err != nil {
		return longmemeval.Answer{}, fmt.Errorf("deliver question: %w", err)
	}
	select {
	case turnErr := <-turnDone:
		if turnErr != nil {
			return longmemeval.Answer{}, fmt.Errorf("queen turn: %w", turnErr)
		}
	case <-ctx.Done():
		preview := req.Question
		if len(preview) > 80 {
			preview = preview[:80]
		}
		return longmemeval.Answer{}, fmt.Errorf("queen did not finish within the question timeout on %q: %w", preview, ctx.Err())
	}
	seconds := time.Since(started).Seconds()

	parts, err := runtime.Transcript()
	if err != nil {
		return longmemeval.Answer{}, fmt.Errorf("read transcript: %w", err)
	}
	answer := transcriptAnswer(parts)
	answer.Seconds = seconds
	return answer, nil
}

// freezeQueenClock makes the dated surfaces the queen sees report when, and
// returns a restore function.
func freezeQueenClock(when time.Time) func() {
	previous, had := os.LookupEnv(EnvFakeNow)
	if when.IsZero() {
		return func() {}
	}
	_ = os.Setenv(EnvFakeNow, when.Format(time.RFC3339))
	return func() {
		if had {
			_ = os.Setenv(EnvFakeNow, previous)
			return
		}
		_ = os.Unsetenv(EnvFakeNow)
	}
}

// ensureBenchmarkLLMConfig merges the benchmark endpoint into a question
// home's configuration, leaving every other key as it was.
func ensureBenchmarkLLMConfig(home hivehome.Home, model, apiBase, keyEnv string) error {
	if model == "" && apiBase == "" && keyEnv == "" {
		return nil
	}
	path := home.ConfigurationFile()
	config := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &config)
	}
	section, _ := config["llm"].(map[string]any)
	if section == nil {
		section = map[string]any{}
	}
	if model != "" {
		section["model"] = model
	}
	if apiBase != "" {
		section["api_base"] = apiBase
	}
	if keyEnv != "" {
		section["api_key_env_var"] = keyEnv
	}
	config["llm"] = section
	if err := os.MkdirAll(home.Root, 0o755); err != nil {
		return err
	}
	return atomicfs.WriteJSON(path, config, 0o644)
}

var spillPattern = regexp.MustCompile(`Full result at: (.+?\.txt)`)

// transcriptAnswer reads what the queen did out of the persisted
// conversation: the final reply, every tool call, and the search calls with
// their full result text (spilled results followed to their file).
func transcriptAnswer(parts []map[string]any) longmemeval.Answer {
	answer := longmemeval.Answer{Turns: parts}
	searchCalls := map[string]longmemeval.SearchCall{}
	searchOrder := map[string]int{}
	for _, part := range parts {
		if role, _ := part["role"].(string); role != "assistant" {
			continue
		}
		if content, _ := part["content"].(string); strings.TrimSpace(content) != "" {
			answer.Text = content
		}
		for _, rawCall := range listOfMaps(part["tool_calls"]) {
			name := toolCallName(rawCall)
			if name == "" {
				continue
			}
			answer.Tools = append(answer.Tools, name)
			if name != searchMessagesTool && name != searchTimelineTool {
				continue
			}
			id, _ := rawCall["id"].(string)
			call := longmemeval.SearchCall{Tool: name, Input: toolCallInput(rawCall)}
			answer.Searches = append(answer.Searches, call)
			if id != "" {
				searchCalls[id] = call
				searchOrder[id] = len(answer.Searches) - 1
			}
		}
	}
	results := make([]string, len(answer.Searches))
	for _, part := range parts {
		if role, _ := part["role"].(string); role != "tool" {
			continue
		}
		id, _ := part["tool_use_id"].(string)
		index, ok := searchOrder[id]
		if !ok {
			continue
		}
		content, _ := part["content"].(string)
		results[index] = fullResultText(content)
	}
	answer.SearchResults = results
	return answer
}

func toolCallName(call map[string]any) string {
	function, _ := call["function"].(map[string]any)
	if function == nil {
		return ""
	}
	name, _ := function["name"].(string)
	return name
}

func toolCallInput(call map[string]any) map[string]any {
	function, _ := call["function"].(map[string]any)
	if function == nil {
		return map[string]any{}
	}
	arguments, _ := function["arguments"].(string)
	if strings.TrimSpace(arguments) == "" {
		return map[string]any{}
	}
	input := map[string]any{}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return map[string]any{"arguments": arguments}
	}
	return input
}

// fullResultText follows the spill pointer a large tool result carries.
func fullResultText(text string) string {
	match := spillPattern.FindStringSubmatch(text)
	if match == nil {
		return text
	}
	path := strings.TrimSpace(match[1])
	if data, err := os.ReadFile(filepath.Clean(path)); err == nil {
		return string(data)
	}
	return text
}

func listOfMaps(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		if typed, ok := value.([]map[string]any); ok {
			return typed
		}
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}
