// Package hive is the public Go SDK for Hive, the agent runtime.
//
// It exposes the primitives a derivative project needs to embed Hive without
// importing any of the runtime's internal packages:
//
//   - a runnable agent loop (NewLoop, Conversation, AgentContext, ToolExecutor)
//   - a colony host that starts workers, serves tasks and publishes events
//     (NewColony, Colony.Serve, Colony.Run)
//   - session, queen-profile and tool-registry construction
//   - event subscription (EventBus)
//   - memory, soul and skill accessors
//   - playbook and tracker primitives
//
// # Example
//
//	home, err := hive.ResolveHome()
//	if err != nil {
//		return err
//	}
//	model, err := hive.NewConfiguredProvider(home, "")
//	if err != nil {
//		return err
//	}
//	loop := hive.NewLoop(hive.DefaultLoopConfig(), func(use hive.ToolUse) (hive.ToolResult, error) {
//		return hive.ToolResult{Content: "It is raining."}, nil
//	})
//	conv := hive.NewConversation("Look outside, then answer.")
//	result, err := loop.ExecuteTurn(ctx, &hive.AgentContext{
//		AgentID: "scout",
//		AgentSpec: hive.NewAgentSpec("scout", "Scout", "Looks around"),
//		LLM:     model,
//	}, conv, nil)
//
// # Stability
//
// Names exported from package hive follow semantic versioning on the module's
// v1.0.x-go.N release line: additive changes only within a minor release, and
// removals or incompatible renames only in a major release. This mirrors the
// compatibility promise of the Python public surface (core/hive/__init__.py),
// which is what derivative projects are told to import.
//
// Everything under internal/ is Hive's implementation and may change in any
// release. The SDK may re-export a small, curated set of internal value types
// (for example Home, LoopConfig and Model aliases); those aliases are part of
// the public contract, but the internal packages themselves are not.
//
// # What is intentionally unsupported
//
// The SDK is a facade over the existing runtime, not a second engine. The
// following are deliberately out of scope:
//
//   - Starting the full `hive serve` HTTP dashboard. That assembly owns the
//     CLI session manager, channels, sentinel, MCP runtime and browser bridge;
//     it lives in internal/cli and cmd/hive.
//   - Reading or returning credentials. There is no get_api_key equivalent:
//     NewConfiguredProvider resolves keys inside the runtime and returns only
//     a usable model. Configuration maps that can contain secrets are not
//     exported.
//   - Advanced AgentContext hooks (dynamic tool/prompt/memory providers,
//     observer seams, escalation sinks). A derivative project that needs them
//     must use internal packages in its own module, which is unsupported.
//   - Live channel plugins, sentinel delivery, cloud sync and the browser
//     bridge. See the parity report for their registered gaps.
//
// # Parity
//
// Hive is a port of a Python runtime; the SDK's shape follows the Python
// public package where one exists. Each exported type carries a // Parity:
// reference to the Python symbol it mirrors, or a // Parity-exempt: note when
// it is Go-only packaging (a wrapper, a constructor, or a convenience that
// has no Python counterpart).
//
// Parity: core/hive/__init__.py
package hive
