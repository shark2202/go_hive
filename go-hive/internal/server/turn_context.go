package server

// Turn context: packages that put something in front of a queen before each of
// her turns.
//
// A package registers a factory with RegisterTurnContextProvider; `hive serve`
// starts what it returns when the package's section of configuration.json
// enables it:
//
//	{"turn_context": {"mycontext": {"enabled": true}}}
//
// Before each turn that answers a user message, every started package gets the
// queen's id and the message, and whatever text it returns reaches the queen as
// a <system-reminder> just ahead of the message (see agentloop's
// TurnContextReminderSource). Returning "" adds nothing. A package that is
// slower than TurnContextTimeout or fails is skipped for that turn; one that
// fails to start is logged and left out. Neither ever holds a turn or takes the
// server down.
//
// Upstream discovers packages through the `hive.turn_context` entry-point group;
// Go binaries have no entry points, so the registry below is the analogue — a
// linked-in package registers itself and the configuration gate is the same.
//
// Parity: framework/server/turn_context.py

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	// TurnContextEntryPointGroup is upstream's ENTRY_POINT_GROUP.
	TurnContextEntryPointGroup = "hive.turn_context"

	// turnContextStartTimeout is upstream's _START_TIMEOUT_S: start() should
	// hand slow work (loading a model, building an index) to a background task.
	turnContextStartTimeout = 30 * time.Second

	// DefaultTurnContextTimeout is upstream's CONTEXT_TIMEOUT_S: context is
	// fetched before the turn starts; past this the turn goes ahead without it.
	DefaultTurnContextTimeout = 5 * time.Second
)

// TurnContextTimeout is CONTEXT_TIMEOUT_S. Upstream's tests shorten the module
// constant; the port keeps it settable for the same reason.
var TurnContextTimeout = DefaultTurnContextTimeout

// errTurnContextTimeout marks the per-turn bound firing (upstream
// asyncio.TimeoutError).
var errTurnContextTimeout = errors.New("turn context timed out")

// TurnContext is what a turn-context factory returns.
//
// Parity: framework/server/turn_context.py::TurnContext
type TurnContext interface {
	// Start is called once when the package is enabled. Slow work belongs in
	// a background task: it is bounded by the start timeout.
	Start() error
	// Stop is called on runtime shutdown.
	Stop() error
	// Context returns the text to put in front of queenID before she answers
	// message, or "" for nothing. message is the user's message as the queen
	// receives it, including any prefix a channel adds. An error skips the
	// package for that turn (upstream raises).
	Context(queenID, message string) (string, error)
}

// TurnContextFactory builds a provider from its host.
type TurnContextFactory func(host TurnContextHost) (TurnContext, error)

// TurnContextHost is what Hive gives one turn-context package: its settings and
// a directory for its files.
//
// Parity: framework/server/turn_context.py::TurnContextHost
type TurnContextHost struct {
	Name string
	// Config is the package's section of configuration.json
	// (turn_context.<name>), exactly as read from disk.
	Config map[string]any

	home hivehome.Home
}

// StateDir is $HIVE_HOME/turn_context/<name>: where the package keeps its own
// files. It is created on first use, like upstream's state_dir property.
func (h TurnContextHost) StateDir() (string, error) {
	if strings.TrimSpace(h.home.Root) == "" {
		return "", errors.New("turn context state dir: empty hive home")
	}
	path := filepath.Join(h.home.Root, "turn_context", h.Name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return "", fmt.Errorf("turn context state dir %s: %w", path, err)
	}
	return path, nil
}

// turnContextRegistry is the installed-package half of upstream's entry-point
// discovery: the factories a linked-in package registered.
var turnContextRegistry = struct {
	mu        sync.Mutex
	factories map[string]TurnContextFactory
}{factories: map[string]TurnContextFactory{}}

// RegisterTurnContextProvider installs a factory under name — the Go analogue of
// a `hive.turn_context` entry point. An installed package starts only when
// configuration.json enables it (turn_context.<name>.enabled).
func RegisterTurnContextProvider(name string, factory TurnContextFactory) {
	name = strings.TrimSpace(name)
	if name == "" || factory == nil {
		return
	}
	turnContextRegistry.mu.Lock()
	defer turnContextRegistry.mu.Unlock()
	turnContextRegistry.factories[name] = factory
}

