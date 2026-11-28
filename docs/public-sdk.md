# Public Go SDK (`hive`)

- Package: `github.com/shark2202/go_hive/go-hive/hive` (`go_hive/go-hive/hive/`)
- Introduced for package-audit gap **S-1**: before this package every Go symbol lived under
  `internal/`, so a derivative project could not import the runtime.
- Relationship to the Python SDK: `core/hive/__init__.py` exports a curated set of framework
  primitives. This package mirrors that idea for Go: a curated facade over the existing runtime,
  not a second engine and not a blind alias of every internal symbol.

## Why `hive/` and not `pkg/hive/`

`hive/` matches the Python package name and the module's own domain vocabulary, keeps the import
short (`.../go-hive/hive`), and leaves `internal/` free to change. `pkg/hive` would add a segment
with no semantic value and would suggest the package is third-party. The directory is inside the
same Go module, so it may import `internal/` packages; consumers outside the module can import
`hive` but not `internal`.

## API surface

| Area | Entry points |
|---|---|
| Home / config | `Home` (`= hivehome.Home`), `ResolveHome`, `HomeAt`, `PreferredModel`, `NewConfiguredProvider` |
| Models | `Model` (`= llm.Completer`), `Completion`, `CompletionRequest`, `ChatMessage`, `ToolCall`, `NewProvider`, `NewAnthropicProvider`, `NewMockProvider` |
| Agent loop | `Loop`, `NewLoop`, `Loop.Run`, `Loop.ExecuteTurn`, `LoopConfig`, `DefaultLoopConfig`, `QueenLoopConfig`, `ColonyQueenLoopConfig`, `AgentSpec`, `NewAgentSpec`, `AgentContext`, `Conversation`, `NewConversation`, `TurnResult`, `ToolCallRecord`, `ParkReason` |
| Tools | `Tool`, `NewTool`, `Tool.Schema`, `ToolRegistry`, `NewToolRegistry`, `Register`, `RegisterFunc`, `ToolFunc`, `ToolParam`, `ToolUse`, `ToolResult`, `ToolExecutor`, `Attachment` |
| Colony / workers | `Colony`, `NewColony`, `ColonyOptions`, `Colony.Start/Serve/Stop`, `Spawn`, `Run`, `Workers`, `Worker`, `Task`, `RunOptions`, `Run`, `RunResult`, `WorkerInfo`, `WorkerResult`, `WorkerOutcome`, `WorkerStatus`, `WorkerDefinition`, `DefaultWorkerDefinition`, `WorkerLoopConfig`, `WorkerSystemPrompt`, `WorkerInputData` |
| Events | `EventBus`, `NewEventBus`, `Subscribe`, `Unsubscribe`, `Publish`, `History`, `WaitFor`, `Event`, `NewEvent`, `EventType`, `EventHandler`, `EventFilter`, `EventTypes`, `EventTypeByName` |
| Sessions / queens | `Session`, `ListSessions`, `ListColonySessions`, `ActiveColonySession`, `SessionDir`, `Queen`, `QueenProfile`, `ListQueens`, `LoadQueenProfile` |
| Memory / soul / skills | `MemoryFile`, `MemoryWriteOptions`, `GlobalMemoryDir`, `QueenMemoryDir`, `ColonyMemoryDir`, `ListMemories`, `ReadMemory`, `WriteMemory`, `Soul`, `SoulItem`, `SoulConfig`, `LoadSoul`, `SoulConfigFor`, `Skill`, `ListSkills`, `ParseSkillFile`, `SkillCatalog`, `NewSkillCatalog` |
| Goals / schemas | `Goal`, `GoalStatus`, `SuccessCriterion`, `Constraint`, `OrderedMap`, `Timestamp` |
| Playbooks / tracker | `RunPlaybookTool`, `RunWorkerTool`, `PlaybookControlTools`, `SavedPlaybooks`, `SanitizePlaybookName`, `ComputeHardTimeout`, `ListPlaybookRuns`, `PlaybookRun`, `TrackerBinding`, `TrackerFor`, `EnsureTracker`, `QueryTracker`, `ValidateTrackerSQL` |

### Patterns

- Pure value types and enums are aliases (`type LoopConfig = agentloop.LoopConfig`); their fields
  and methods are part of the public contract.
- Stateful or leaky internals are wrappers (`Loop`, `Conversation`, `EventBus`, `Colony`, `Worker`,
  `ToolRegistry`, `SkillCatalog`). Their methods accept and return only public types.
- Constructor helpers convert between the public `Tool` shape and the two internal tool shapes
  (`llm.ToolDef` and `tools.Tool`).
- `ToolResult` is the union of the loop result and the registry result. The loop ignores
  `ToolUseID`/`Attachments`; the registry fills them.

## Stability

- Names exported from `hive` follow semantic versioning on the module's `v1.0.x-go.N` release line:
  additive changes within a minor release; removals or incompatible renames only in a major.
- The internal packages stay internal and may change in any release. A derivative project must not
  import `.../internal/...` (Go enforces the internal rule across modules).
- The facade is documented with `// Parity:` references to the Python symbol it mirrors and
  `// Parity-exempt:` notes for Go-only packaging; `make parity` and `make parity-targets` enforce
  that those references resolve.

## Intentionally unsupported

- **The `hive serve` HTTP dashboard.** `internal/server.App` needs the CLI-owned session manager,
  channels, sentinel, MCP runtime, browser bridge and frontend assembly. Exposing a stable
  `Serve` would either freeze that whole graph or ship a half-working app. A derivative project
  that needs the dashboard runs `cmd/hive` (or replies on its own transport) for now.
- **Credentials.** There is no `get_api_key` equivalent. `NewConfiguredProvider` resolves the key
  inside the runtime and returns a `Model`; configuration maps that can contain secrets are not
  exported.
- **Advanced `AgentContext` hooks.** Dynamic tool/prompt/memory providers, observer seams,
  escalation sinks and the server-owned skill catalog are not in the curated context.
- **Live channel plugins, sentinel delivery, cloud sync, the browser bridge.**
- **The full `hive` CLI surface.** `hive/` does not wrap `internal/cli`.

## Remaining structural work for S-1 closure

1. **Submodule release tags.** The repository tags releases as `v1.0.27-go.1`, but the Go module
   root is the subdirectory `go_hive/go-hive`. Go modules require tags of the form
   `go_hive/go-hive/v1.0.27-go.1` for `go get` to resolve this module through the proxy. Until that
   tagging scheme exists, the package is usable from a workspace / `replace` directive but not
   `go get`-able from a clean module cache. This is a release-process change, not a code change.
2. **A real HTTP serve facade (optional).** Needs a public `App`/`SessionManager` contract or a
   CLI-owned `hive/serve` builder; currently documented as unsupported instead of leaking
   `internal/server.AppDeps`.
3. **Advanced agent hooks (optional).** Would need stable public provider interfaces instead of
   `any`-typed seams in `agentloop.AgentContext`.
4. **Behavioral cross-check.** The facade tests exercise the public surface; a full S-1 ecosystem
   check (an out-of-repo module importing `hive` through the proxy) depends on item 1.

No internal package had to be moved to build this facade.
