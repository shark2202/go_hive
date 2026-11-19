package server

import (
	"testing"

	"github.com/aden-hive/hive-go/internal/agentloop"
)

// Parity-exempt: Go chat sessions carry no success criteria, so the judge must
// stay off and a turn must cost exactly one LLM call (the queen's own).
func TestChatRuntimeWithoutCriteriaNeverRunsJudge(t *testing.T) {
	root := t.TempDir()
	runtime, model := newTestChatRuntime(t, root)

	if agentloop.HasSuccessCriteria(runtime.agentCtx.AgentSpec) {
		t.Fatalf("chat AgentSpec = %#v, want no success criteria", runtime.agentCtx.AgentSpec)
	}
	if runtime.loop.Judge != nil {
		t.Fatalf("chat loop Judge = %#v, want nil", runtime.loop.Judge)
	}

	if err := runtime.Deliver("hello queen"); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	waitForAssistantParts(t, runtime, 1)
	if got := model.requestCount(); got != 1 {
		t.Fatalf("LLM calls = %d, want exactly the chat turn (no judge call)", got)
	}
}