// UnregisterTurnContextProvider removes an installed factory (test teardown).
func UnregisterTurnContextProvider(name string) {
	turnContextRegistry.mu.Lock()
	defer turnContextRegistry.mu.Unlock()
	delete(turnContextRegistry.factories, name)
}

func lookupTurnContextFactory(name string) (TurnContextFactory, bool) {
	turnContextRegistry.mu.Lock()
	defer turnContextRegistry.mu.Unlock()
	factory, ok := turnContextRegistry.factories[name]
	return factory, ok
}

// installedTurnContextNames is upstream _installed(), sorted by name.
func installedTurnContextNames() []string {
	turnContextRegistry.mu.Lock()
	names := make([]string, 0, len(turnContextRegistry.factories))
	for name, factory := range turnContextRegistry.factories {
		if factory != nil {
			names = append(names, name)
		}
	}
	turnContextRegistry.mu.Unlock()
	sort.Strings(names)
	return names
}

// TurnContextSettings mirrors `get_hive_config().get("turn_context") or {}`: a
// missing or non-object section reads as empty.
//
// Parity: framework/server/turn_context.py::start_turn_context
func TurnContextSettings(config map[string]any) map[string]any {
	if section, ok := config["turn_context"].(map[string]any); ok && section != nil {
		return section
	}
	return map[string]any{}
}

// TurnContextSectionEnabled mirrors `isinstance(section, dict) and
// section.get("enabled")`: the section must be an object with a truthy enabled
// flag, so `{"voice": true}` does not enable anything.
func TurnContextSectionEnabled(section map[string]any) bool {
	if section == nil {
		return false
	}
	return pyTruthy(section["enabled"])
}

// turnContextEntry is one started package; entries keep start order, the order
// upstream's _running dict iterates in.
type turnContextEntry struct {
	name     string
	provider TurnContext
}

// TurnContextRuntime is the process's started turn-context packages.
//
// Parity: framework/server/turn_context.py (module-level _running)
type TurnContextRuntime struct {
	home hivehome.Home

	mu      sync.Mutex
	entries []turnContextEntry
}

// turnContextRuntimes keeps one runtime per hive home: upstream starts packages
// once per server process (app on_startup), not once per session, so a second
// caller for the same home gets the running instance.
var turnContextRuntimes = struct {
	mu     sync.Mutex
	byRoot map[string]*TurnContextRuntime
}{byRoot: map[string]*TurnContextRuntime{}}

// StartTurnContexts starts every installed package the hive home's
// configuration.json enables. Idempotent per home: the serve path calls it at
// boot and a session runtime may call it again.
//
// Parity: framework/server/turn_context.py::start_turn_context
func StartTurnContexts(home hivehome.Home) *TurnContextRuntime {
	if strings.TrimSpace(home.Root) == "" {
		return nil
	}
	turnContextRuntimes.mu.Lock()
	if running := turnContextRuntimes.byRoot[home.Root]; running != nil {
		turnContextRuntimes.mu.Unlock()
		return running
	}
	runtime := &TurnContextRuntime{home: home}
	turnContextRuntimes.byRoot[home.Root] = runtime
	turnContextRuntimes.mu.Unlock()

	runtime.start()
	return runtime
}

// StopTurnContexts stops the packages started for root and forgets the runtime,
// so a later StartTurnContexts starts fresh (upstream's on_shutdown).
func StopTurnContexts(root string) {
	turnContextRuntimes.mu.Lock()
	runtime := turnContextRuntimes.byRoot[root]
	delete(turnContextRuntimes.byRoot, root)
	turnContextRuntimes.mu.Unlock()
	if runtime != nil {
		runtime.Stop()
	}
}

// start runs the installed/enabled pass once.
func (rt *TurnContextRuntime) start() {
	settings := TurnContextSettings(HiveConfig(rt.home))
	for _, name := range installedTurnContextNames() {
		factory, ok := lookupTurnContextFactory(name)
		if !ok {
			continue
		}
		section, _ := settings[name].(map[string]any)
		if !TurnContextSectionEnabled(section) {
			log.Printf("turn context %q is installed but off: set turn_context.%s.enabled in configuration.json", name, name)
			continue
		}
		provider, err := factory(TurnContextHost{Name: name, Config: section, home: rt.home})
		if err != nil {
			log.Printf("turn context %q failed to start: %v", name, err)
			continue
		}
		if err := rt.startProvider(provider); err != nil {
			log.Printf("turn context %q failed to start: %v", name, err)
			continue
		}
		rt.mu.Lock()
		rt.entries = append(rt.entries, turnContextEntry{name: name, provider: provider})
		rt.mu.Unlock()
		log.Printf("turn context %q started", name)
	}
}

