"""Curated contract data for go_hive/scripts/freeze_env_config_contract.py.

Every reader is anchored by `{"file", "contains"}`: the generator resolves the
first line containing that literal and FAILS the freeze when it is missing.
That keeps this table honest as the code moves (line numbers are recomputed;
a rename or deletion is a hard error, not a silent stale doc).

Status vocabulary
-----------------
aligned                 both sides read the key with the same type/default/semantics
default_differs         default value differs
type_differs            accepted JSON type differs
parse_differs           same type but coercion/invalid-value handling differs
go_partial              go has the key (REST/CRUD or partial path) but the runtime
                        reader is missing or not wired
python_only             no go reader at all
go_only                 go reads a section/key the Python reference does not
unread                  written by installers/docs but read by neither runtime
"""

from __future__ import annotations

HEAD = "bd5041862d5df219a029b190141dda3db8c7eeab"

# ---------------------------------------------------------------------------
# configuration.json keys
# ---------------------------------------------------------------------------

CONFIG_KEYS: list[dict] = [
    # ----- top-level feature switches -------------------------------------
    {
        "path": "adaptive_tool_budget", "type": "bool", "default": True,
        "env": "HIVE_ADAPTIVE_TOOL_BUDGET",
        "py": [{"file": "core/framework/config.py", "contains": "raw = os.environ.get(\"HIVE_ADAPTIVE_TOOL_BUDGET\")"},
               {"file": "core/framework/config.py", "contains": "value = get_hive_config().get(\"adaptive_tool_budget\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "if value, ok := envFeatureFlag(\"HIVE_ADAPTIVE_TOOL_BUDGET\"); ok {"},
               {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "return boolConfigFlag(home, \"adaptive_tool_budget\", true)"}],
        "status": "aligned",
        "notes": "env > configuration.json > default(true); empty env string falls through to the file on both sides.",
    },
    {
        "path": "email_senders", "type": "bool", "default": False, "env": "HIVE_EMAIL_SENDERS",
        "py": [{"file": "core/framework/config.py", "contains": "raw = os.environ.get(\"HIVE_EMAIL_SENDERS\")"},
               {"file": "core/framework/config.py", "contains": "value = get_hive_config().get(\"email_senders\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "if value, ok := envFeatureFlag(\"HIVE_EMAIL_SENDERS\"); ok {"},
               {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "return boolConfigFlag(home, \"email_senders\", false)"}],
        "status": "aligned",
        "notes": "env > file > default(false); sync_email_senders_env publishes the resolved flag back to the process env on both sides.",
    },
    {
        "path": "stall_check", "type": "bool", "default": False, "env": "HIVE_STALL_CHECK",
        "py": [{"file": "core/framework/config.py", "contains": "raw = os.environ.get(\"HIVE_STALL_CHECK\")"},
               {"file": "core/framework/config.py", "contains": "return bool(get_hive_config().get(\"stall_check\", False))"}],
        "go": [{"file": "go_hive/go-hive/internal/server/hive_config.go", "contains": "if raw, ok := os.LookupEnv(\"HIVE_STALL_CHECK\"); ok {"},
               {"file": "go_hive/go-hive/internal/server/hive_config.go", "contains": "return pyTruthy(HiveConfig(home)[\"stall_check\"])"}],
        "status": "aligned",
        "notes": "empty/whitespace env falls through to the file; falsey = (0,false,no,off).",
    },
    {
        "path": "memory_timeline", "type": "bool", "default": False, "env": "HIVE_MEMORY_TIMELINE",
        "py": [{"file": "core/framework/config.py", "contains": "raw = os.environ.get(\"HIVE_MEMORY_TIMELINE\")"},
               {"file": "core/framework/config.py", "contains": "value = get_hive_config().get(\"memory_timeline\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "if value, ok := envFeatureFlag(\"HIVE_MEMORY_TIMELINE\"); ok {"},
               {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "return boolConfigFlag(home, \"memory_timeline\", false)"}],
        "status": "aligned",
        "notes": "same env > file > default(false) chain.",
    },
    {
        "path": "gcu_enabled", "type": "bool", "default": True, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "return get_hive_config().get(\"gcu_enabled\", True)"}],
        "go": [{"file": "go_hive/go-hive/internal/cli/browser_supervisor.go", "contains": "if enabled, present := config[\"gcu_enabled\"].(bool); present {"}],
        "status": "aligned",
        "notes": "file only. Go additionally lets HIVE_BROWSER_BRIDGE force the bridge on/off (documented operator override, not a parity break).",
    },
    {
        "path": "gcu_viewport_scale", "type": "number (0.1-1.0)", "default": 0.8, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "scale = get_hive_config().get(\"gcu_viewport_scale\", 0.8)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/hive_config.go", "contains": "scale, ok := pythonNumber(HiveConfig(home)[\"gcu_viewport_scale\"])"}],
        "status": "aligned",
        "notes": "out-of-range / non-number -> 0.8; Python's isinstance(True, int) quirk (true -> 1.0) is replicated in Go.",
    },
    {
        "path": "created_at", "type": "string (ISO time)", "default": None, "env": None,
        "py": [{"file": "quickstart.sh", "contains": "config[\"created_at\"] = created_at"}],
        "go": [],
        "status": "unread",
        "notes": "unread: written by the installers (quickstart.sh/quickstart.ps1), read by neither runtime — a write-once install timestamp, not a runtime knob. Go preserves it on every configuration write; TestConfigWritesPreserveChannelsAndCreatedAt pins that.",
    },

    # ----- llm slot -------------------------------------------------------
    {
        "path": "llm.provider", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"provider\") and llm.get(\"model\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "provider, hasProvider := llmSection[\"provider\"]"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotModel("}],
        "status": "aligned",
        "notes": "both require provider+model before the slot counts as configured; the session builder joins them as provider/model (get_preferred_model shape), and without a usable pair both fall back to the same hardcoded default pair (anthropic/claude-sonnet-5-5).",
    },
    {
        "path": "llm.model", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "model = str(llm[\"model\"]).strip()"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "model, hasModel := llmSection[\"model\"]"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotModel("}],
        "status": "aligned",
        "notes": "openrouter/<id> prefix is stripped on both sides before the provider/model join. The Go session builder (MainSlotProvider → MainSlotModel) joins the pair the same way; a model request override is used as given, as RuntimeConfig(model=...) does.",
    },
    {
        "path": "llm.api_key", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "literal = llm.get(\"api_key\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "if pyTruthy(llmSection[\"api_key\"]) {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if literal, ok := section[\"api_key\"].(string); ok && literal != \"\" {"}],
        "status": "aligned",
        "notes": "a literal key beats ambient sources on both sides. Go resolves the runtime key with the same order in MainSlotKey (endpoint → pool → literal → subscriptions → credential store → api_key_env_var → provider env); the status and hot-swap routes mirror it. Pinned by TestMainSlotKeyFollowsTheUpstreamOrder.",
    },
    {
        "path": "llm.api_keys", "type": "list[string]", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "keys = llm.get(\"api_keys\")"},
               {"file": "core/framework/llm/key_pool.py", "contains": "class KeyPool:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "if pool, ok := llmSection[\"api_keys\"].([]any); ok && len(pool) > 0 {"},
               {"file": "go_hive/go-hive/internal/llm/keypool.go", "contains": "type KeyPool struct {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotAPIKeys("},
               {"file": "go_hive/go-hive/internal/llm/transport.go", "contains": "if p.KeyPool != nil {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotKeyPool("}],
        "status": "aligned",
        "notes": "empty strings are filtered; non-list is ignored on both sides. Go installs the pool as llm.KeyPool on the session provider (first key for single-key callers, rotation on 2+) and parks a 429ed key for 60s, like Python's KeyPool. Boundary: a PUT-rebuilt transport takes the route-resolved key until the next session build installs the pool again.",
    },
    {
        "path": "llm.api_base", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"api_base\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "\"api_base\":               llmSection[\"api_base\"],"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotAPIBase("}],
        "status": "aligned",
        "notes": "subscription endpoints win first (codex/kimi/antigravity), then the configured api_base, then openrouter's https://openrouter.ai/api/v1, on both sides. Pinned by TestMainSlotAPIBasePrefersSubscriptionEndpointsThenTheSection.",
    },
    {
        "path": "llm.api_key_env_var", "type": "string", "default": None, "env": "name read from the file",
        "py": [{"file": "core/framework/config.py", "contains": "api_key_env_var = llm.get(\"api_key_env_var\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "if envValue := llmSection[\"api_key_env_var\"]; pyTruthy(envValue) {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if envName, ok := section[\"api_key_env_var\"].(string); ok && strings.TrimSpace(envName) != \"\" {"}],
        "status": "aligned",
        "notes": "the env var named here is a fallback AFTER the credential store for Python's get_api_key; Go's MainSlotKey (session build) and llm_config (status/hot swap) follow the same order.",
    },
    {
        "path": "llm.max_tokens", "type": "int", "default": 8192, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if \"max_tokens\" in llm:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "\"max_tokens\":             llmSection[\"max_tokens\"],"},
               {"file": "go_hive/go-hive/internal/server/chat_memory_wiring.go", "contains": "func chatTurnMaxTokens("},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func LLMMaxTokens("}],
        "status": "aligned",
        "notes": "missing -> model catalog -> DEFAULT_MAX_TOKENS=8192. Go consumes it for the queen turn budget (chatTurnMaxTokens → agentCtx.MaxTokens) with the same fallback chain, not only in the REST readback; the fallback chain and the built session's MaxTokens are pinned by TestChatTurnMaxTokensFollowsTheMainSlot / TestChatRuntimeBudgetsTheQueenTurnFromTheMainSlot.",
    },
    {
        "path": "llm.max_context_tokens", "type": "int", "default": 32000, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if \"max_context_tokens\" in llm:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "\"max_context_tokens\":     llmSection[\"max_context_tokens\"],"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "mainSection := configSection(home, \"llm\")"}],
        "status": "aligned",
        "notes": "missing -> catalog -> 32000 (DEFAULT_MAX_CONTEXT_TOKENS). A queen without her own section budgets her loop by this value first (QueenMaxContextTokens falls through the main slot), matching get_queen_max_context_tokens.",
    },
    {
        "path": "llm.aux_max_tokens", "type": "int", "default": "min(8192, max_tokens)", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if \"aux_max_tokens\" in llm:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func AuxMaxTokens("}],
        "status": "aligned",
        "notes": "both read the JSON int, else min(8192, llm.max_tokens). Wired: serve installs it process-wide with llm.SetAuxMaxTokens, and the recall/judge/sentinel/skill-test call sites read llm.AuxMaxTokens(). Pinned by TestConfiguredAuxMaxTokensReachesTheJudge.",
    },
    {
        "path": "llm.endpoint", "type": "string (endpoint name)", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "name = section.get(\"endpoint\") if isinstance(section, dict) else None"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "if name, ok := llmSection[\"endpoint\"].(string); ok && name != \"\" {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if name, ok := section[\"endpoint\"].(string); ok && strings.TrimSpace(name) != \"\" {"}],
        "status": "aligned",
        "notes": "named endpoint must exist in model_endpoints; its stored credential (or its never-ambient placeholder) wins over ambient keys on both sides. Go's MainSlotKey checks it first, exactly like _endpoint_key.",
    },
    {
        "path": "llm.extra_body", "type": "object", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "extra_body = section.get(\"extra_body\")"}],
        "go": [{"file": "go_hive/go-hive/internal/llm/request_shape.go", "contains": "if extraBody, ok := llmSection[\"extra_body\"].(map[string]any); ok && len(extraBody) > 0 {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotExtraKwargs("}],
        "status": "aligned",
        "notes": "non-object/empty -> not sent; copied verbatim into every request body. Go's MainSlotProvider installs it as the provider's extra kwargs at session build (upstream get_llm_extra_kwargs does the same), and a section PUT carries it to the rebuilt transport.",
    },
    {
        "path": "llm.num_ctx", "type": "int", "default": 16384, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "return {\"num_ctx\": llm.get(\"num_ctx\", 16384)}"}],
        "go": [{"file": "go_hive/go-hive/internal/llm/request_shape.go", "contains": "return map[string]any{\"num_ctx\": numCtxOr(llmSection[\"num_ctx\"], 16384)}"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "func MainSlotExtraKwargs("}],
        "status": "aligned",
        "notes": "only sent for provider=ollama on both sides. Go reads a JSON integer (json.Number included) and defaults to 16384; a non-integer/absent value keeps the default.",
    },
    {
        "path": "llm.use_claude_code_subscription", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"use_claude_code_subscription\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/llm/request_shape.go", "contains": "if truthy(llmSection[\"use_claude_code_subscription\"]) && apiKey != \"\" {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if pyTruthy(section[\"use_claude_code_subscription\"]) {"}],
        "status": "aligned",
        "notes": "adds Bearer auth headers; Go resolves the token from ~/.claude/.credentials.json at session build (upstream also checks the macOS Keychain and refreshes expiry; the port reads the CLI-refreshed file and does not refresh). The config gate and header shape match.",
    },
    {
        "path": "llm.use_codex_subscription", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"use_codex_subscription\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/llm/request_shape.go", "contains": "if truthy(llmSection[\"use_codex_subscription\"]) && apiKey != \"\" {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if pyTruthy(section[\"use_codex_subscription\"]) {"}],
        "status": "aligned",
        "notes": "adds ChatGPT backend headers (Authorization/User-Agent/ChatGPT-Account-Id) on both sides; Go resolves the token from ~/.codex/auth.json and the account id from tokens.account_id (else the JWT claim) at session build.",
    },
    {
        "path": "llm.use_kimi_code_subscription", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"use_kimi_code_subscription\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "\"use_kimi_code_subscription\""},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if pyTruthy(section[\"use_kimi_code_subscription\"]) {"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "return llm.KimiAPIBase"}],
        "status": "aligned",
        "notes": "runtime wired end-to-end: MainSlotKey resolves the token from ~/.kimi/config.toml and MainSlotAPIBase returns https://api.kimi.com/coding, so a session on this flag authenticates and routes like Python's get_api_key/get_api_base branches (the kimi/ model prefix routes through the Anthropic Messages transport). Pinned by TestMainSlotProviderReadsTheSubscriptionTokens.",
    },
    {
        "path": "llm.use_antigravity_subscription", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if llm.get(\"use_antigravity_subscription\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "case \"antigravity\":"},
               {"file": "go_hive/go-hive/internal/server/main_slot.go", "contains": "if pyTruthy(section[\"use_antigravity_subscription\"]) {"}],
        "status": "go_partial",
        "notes": "partial: Python's flag swaps the whole session to AntigravityProvider (Gemini-style cloudcode API, OAuth refresh) in session_manager.build_llm; that provider is not ported. Go consumes the flag as far as it can — MainSlotKey reads the account file's access token for get_api_key's branch and MainSlotProvider refuses the session with an explicit error instead of silently sending an OpenAI request with no key. Structural reason and alternative verification are recorded in contracts/config-support-ledger.json (TestMainSlotProviderRefusesAntigravitySubscription).",
    },
    {
        "path": "llm.antigravity_client_id", "type": "string", "default": None, "env": "ANTIGRAVITY_CLIENT_ID",
        "py": [{"file": "core/framework/config.py", "contains": "cfg_val = get_hive_config().get(\"llm\", {}).get(\"antigravity_client_id\")"}],
        "go": [
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func AntigravityClientID("}
        ],
        "status": "go_partial",
        "notes": "reader only: ANTIGRAVITY_CLIENT_ID, then llm.antigravity_client_id (no public-source fetch), tested by TestAntigravityClientCredentials. The only upstream consumers are the OAuth refresh flows inside the unported AntigravityProvider (framework/llm/antigravity.py, framework/loader/agent_loader.py::_refresh_antigravity_token); the port reads the pre-issued access token from antigravity-accounts.json and has no refresh to hand a client id to. Alternative verification: the reader's env-then-file precedence is pinned; no REST/CRUD path is claimed as consumption.",
    },
    {
        "path": "llm.antigravity_client_secret", "type": "string", "default": None, "env": "ANTIGRAVITY_CLIENT_SECRET",
        "py": [{"file": "core/framework/config.py", "contains": "get(\"llm\", {}).get(\"antigravity_client_secret\")"}],
        "go": [
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func AntigravityClientSecret("}
        ],
        "status": "go_partial",
        "notes": "reader only: ANTIGRAVITY_CLIENT_SECRET, then llm.antigravity_client_secret (no public-source fetch), tested by TestAntigravityClientCredentials. Same structural reason as the client id: the only consumers are the unported AntigravityProvider's OAuth refresh flows, so there is no request path to hand a client secret to.",
    },

    # ----- worker_llm slot ------------------------------------------------
    {
        "path": "worker_llm.provider", "type": "string", "default": "inherit llm", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "worker_llm = get_hive_config().get(\"worker_llm\", {})"},
               {"file": "core/framework/server/session_manager.py", "contains": "def build_worker_llm(self):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "var RoleSections = []string{\"llm\", \"worker_llm\", \"vision_fallback\", \"judge\"}"},
               {"file": "go_hive/go-hive/internal/colony/runtime.go", "contains": "worker_llm separately"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "status": "aligned",
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.model", "type": "string", "default": "inherit llm", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if worker_llm.get(\"provider\") and worker_llm.get(\"model\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "status": "aligned",
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.api_key", "type": "string", "default": "inherit", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "literal = worker_llm.get(\"api_key\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "status": "aligned",
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.api_base", "type": "string", "default": "inherit", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if worker_llm.get(\"api_base\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.api_key_env_var", "type": "string", "default": None, "env": "name read from the file",
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "api_key_env_var = worker_llm.get(\"api_key_env_var\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.max_tokens", "type": "int", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if worker_llm and \"max_tokens\" in worker_llm:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerMaxTokens("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.max_context_tokens", "type": "int", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if worker_llm and \"max_context_tokens\" in worker_llm:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.endpoint", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "endpoint_key = _endpoint_key(worker_llm)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.extra_body", "type": "object", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "extra_body = section_extra_body(worker_llm)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },
    {
        "path": "worker_llm.num_ctx", "type": "int", "default": 16384, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "return {\"num_ctx\": worker_llm.get(\"num_ctx\", 16384)}"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "\"worker_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func workerChatProvider("},
        ],
        "notes": "wired: workerChatProvider builds the colony's dedicated WorkerLLM and the colony's worker contexts run on it (buildContext → workerCompleter); key/base/endpoint/extra_body/num_ctx resolve through SectionAPIKey/SectionAPIBase/sectionExtraKwargs, max_context_tokens budgets the worker loop, and max_tokens has a reader (WorkerMaxTokens) with no call site on either side — upstream's get_worker_max_tokens has none either. A PUT is new-session effective, matching upstream's _write_role_section (only role=llm hot-swaps running sessions).",
    },

    # ----- vision_fallback slot ------------------------------------------
    {
        "path": "vision_fallback.provider", "type": "string", "default": "inherit llm", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "vision = get_hive_config().get(\"vision_fallback\", {})"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "var modelRoleSlots = []string{\"llm\", \"worker_llm\", \"vision_fallback\"}"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "status": "aligned",
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.model", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if vision.get(\"provider\") and vision.get(\"model\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.api_key", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if vision.get(\"api_key\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.api_base", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if vision.get(\"api_base\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.api_key_env_var", "type": "string", "default": None, "env": "name read from the file",
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "api_key_env_var = vision.get(\"api_key_env_var\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.max_tokens", "type": "int", "default": "aux_max_tokens", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/agent_loop/internals/vision_fallback.py", "contains": "\"max_tokens\": int(get_hive_config().get(\"vision_fallback\", {}).get(\"max_tokens\") or get_aux_max_tokens())"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },
    {
        "path": "vision_fallback.endpoint", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "endpoint_key = _endpoint_key(vision)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_models.go", "contains": "\"vision_fallback\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func VisionFallbackSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func visionCaptionerFor("},
        ],
        "notes": "wired: visionCaptionerFor builds a resolver-backed captioner and the slot is re-read on every caption, so a PUT reaches the next tool image without a session rebuild (upstream reads the config per caption_tool_image call); key/base/endpoint resolve through SectionAPIKey/SectionAPIBase and max_tokens falls back to the aux budget. For a text-only main model the agent loop splices the caption (viewport coordinates, crop remapped) into the tool result and drops the image blocks.",
    },

    # ----- queen_llm ------------------------------------------------------
    {
        "path": "queen_llm.<queen_id>.provider", "type": "string", "default": "inherit llm", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "def get_queen_llm_section(queen_id: str | None) -> dict[str, Any] | None:"},
               {"file": "core/framework/server/queen_models.py", "contains": "section = get_queen_llm_section(queen_id)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "if sections, ok := config[\"queen_llm\"].(map[string]any); ok {"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "status": "aligned",
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.model", "type": "string", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "section = sections.get(queen_id)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "if strings.TrimSpace(queenStr(object[\"provider\"])) == \"\" || strings.TrimSpace(queenStr(object[\"model\"])) == \"\" {"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.api_key", "type": "string", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if section.get(\"api_key\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "queenStr(object[\"api_key\"])"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.api_base", "type": "string", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if section.get(\"api_base\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "apiBase := strings.TrimSpace(queenStr(object[\"api_base\"]))"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.api_key_env_var", "type": "string", "default": None, "env": "name read from the file",
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "env_var = section.get(\"api_key_env_var\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "if envName := queenStr(object[\"api_key_env_var\"]); envName != \"\" {"},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.max_context_tokens", "type": "int", "default": "inherit llm", "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "if \"max_context_tokens\" in section:"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "\"queen_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.endpoint", "type": "string", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "endpoint_key = _endpoint_key(section)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "\"queen_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },
    {
        "path": "queen_llm.<queen_id>.extra_body", "type": "object", "default": None, "env": None,
        "status": "aligned",
        "py": [{"file": "core/framework/config.py", "contains": "extra_body = section.get(\"extra_body\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens.go", "contains": "\"queen_llm\""},
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func QueenLLMSection("},
               {"file": "go_hive/go-hive/internal/cli/loader_model_slots.go", "contains": "func queenAwareChatProvider("},
        ],
        "notes": "wired at session start and on PUT: queenAwareChatProvider builds her provider from this section (the colony host uses her model for her colony), and a queen_llm write rebuilds the running sessions' transport in place (ProviderSlot → NewHTTPProvider, the constructor the session started on). Hot-swap matrix vs upstream: Python mutates the shared provider and her next model call uses it; Go pins the provider for the turn in flight and the next turn resolves the replacement — a deliberate no-tearing variant, recorded in go_hive/docs/configuration.md §3.8. max_context_tokens budgets her loop; endpoint/api_key_env_var/extra_body resolve through the shared Section* readers.",
    },

    # ----- judge ----------------------------------------------------------
    {
        "path": "judge.protocol", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/server/judge.py", "contains": "proto = protocols.get(str(s.get(\"protocol\") or \"\"))"}],
        "go": [{"file": "go_hive/go-hive/internal/server/judge.go", "contains": "name := pyStrOr(slot[\"protocol\"], \"\")"}],
        "status": "aligned",
        "notes": "only typesafe exists; an unknown protocol logs and returns no answer on both sides.",
    },
    {
        "path": "judge.api_base", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/server/judge.py", "contains": "if not isinstance(s, dict) or not s.get(\"api_base\") or not s.get(\"api_key\"):"}],
        "go": [{"file": "go_hive/go-hive/internal/server/judge.go", "contains": "if !pyTruthy(slot[\"api_base\"]) || !pyTruthy(slot[\"api_key\"]) {"}],
        "status": "aligned",
        "notes": "both api_base and api_key are required for the slot to count.",
    },
    {
        "path": "judge.model", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/server/judge.py", "contains": "\"model\": s.get(\"model\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/judge.go", "contains": "\"model\":     slot[\"model\"],"}],
        "status": "aligned", "notes": "sent as-is in the /systemone body.",
    },
    {
        "path": "judge.api_key", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/server/judge.py", "contains": "headers={\"Authorization\": f\"Bearer {s['api_key']}\"}"}],
        "go": [{"file": "go_hive/go-hive/internal/server/judge.go", "contains": "slot[\"api_key\"]"}],
        "status": "aligned", "notes": "literal key only; no env indirection on either side.",
    },
    {
        "path": "judge.threshold", "type": "number", "default": 0.5, "env": None,
        "py": [{"file": "core/framework/server/stall_check.py", "contains": "threshold = float(slot.get(\"threshold\", 0.5))"}],
        "go": [{"file": "go_hive/go-hive/internal/server/stall_check.go", "contains": "threshold := 0.5"}],
        "status": "aligned", "notes": "stall check only; invalid value falls back to 0.5.",
    },

    # ----- memory_reflection ---------------------------------------------
    {
        "path": "memory_reflection.every_turns", "type": "int >= 1", "default": 6, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "every_turns: int = 6"}],
        "go": [{"file": "go_hive/go-hive/internal/server/chat_memory_wiring.go", "contains": "cfg.EveryTurns = positive(\"every_turns\", cfg.EveryTurns)"}],
        "status": "aligned", "notes": "int (bool rejected), >=1; invalid keeps 6.",
    },
    {
        "path": "memory_reflection.long_every", "type": "int >= 1", "default": 4, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "long_every: int = 4"}],
        "go": [{"file": "go_hive/go-hive/internal/server/chat_memory_wiring.go", "contains": "cfg.LongEvery = positive(\"long_every\", cfg.LongEvery)"}],
        "status": "aligned", "notes": "same validation as every_turns.",
    },
    {
        "path": "memory_reflection.model", "type": "\"main\" | \"worker\"", "default": "main", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "model = raw.get(\"model\", default.model)"}],
        "go": [{"file": "go_hive/go-hive/internal/server/chat_memory_wiring.go", "contains": "if model, ok := section[\"model\"].(string); ok && (model == \"main\" || model == \"worker\") {"}],
        "status": "aligned", "notes": "other strings keep \"main\".",
    },

    # ----- queen_mail -----------------------------------------------------
    {
        "path": "queen_mail.enabled", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "enabled=raw.get(\"enabled\") is True,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "config.Enabled = section[\"enabled\"] == true"}],
        "status": "aligned", "notes": "only JSON true enables it; 1/\"true\" do not.",
    },
    {
        "path": "queen_mail.queens", "type": "list[string]", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "queens=tuple(q for q in queens if isinstance(q, str) and q) if isinstance(queens, list) else None,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "if rawQueens, present := section[\"queens\"]; present {"}],
        "status": "aligned", "notes": "absent -> every queen; non-list -> every queen; empty entries dropped.",
    },
    {
        "path": "queen_mail.daily_limit", "type": "int >= 1", "default": 100, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "daily_limit: int = 100"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "config.DailyLimit = positiveInt(section[\"daily_limit\"], config.DailyLimit)"}],
        "status": "aligned", "notes": "bool is rejected as an int on both sides.",
    },
    {
        "path": "queen_mail.max_depth", "type": "int >= 1", "default": 8, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "max_depth: int = 8"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "config.MaxDepth = positiveInt(section[\"max_depth\"], config.MaxDepth)"}],
        "status": "aligned", "notes": "same validation.",
    },
    {
        "path": "queen_mail.max_parallel_turns", "type": "int >= 1", "default": 2, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "max_parallel_turns: int = 2"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "config.MaxParallelTurns = positiveInt(section[\"max_parallel_turns\"], config.MaxParallelTurns)"}],
        "status": "aligned", "notes": "same validation.",
    },
    {
        "path": "queen_mail.claude_code.<id>.name", "type": "string", "default": "id", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "name=text(\"name\", default.name) or queen_id,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Name:            textOr(entry[\"name\"], queenID),"}],
        "status": "aligned", "notes": "blank/whitespace falls back to the id.",
    },
    {
        "path": "queen_mail.claude_code.<id>.title", "type": "string", "default": "Claude Code", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "title=text(\"title\", default.title) or default.title,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Title:           textOr(entry[\"title\"], DefaultClaudeTitle),"}],
        "status": "aligned", "notes": "same fallback.",
    },
    {
        "path": "queen_mail.claude_code.<id>.command", "type": "string | list[string]", "default": "[\"claude\"]", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "command_parts = texts(\"command\") or default.command"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Command:         commandOf(entry[\"command\"]),"}],
        "status": "aligned", "notes": "a non-empty string is one argv element; a list is filtered to non-empty strings.",
    },
    {
        "path": "queen_mail.claude_code.<id>.auth", "type": "\"api_key\" | \"subscription\"", "default": "api_key", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "auth=auth if auth in (\"api_key\", \"subscription\") else default.auth,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Auth:            authOf(entry[\"auth\"]),"}],
        "status": "aligned", "notes": "anything else keeps api_key.",
    },
    {
        "path": "queen_mail.claude_code.<id>.api_key", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "api_key=text(\"api_key\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "APIKey:"}],
        "status": "aligned", "notes": "blank is none; ignored when auth=subscription.",
    },
    {
        "path": "queen_mail.claude_code.<id>.api_base", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "api_base=text(\"api_base\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "APIBase:         textOr(entry[\"api_base\"], \"\"),"}],
        "status": "aligned", "notes": "passed as ANTHROPIC_BASE_URL.",
    },
    {
        "path": "queen_mail.claude_code.<id>.config_dir", "type": "string", "default": "HIVE_HOME/claude_code/<id>/config", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "config_dir=text(\"config_dir\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "ConfigDir:       textOr(entry[\"config_dir\"], \"\"),"}],
        "status": "aligned", "notes": "None is resolved by the runner, not the config reader.",
    },
    {
        "path": "queen_mail.claude_code.<id>.cwd", "type": "string", "default": "HIVE_HOME/claude_code/<id>/workspace", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "cwd=text(\"cwd\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Cwd:             textOr(entry[\"cwd\"], \"\"),"}],
        "status": "aligned", "notes": "same resolution split.",
    },
    {
        "path": "queen_mail.claude_code.<id>.permission_mode", "type": "string", "default": "default", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "permission_mode=text(\"permission_mode\", default.permission_mode) or default.permission_mode,"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "PermissionMode:  textOr(entry[\"permission_mode\"], DefaultClaudePermissionMode),"}],
        "status": "aligned", "notes": "passed verbatim to --permission-mode; no allowlist of values.",
    },
    {
        "path": "queen_mail.claude_code.<id>.allowed_tools", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "allowed_tools=texts(\"allowed_tools\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "AllowedTools:    stringsList(entry[\"allowed_tools\"]),"}],
        "status": "aligned", "notes": "non-string entries and empties dropped.",
    },
    {
        "path": "queen_mail.claude_code.<id>.disallowed_tools", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "disallowed_tools=texts(\"disallowed_tools\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "DisallowedTools: stringsList(entry[\"disallowed_tools\"]),"}],
        "status": "aligned", "notes": "same filtering.",
    },
    {
        "path": "queen_mail.claude_code.<id>.add_dirs", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "add_dirs=texts(\"add_dirs\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "AddDirs:         stringsList(entry[\"add_dirs\"]),"}],
        "status": "aligned", "notes": "same filtering.",
    },
    {
        "path": "queen_mail.claude_code.<id>.model", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "model=text(\"model\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Model:           textOr(entry[\"model\"], \"\"),"}],
        "status": "aligned", "notes": "None leaves Claude Code's own default.",
    },
    {
        "path": "queen_mail.claude_code.<id>.timeout_minutes", "type": "int >= 1", "default": 30, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "timeout_minutes: int = 30"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "TimeoutMinutes:  positiveInt(entry[\"timeout_minutes\"], DefaultClaudeTimeoutMinutes),"}],
        "status": "aligned", "notes": "bool/invalid keeps 30.",
    },
    {
        "path": "queen_mail.claude_code.<id>.system_prompt", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "system_prompt=text(\"system_prompt\", None),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "SystemPrompt:    textOr(entry[\"system_prompt\"], \"\"),"}],
        "status": "aligned", "notes": "appended after Hive's note.",
    },
    {
        "path": "queen_mail.claude_code.<id>.env", "type": "object str->str", "default": {}, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "env=tuple((k, v) for k, v in env.items() if isinstance(k, str) and isinstance(v, str)) if isinstance(env, dict) else (),"}],
        "go": [{"file": "go_hive/go-hive/internal/qmail/config.go", "contains": "Env:             envMap(entry[\"env\"]),"}],
        "status": "aligned", "notes": "non-string keys/values dropped.",
    },

    # ----- soul -----------------------------------------------------------
    {
        "path": "soul.queens.<id>.enabled", "type": "bool", "default": "true when the entry exists", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "if not isinstance(raw, dict) or raw.get(\"enabled\") is False:"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if enabled, present := raw[\"enabled\"]; present && enabled == false {"}],
        "status": "aligned", "notes": "only JSON false disables; an absent entry means no soul.",
    },
    {
        "path": "soul.queens.<id>.mode", "type": "\"review\" | \"auto\"", "default": "review", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "mode=raw.get(\"mode\") if raw.get(\"mode\") in (\"review\", \"auto\") else default.mode,"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if mode, ok := raw[\"mode\"].(string); ok && (mode == \"review\" || mode == \"auto\") {"}],
        "status": "aligned", "notes": "other values keep review.",
    },
    {
        "path": "soul.queens.<id>.hour", "type": "int 0..23", "default": 4, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "hour=count(raw.get(\"hour\"), default.hour, low=0, high=23),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "Hour:              pyCount(raw[\"hour\"], defaults.Hour, 0, intPtr(23)),"}],
        "status": "aligned", "notes": "out of range -> 4.",
    },
    {
        "path": "soul.queens.<id>.model", "type": "\"queen\" | \"worker\"", "default": "queen", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "model=raw.get(\"model\") if raw.get(\"model\") in (\"queen\", \"worker\") else default.model,"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if model, ok := raw[\"model\"].(string); ok && (model == \"queen\" || model == \"worker\") {"}],
        "status": "aligned", "notes": "other values keep queen.",
    },
    {
        "path": "soul.queens.<id>.max_edits", "type": "int >= 1", "default": 5, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "max_edits=count(raw.get(\"max_edits\"), default.max_edits),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "MaxEdits:          pyCount(raw[\"max_edits\"], defaults.MaxEdits, 1, nil),"}],
        "status": "aligned", "notes": "bool rejected.",
    },
    {
        "path": "soul.queens.<id>.bootstrap_edits", "type": "int >= 1", "default": 30, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "bootstrap_edits=count(raw.get(\"bootstrap_edits\"), default.bootstrap_edits),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "BootstrapEdits:    pyCount(raw[\"bootstrap_edits\"], defaults.BootstrapEdits, 1, nil),"}],
        "status": "aligned", "notes": "same validation.",
    },
    {
        "path": "soul.queens.<id>.include_titles", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "include_titles=patterns(\"include_titles\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "IncludeTitles:     patterns(raw, \"include_titles\"),"}],
        "status": "aligned", "notes": "regex strings; non-list/empty entries dropped.",
    },
    {
        "path": "soul.queens.<id>.exclude_titles", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "exclude_titles=patterns(\"exclude_titles\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "ExcludeTitles:     patterns(raw, \"exclude_titles\"),"}],
        "status": "aligned", "notes": "same filtering.",
    },
    {
        "path": "soul.queens.<id>.sources.file", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "source_file=str(sources.get(\"file\") or \"\") or None,"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if file := OrString(sources[\"file\"]); file != \"\" {"}],
        "status": "aligned", "notes": "relative paths resolve against HIVE_HOME.",
    },
    {
        "path": "soul.queens.<id>.sources.keys", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "source_keys=tuple(str(k) for k in sources.get(\"keys\") or [] if isinstance(k, str) and k),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "list, _ := sources[\"keys\"].([]any)"}],
        "status": "aligned", "notes": "empty strings dropped.",
    },
    {
        "path": "soul.queens.<id>.bootstrap_memories", "type": "list[string]", "default": [], "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "bootstrap_memories=patterns(\"bootstrap_memories\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "BootstrapMemories: patterns(raw, \"bootstrap_memories\"),"}],
        "status": "aligned", "notes": "same filtering.",
    },
    {
        "path": "soul.queens.<id>.sections[].key", "type": "string", "default": "default section list", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "key=str(entry[\"key\"]).strip(),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "key := strings.TrimSpace(PyString(mapping[\"key\"]))"}],
        "status": "aligned", "notes": "a missing/blank key drops the entry.",
    },
    {
        "path": "soul.queens.<id>.sections[].title", "type": "string", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "title=str(entry[\"title\"]).strip(),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "title := strings.TrimSpace(PyString(mapping[\"title\"]))"}],
        "status": "aligned", "notes": "blank title drops the entry.",
    },
    {
        "path": "soul.queens.<id>.sections[].limit", "type": "int >= 1", "default": 400, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "limit=count(entry.get(\"limit\"), 400),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "Limit:    pyCount(mapping[\"limit\"], 400, 1, nil),"}],
        "status": "aligned", "notes": "invalid -> 400.",
    },
    {
        "path": "soul.queens.<id>.sections[].max_age_days", "type": "int >= 1", "default": None, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "max_age_days=max_age if isinstance(max_age, int) and not isinstance(max_age, bool) and max_age >= 1 else None,"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if age, isInt := AsInt(mapping[\"max_age_days\"]); isInt && age >= 1 {"}],
        "status": "aligned", "notes": "None/0/invalid means keep forever.",
    },
    {
        "path": "soul.queens.<id>.sections[].evidence", "type": "int >= 1", "default": 1, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "evidence=count(entry.get(\"evidence\"), 1),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "Evidence: pyCount(mapping[\"evidence\"], 1, 1, nil),"}],
        "status": "aligned", "notes": "invalid -> 1.",
    },
    {
        "path": "soul.queens.<id>.header", "type": "string", "default": "built-in header", "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "header=str(raw.get(\"header\") or default.header),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "if header := OrString(raw[\"header\"]); header != \"\" {"}],
        "status": "aligned", "notes": "blank keeps the built-in header.",
    },
    {
        "path": "soul.queens.<id>.session_chars", "type": "int >= 1", "default": 12000, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "session_chars=count(raw.get(\"session_chars\"), default.session_chars),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "SessionChars:      pyCount(raw[\"session_chars\"], defaults.SessionChars, 1, nil),"}],
        "status": "aligned", "notes": "invalid -> 12000.",
    },
    {
        "path": "soul.queens.<id>.message_chars", "type": "int >= 1", "default": 400, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "message_chars=count(raw.get(\"message_chars\"), default.message_chars),"}],
        "go": [{"file": "go_hive/go-hive/internal/soul/config.go", "contains": "MessageChars:      pyCount(raw[\"message_chars\"], defaults.MessageChars, 1, nil),"}],
        "status": "aligned", "notes": "invalid -> 400.",
    },

    # ----- retention ------------------------------------------------------
    {
        "path": "retention.enabled", "type": "bool", "default": True, "env": "HIVE_RETENTION_ENABLED",
        "py": [{"file": "core/framework/config.py", "contains": "enabled: bool = True  # master kill switch"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "Enabled:             true,"}],
        "status": "aligned", "notes": "file: exact bool only; env: (0,false,no,'') false. Same on both sides.",
    },
    {
        "path": "retention.mode", "type": "\"archive\" | \"delete\"", "default": "archive", "env": "HIVE_RETENTION_MODE",
        "py": [{"file": "core/framework/config.py", "contains": "mode: str = \"archive\"  # \"archive\" | \"delete\""}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "Mode:                \"archive\","}],
        "status": "aligned", "notes": "neither side validates the enum; an unknown string is kept (janitor treats non-delete as archive on both sides).",
    },
    {
        "path": "retention.archive_dir", "type": "string", "default": "HIVE_HOME/archive", "env": "HIVE_RETENTION_ARCHIVE_DIR",
        "py": [{"file": "core/framework/config.py", "contains": "archive_dir: str = \"\"  # default: $HIVE_HOME/archive"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "ArchiveDir:          \"\","}],
        "status": "aligned", "notes": "empty resolves to $HIVE_HOME/archive.",
    },
    {
        "path": "retention.event_logs_days", "type": "int", "default": 7, "env": "HIVE_RETENTION_EVENT_LOGS_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "event_logs_days: int = 7"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "EventLogsDays:       7,"}],
        "status": "aligned", "notes": "file: JSON integer only (bool/float/string ignored); env: invalid int keeps the file/default value.",
    },
    {
        "path": "retention.llm_logs_days", "type": "int", "default": 7, "env": "HIVE_RETENTION_LLM_LOGS_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "llm_logs_days: int = 7"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "LLMLogsDays:         7,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.compaction_log_days", "type": "int", "default": 7, "env": "HIVE_RETENTION_COMPACTION_LOG_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "compaction_log_days: int = 7"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "CompactionLogDays:   7,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.tool_artifacts_days", "type": "int", "default": 14, "env": "HIVE_RETENTION_TOOL_ARTIFACTS_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "tool_artifacts_days: int = 14"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "ToolArtifactsDays:   14,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.rotated_logs_days", "type": "int", "default": 30, "env": "HIVE_RETENTION_ROTATED_LOGS_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "rotated_logs_days: int = 30"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "RotatedLogsDays:     30,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.worker_deep_clean_days", "type": "int", "default": 14, "env": "HIVE_RETENTION_WORKER_DEEP_CLEAN_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "worker_deep_clean_days: int = 14"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "WorkerDeepCleanDays: 14,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.queen_hygiene_days", "type": "int", "default": 30, "env": "HIVE_RETENTION_QUEEN_HYGIENE_DAYS",
        "py": [{"file": "core/framework/config.py", "contains": "queen_hygiene_days: int = 30"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "QueenHygieneDays:    30,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.events_rewrite_min_bytes", "type": "int", "default": 1000000, "env": "HIVE_RETENTION_EVENTS_REWRITE_MIN_BYTES",
        "py": [{"file": "core/framework/config.py", "contains": "events_rewrite_min_bytes: int = 1_000_000"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "EventsRewriteMin:    1_000_000,"}],
        "status": "aligned", "notes": "int64 in Go; JSON integer only.",
    },
    {
        "path": "retention.active_grace_hours", "type": "int", "default": 48, "env": "HIVE_RETENTION_ACTIVE_GRACE_HOURS",
        "py": [{"file": "core/framework/config.py", "contains": "active_grace_hours: int = 48"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "ActiveGraceHours:    48,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.io_sleep_ms", "type": "int", "default": 10, "env": "HIVE_RETENTION_IO_SLEEP_MS",
        "py": [{"file": "core/framework/config.py", "contains": "io_sleep_ms: int = 10"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "IOSleepMS:           10,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.batch_size", "type": "int", "default": 200, "env": "HIVE_RETENTION_BATCH_SIZE",
        "py": [{"file": "core/framework/config.py", "contains": "batch_size: int = 200"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "BatchSize:           200,"}],
        "status": "aligned", "notes": "same rule.",
    },
    {
        "path": "retention.junk_min_bytes", "type": "int", "default": 50000000, "env": "HIVE_RETENTION_JUNK_MIN_BYTES",
        "py": [{"file": "core/framework/config.py", "contains": "junk_min_bytes: int = 50_000_000"}],
        "go": [{"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "JunkMinBytes:        50_000_000,"}],
        "status": "aligned", "notes": "int64 in Go.",
    },

    # ----- loop -----------------------------------------------------------
    {
        "path": "loop.max_tool_result_chars", "type": "int", "default": 30000, "env": None,
        "py": [{"file": "core/framework/config.py", "contains": "loop = get_hive_config().get(\"loop\", {})"}],
        "go": [{"file": "go_hive/go-hive/internal/agentloop/loop_config.go", "contains": "MaxToolResultChars:    30_000,"},
               {"file": "go_hive/go-hive/internal/hiveconf/hiveconf.go", "contains": "func MaxToolResultChars("},
        ],
        "status": "aligned",
        "notes": "both read the JSON int and fall back to the call site's value (30_000 global default). Wired: hiveconf.MaxToolResultChars applies to the queen chat loop (chatLoopConfig) and to each colony worker loop (BuildWorkerLoopConfig), so a pinned threshold moves the spillover point.",
    },

    # ----- tool category overrides ---------------------------------------
    {
        "path": "queen_tools.always_enabled_categories", "type": "list[string]", "default": None, "env": None,
        "py": [{"file": "core/framework/agents/queen/queen_tools_defaults.py", "contains": "override = get_hive_config().get(\"queen_tools\", {}).get(\"always_enabled_categories\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/routes_queens_tools.go", "contains": "if section, ok := HiveConfig(deps.Home)[\"queen_tools\"].(map[string]any); ok {"}],
        "status": "aligned",
        "notes": "list of strings; non-list/entries ignored; applies to the default tool catalogue.",
    },
    {
        "path": "worker_tools.always_enabled_categories", "type": "list[string]", "default": None, "env": None,
        "py": [{"file": "core/framework/agents/queen/queen_tools_defaults.py", "contains": "override = get_hive_config().get(\"worker_tools\", {}).get(\"always_enabled_categories\")"}],
        "go": [
               {"file": "go_hive/go-hive/internal/server/config_slots.go", "contains": "func WorkerKeepSetFromConfig("}
        ],
        "status": "aligned",
        "notes": "same list-of-strings rule as queen_tools: a mixed list is not an override. Wired: server.WorkerKeepSetFromConfig expands the categories and hands the colony runtime its WorkerKeepSet; an empty list turns worker tiering off, an absent key keeps the hardcoded default.",
    },

    # ----- endpoints / library / skills / profile / hooks -----------------
    {
        "path": "model_endpoints.<name>.api_base", "type": "object(str->str)", "default": "{}", "env": None,
        "py": [{"file": "core/framework/llm/endpoints.py", "contains": "raw = get_hive_config().get(\"model_endpoints\")"}],
        "go": [{"file": "go_hive/go-hive/internal/llm/endpoints.go", "contains": "raw, _ := loadHiveConfig()[\"model_endpoints\"].(map[string]any)"}],
        "status": "aligned",
        "notes": "name must match [a-z0-9_-]+; blank api_base skipped; trailing / trimmed. magpie is built in.",
    },
    {
        "path": "provider_library.<name>", "type": "object (slot-shaped)", "default": "{}", "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "lib = get_hive_config().get(\"provider_library\")"}],
        "go": [{"file": "go_hive/go-hive/internal/server/provider_library.go", "contains": "raw, ok := config[\"provider_library\"].(map[string]any)"}],
        "status": "aligned",
        "notes": "named saved slots; applying copies the object into a role slot, no indirection. Non-object entries ignored.",
    },
    {
        "path": "external_skills", "type": "list[string]", "default": "[]", "env": None,
        "py": [{"file": "core/framework/skills/discovery.py", "contains": "for raw in get_hive_config().get(\"external_skills\") or []:"}],
        "go": [{"file": "go_hive/go-hive/internal/skills/discovery.go", "contains": "raw, _ := cfg[\"external_skills\"].([]any)"}],
        "status": "aligned",
        "notes": "non-string entries ignored; paths may be nonexistent and resolve later.",
    },
    {
        "path": "user_profile.displayName", "type": "string", "default": "\"\"", "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "\"displayName\": profile.get(\"displayName\", \"\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/profile.go", "contains": "\"prompt_library_sort\": NormalizePromptSort(profile[\"prompt_library_sort\"]),"}],
        "status": "aligned",
        "notes": "GET returns \"\" for missing; PUT trims. Go's ProfileResponse 500s on a non-object user_profile exactly like upstream's AttributeError path.",
    },
    {
        "path": "user_profile.about", "type": "string", "default": "\"\"", "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "\"about\": profile.get(\"about\", \"\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/profile.go", "contains": "aboutValue, hasAbout := body[\"about\"]"}],
        "status": "aligned", "notes": "same trim; max length enforced by both (profile.go mirrors routes_config).",
    },
    {
        "path": "user_profile.theme", "type": "\"\" | \"light\" | \"dark\"", "default": "\"\"", "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "\"theme\": profile.get(\"theme\", \"\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/profile.go", "contains": "setTheme := themeIsString && (themeValue == \"light\" || themeValue == \"dark\")"}],
        "status": "aligned", "notes": "PUT ignores other values.",
    },
    {
        "path": "user_profile.density", "type": "\"\" | \"spacious\" | \"compact\"", "default": "\"\"", "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "\"density\": profile.get(\"density\", \"\"),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/profile.go", "contains": "setDensity := densityIsString && (densityValue == \"spacious\" || densityValue == \"compact\")"}],
        "status": "aligned", "notes": "PUT ignores other values.",
    },
    {
        "path": "user_profile.prompt_library_sort", "type": "object {my,community}", "default": None, "env": None,
        "py": [{"file": "core/framework/server/routes_config.py", "contains": "\"prompt_library_sort\": _normalize_prompt_sort(profile.get(\"prompt_library_sort\")),"}],
        "go": [{"file": "go_hive/go-hive/internal/server/profile.go", "contains": "\"prompt_library_sort\": NormalizePromptSort(profile[\"prompt_library_sort\"]),"}],
        "status": "aligned", "notes": "only known sort keys survive; absent -> null so the UI cache is not clobbered.",
    },
    {
        "path": "turn_context.<name>.enabled", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/server/turn_context.py", "contains": "settings = get_hive_config().get(\"turn_context\") or {}"}],
        "go": [{"file": "go_hive/go-hive/internal/server/turn_context.go", "contains": "if section, ok := config[\"turn_context\"].(map[string]any); ok && section != nil {"}],
        "status": "aligned",
        "notes": "section must be an object; a truthy enabled starts the installed entry point. `{\"voice\": true}` does not enable anything.",
    },
    {
        "path": "channels.<name>.enabled", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/server/channels.py", "contains": "settings = get_hive_config().get(\"channels\") or {}"}],
        "go": [{"file": "go_hive/go-hive/internal/server/channels_config.go", "contains": "func ChannelSections("},
               {"file": "go_hive/go-hive/internal/cli/loader.go", "contains": "for _, name := range hiveserver.UnhostedChannels(home) {"}],
        "status": "go_partial",
        "notes": "partial: no plugin host, so the flag cannot start a channel — the Go port has no entry-point discovery or channel lifecycle. Field-level parity instead: Go reads the section with upstream's shape (object, per-channel object, truthy enabled), preserves it on every configuration write, keeps GET /api/channels' upstream response shape (empty with no installed channels), and names enabled channels in a serve-time no-effect warning. Recorded in contracts/config-support-ledger.json.",
    },
    {
        "path": "rate_limits", "type": "object (dotted platform.action.window)", "default": "built-in table", "env": "HIVE_RATE_<PLATFORM>_<ACTION>_<WINDOW>",
        "py": [{"file": "core/framework/rate_limiter.py", "contains": "return get_hive_config().get(\"rate_limits\", {})"}],
        "go": [{"file": "go_hive/go-hive/internal/ratelimit/rate_limiter.go", "contains": "section, ok := cfg[\"rate_limits\"].(map[string]any)"}],
        "status": "aligned",
        "notes": "file values can raise defaults but never past the hard ceiling; env overrides exist on the Python side (HIVE_RATE_...) and are documented in the rate-limiter module.",
    },
    {
        "path": "sentinel.classify_after_seconds", "type": "number >= 1", "default": 300.0, "env": None,
        "py": [{"file": "core/framework/sentinel/store.py", "contains": "raw = global_sentinel_config().get(\"classify_after_seconds\", _DEFAULT_CLASSIFY_AFTER_SECONDS)"}],
        "go": [{"file": "go_hive/go-hive/internal/sentinel/store.go", "contains": "raw, ok := block[\"classify_after_seconds\"]"}],
        "status": "aligned", "notes": "float() coercion; values < 1 clamp to 1.0; invalid -> 300.0.",
    },
    {
        "path": "sentinel.max_nudges_before_escalate", "type": "int >= 1", "default": 5, "env": None,
        "py": [{"file": "core/framework/sentinel/store.py", "contains": "raw = global_sentinel_config().get(\"max_nudges_before_escalate\", _DEFAULT_MAX_NUDGES_BEFORE_ESCALATE)"}],
        "go": [{"file": "go_hive/go-hive/internal/sentinel/store.go", "contains": "raw, ok := block[\"max_nudges_before_escalate\"]"}],
        "status": "aligned", "notes": "int(); values < 1 clamp to 1; invalid -> 5.",
    },
    {
        "path": "sentinel.escalate_when_ui_attached", "type": "bool", "default": False, "env": None,
        "py": [{"file": "core/framework/sentinel/store.py", "contains": "return bool(global_sentinel_config().get(\"escalate_when_ui_attached\", _DEFAULT_ESCALATE_WHEN_UI_ATTACHED))"}],
        "go": [{"file": "go_hive/go-hive/internal/sentinel/store.go", "contains": "raw, ok := block[\"escalate_when_ui_attached\"]"}],
        "status": "aligned", "notes": "Python bool() admits truthy strings; Go uses PyTruthy to match.",
    },

    # ----- Go-only configuration.json extension --------------------------
    {
        "path": "mcp.enabled", "type": "bool", "default": True, "env": None,
        "py": [],
        "go": [{"file": "go_hive/go-hive/internal/cli/mcp_config.go", "contains": "Enabled        *bool           `json:\"enabled\"`"}],
        "status": "go_only",
        "notes": "go_only: confirmed no upstream counterpart — Python configures MCP servers through the registry (`hive mcp add`) and .mcp.json, and never reads an `mcp` block from configuration.json. Go reads it as the operator pin: enabled gates the whole subsystem (default true).",
    },
    {
        "path": "mcp.namespace", "type": "string", "default": "mcp__", "env": None,
        "py": [],
        "go": [{"file": "go_hive/go-hive/internal/cli/mcp_config.go", "contains": "Namespace      string          `json:\"namespace\"`"}],
        "status": "go_only", "notes": "go_only: confirmed no upstream counterpart. Tool-name namespace; blank keeps the default \"mcp__\".",
    },
    {
        "path": "mcp.timeout_seconds", "type": "number", "default": 240.0, "env": "HIVE_MCP_CALL_RESULT_TIMEOUT",
        "py": [],
        "go": [{"file": "go_hive/go-hive/internal/cli/mcp_config.go", "contains": "TimeoutSeconds *float64        `json:\"timeout_seconds\"`"},
               {"file": "go_hive/go-hive/internal/cli/mcp_loader.go", "contains": "configuration.json is the fallback for HIVE_MCP_CALL_RESULT_TIMEOUT"}],
        "status": "go_only",
        "notes": "env > file > 240s default, the general rule: internal/tools.MCPCallResultTimeout resolves HIVE_MCP_CALL_RESULT_TIMEOUT first, then the file value. Was file > env until the config-env-align lane; the reversal is fixed and pinned by TestMCPCallResultTimeoutPrecedence.",
    },
    {
        "path": "mcp.servers", "type": "list | object(name->spec)", "default": "[]", "env": None,
        "py": [],
        "go": [{"file": "go_hive/go-hive/internal/cli/mcp_config.go", "contains": "Servers        json.RawMessage `json:\"servers\"`"}],
        "status": "go_only",
        "notes": "go_only: confirmed no upstream counterpart. Accepts both the list and map shapes; a same-named registry entry is replaced by the file entry and `enabled: false` removes it from the load set.",
    },
]

