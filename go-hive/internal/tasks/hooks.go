package tasks

// Parity: framework/tasks/hooks.py

import (
	"errors"
	"fmt"
	"log"
	"sync"
)

const (
	HookTaskCreated   = "task_created"
	HookTaskCompleted = "task_completed"
)

type BlockingHookError struct {
	Message string
}

func (e *BlockingHookError) Error() string { return e.Message }

func NewBlockingHookError(msg string) *BlockingHookError {
	return &BlockingHookError{Message: msg}
}

func AsBlockingHookError(err error) (*BlockingHookError, bool) {
	var target *BlockingHookError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

type TaskHookContext struct {
	Event     string
	SessionID string
	Task      *TaskRecord
	AgentID   *string
	Metadata  map[string]any
}

type HookFn func(TaskHookContext) error

var (
	hookRegistryMu sync.Mutex
	hookRegistry   = map[string][]HookFn{
		HookTaskCreated:   {},
		HookTaskCompleted: {},
	}
)

func RegisterHook(event string, fn HookFn) error {
	hookRegistryMu.Lock()
	defer hookRegistryMu.Unlock()
	fns, ok := hookRegistry[event]
	if !ok {
		return fmt.Errorf("Unknown hook event: %s", reprString(event))
	}
	hookRegistry[event] = append(fns, fn)
	return nil
}

func ClearHooks(event string) {
	hookRegistryMu.Lock()
	defer hookRegistryMu.Unlock()
	if event == "" {
		for k := range hookRegistry {
			hookRegistry[k] = nil
		}
		return
	}
	if _, ok := hookRegistry[event]; ok {
		hookRegistry[event] = nil
	}
}

func RunTaskHooks(event string, ctx TaskHookContext) error {
	hookRegistryMu.Lock()
	hooks := append([]HookFn(nil), hookRegistry[event]...)
	hookRegistryMu.Unlock()
	if len(hooks) == 0 {
		return nil
	}
	ctx.Event = event
	for _, hook := range hooks {
		err := hook(ctx)
		if err == nil {
			continue
		}
		if _, ok := AsBlockingHookError(err); ok {
			return err
		}
		log.Printf("Non-blocking hook failed for %s: %v", event, err)
	}
	return nil
}

func reprString(s string) string {
	return "'" + s + "'"
}