func (rt *TurnContextRuntime) startProvider(provider TurnContext) error {
	if provider == nil {
		return errors.New("turn context factory returned nil")
	}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()
		done <- provider.Start()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(turnContextStartTimeout):
		return fmt.Errorf("start took over %.0fs", turnContextStartTimeout.Seconds())
	}
}

// Context gathers what the started packages want queenID to see before she
// answers message: each package's text, trimmed and joined by a blank line, or
// "" when no package has anything to add. A package that takes over
// TurnContextTimeout or fails is skipped for this turn.
//
// Parity: framework/server/turn_context.py::context_for
func (rt *TurnContextRuntime) Context(queenID, message string) string {
	if rt == nil {
		return ""
	}
	rt.mu.Lock()
	entries := append([]turnContextEntry(nil), rt.entries...)
	rt.mu.Unlock()

	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		text, err := turnContextCall(entry.provider, queenID, message)
		if err != nil {
			if errors.Is(err, errTurnContextTimeout) {
				log.Printf("turn context %q took over %.0fs; this turn goes ahead without it", entry.name, TurnContextTimeout.Seconds())
			} else {
				log.Printf("turn context %q failed for %s: %v", entry.name, queenID, err)
			}
			continue
		}
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n\n")
}

// turnContextCall applies the per-turn bound and swallows a packaging failure:
// the reminder render receives "" instead.
func turnContextCall(provider TurnContext, queenID, message string) (string, error) {
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- outcome{err: fmt.Errorf("panic: %v", recovered)}
			}
		}()
		text, err := provider.Context(queenID, message)
		done <- outcome{text: text, err: err}
	}()
	select {
	case result := <-done:
		return result.text, result.err
	case <-time.After(TurnContextTimeout):
		return "", errTurnContextTimeout
	}
}

// ForSession is the turn-context provider a queen session's loop holds: it asks
// for the session's queen. The queen is read on each call, since a new session
// picks its queen with the first message. An empty name is the "default" queen.
//
// Parity: framework/server/turn_context.py::for_session
func (rt *TurnContextRuntime) ForSession(queenName func() string) func(string) string {
	if rt == nil {
		return nil
	}
	return func(message string) string {
		name := "default"
		if queenName != nil {
			if resolved := strings.TrimSpace(queenName()); resolved != "" {
				name = resolved
			}
		}
		return rt.Context(name, message)
	}
}

// Empty reports whether nothing started; such a runtime never contributes text.
func (rt *TurnContextRuntime) Empty() bool {
	if rt == nil {
		return true
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return len(rt.entries) == 0
}

// Names lists the started packages in start order.
func (rt *TurnContextRuntime) Names() []string {
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	names := make([]string, 0, len(rt.entries))
	for _, entry := range rt.entries {
		names = append(names, entry.name)
	}
	return names
}

// Stop stops the started packages, most recent first (upstream's popitem order)
// and drops them, so a stopped runtime contributes nothing further.
//
// Parity: framework/server/turn_context.py::stop_turn_context
func (rt *TurnContextRuntime) Stop() {
	if rt == nil {
		return
	}
	rt.mu.Lock()
	entries := rt.entries
	rt.entries = nil
	rt.mu.Unlock()

	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry.provider == nil {
			continue
		}
		if err := rt.stopProvider(entry.provider); err != nil {
			log.Printf("turn context %q failed to stop cleanly: %v", entry.name, err)
		}
	}
}

func (rt *TurnContextRuntime) stopProvider(provider TurnContext) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- fmt.Errorf("panic: %v", recovered)
			}
		}()
		done <- provider.Stop()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(turnContextStartTimeout):
		return fmt.Errorf("stop took over %.0fs", turnContextStartTimeout.Seconds())
	}
}