# ---------------------------------------------------------------------------
# Priority & boundary rules (contract-level, not per-key)
# ---------------------------------------------------------------------------

BOUNDARY_RULES: list[dict] = [
    {
        "rule": "general precedence",
        "contract": "env > configuration.json > built-in default",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "raw = os.environ.get(\"HIVE_ADAPTIVE_TOOL_BUDGET\")"},
            {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "if value, ok := envFeatureFlag(\"HIVE_ADAPTIVE_TOOL_BUDGET\"); ok {"},
        ],
        "status": "aligned",
        "notes": "Holds for adaptive_tool_budget, email_senders, stall_check, memory_timeline and every HIVE_RETENTION_<FIELD>.",
    },
    {
        "rule": "empty env string",
        "contract": "feature flags: empty/whitespace = not set -> fall through to configuration.json; retention bool fields: empty = false",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "if raw is not None and raw.strip() != \"\":"},
            {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "if trimmed == \"\" {"},
            {"file": "core/framework/config.py", "contains": "env_val.strip().lower() not in (\"0\", \"false\", \"no\", \"\")"},
        ],
        "status": "aligned",
        "notes": "The two rules are deliberately different; both runtimes implement the same split.",
    },
    {
        "rule": "non-boolean env values",
        "contract": "deny-list (0/false/no/off) -> false; any other non-empty value -> true. Not strconv.ParseBool.",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "raw.strip().lower() not in (\"0\", \"false\", \"no\", \"off\")"},
            {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "case \"0\", \"false\", \"no\", \"off\":"},
        ],
        "status": "aligned",
        "notes": "So HIVE_STALL_CHECK=disabled is TRUE on both sides. Documented as a boundary, not a bug to fix casually.",
    },
    {
        "rule": "file value of the wrong JSON type",
        "contract": "flag/section readers require the exact type; non-bool for a flag keeps the default; non-object section reads as empty",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "if isinstance(value, bool):"},
            {"file": "go_hive/go-hive/internal/server/config_features.go", "contains": "value, ok := HiveConfig(home)[key].(bool)"},
            {"file": "core/framework/config.py", "contains": "raw = raw if isinstance(raw, dict) else {}"},
        ],
        "status": "aligned",
        "notes": "JSON 1 is not a bool on either side (Go decodes to json.Number/float64; Python bool is a distinct type).",
    },
    {
        "rule": "bad JSON / unreadable configuration.json",
        "contract": "read as {} -> every reader falls back to its default; a warning is logged on the Python side",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "except (json.JSONDecodeError, OSError) as e:"},
            {"file": "go_hive/go-hive/internal/hiveconf/hiveconf.go", "contains": "if err := decoder.Decode(&out); err != nil || out == nil {"},
        ],
        "status": "aligned",
        "notes": "Both strip a UTF-8 BOM; Go additionally rejects trailing JSON values after the object.",
    },
    {
        "rule": "integer fields: invalid value",
        "contract": "JSON: non-integer (bool/float/string) is ignored and the default/file value stays; env: invalid integer is ignored (Go) or warned+ignored (Python)",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "if f.name in raw and type(raw[f.name]) is type(getattr(cfg, f.name)):"},
            {"file": "go_hive/go-hive/internal/maintenance/config.go", "contains": "if strings.ContainsAny(num.String(), \".eE\") {"},
        ],
        "status": "aligned",
        "notes": "Floats like 7.0 are rejected by both (Python type mismatch; Go rejects decimal spellings).",
    },
    {
        "rule": "bounded / non-negative fields",
        "contract": "soul hour 0..23, every count >= 1, sentinel seconds >= 1, gcu viewport 0.1..1.0; out-of-range keeps the default (or clamps where the doc says clamp)",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "high: int | None = None"},
            {"file": "go_hive/go-hive/internal/soul/config.go", "contains": "pyCount(raw[\"hour\"], defaults.Hour, 0, intPtr(23))"},
            {"file": "go_hive/go-hive/internal/sentinel/store.go", "contains": "if value < 1.0 {"},
        ],
        "status": "aligned",
        "notes": "sentinel clamps to the floor; soul/gcu fall back to the default. Both sides agree per field.",
    },
    {
        "rule": "credentials vs env",
        "contract": "LLM key resolution: slot literal api_key -> named endpoint key -> subscription token -> credential store -> api_key_env_var -> main key",
        "evidence": [
            {"file": "core/framework/config.py", "contains": "Credential store is checked BEFORE the env var"},
            {"file": "go_hive/go-hive/internal/server/llm_config.go", "contains": "if envValue := llmSection[\"api_key_env_var\"]; pyTruthy(envValue) {"},
        ],
        "status": "go_partial",
        "notes": "Go's hot-swap path mirrors the order; its boot path is simpler. Any parity test for keys must pin this order.",
    },
    {
        "rule": "MCP call timeout",
        "contract": "env > file > 240s default: HIVE_MCP_CALL_RESULT_TIMEOUT wins over mcp.timeout_seconds (the reversal was fixed in the config-env-align lane)",
        "evidence": [
            {"file": "go_hive/go-hive/internal/cli/mcp_loader.go", "contains": "configuration.json is the fallback for HIVE_MCP_CALL_RESULT_TIMEOUT"},
            {"file": "go_hive/go-hive/internal/tools/mcp_client.go", "contains": "func MCPCallResultTimeout() time.Duration"},
        ],
        "status": "go_only",
        "notes": "No Python counterpart (Python has no mcp block in configuration.json). Freeze as a Go extension.",
    },
]

# ---------------------------------------------------------------------------
# Environment-variable alignment recommendations
# ---------------------------------------------------------------------------

ENV_PRODUCTION_ONLY = [
    # Python-only names that gate runtime behaviour in the reference. These are
    # the porting candidates (P1/P2); dev/eval/GCU capture variables are P3 and
    # are listed separately in the diff doc.
    #
    # HIVE_MAX_ITER and HIVE_SPLIT_WORKER_EVENTS graduated out of this list in
    # the config-env-align lane: the Go runtime now reads both (support-ledger
    # status "wired", pinned by tests). They stay in
    # contracts/config-support-ledger.json so the port remains documented.
    # The env-verbatim-port lane moved every remaining P2 and the resource /
    # logging switches into the Go runtime (support-ledger status "wired",
    # pinned by tests). What stays here is the GCU platform difference: those
    # knobs belong to the Python GCU process, which the Go port replaces with
    # the Go browser bridge — recorded as a difference, not a port.
    ("HIVE_GCU_MEMTRACE*", "P3", "GCU memory tracing (5 vars); platform difference — see contracts/config-support-ledger.json"),
    ("HIVE_GCU_SCREENSHOT_CONCURRENCY", "P3", "GCU screenshot concurrency; platform difference — the Go bridge has its own concurrency control"),
]

ENV_GO_PRODUCTION_ONLY = [
    ("HIVE_BROWSERBRIDGE_ARGS", "documented Go bridge flag pass-through"),
    ("HIVE_BROWSERBRIDGE_BIN", "documented Go bridge binary path"),
    ("HIVE_BROWSERBRIDGE_CONTROL", "documented Go bridge control port"),
    ("HIVE_BROWSERBRIDGE_HTTP", "documented Go bridge HTTP port"),
    ("HIVE_BROWSER_BRIDGE", "documented Go bridge on/off switch"),
    ("HIVE_BROWSER_VIEWPORT_SCALE", "injected into the bridge from gcu_viewport_scale"),
    ("HIVE_MCP2CLI", "native/external/auto mcp2cli selection (Go-only)"),
    ("HIVE_SESSION_COST_LIMIT_USD", "per-session cost ceiling (Go-only)"),
]
