import { useEffect, useState } from "react";
import {
  Plus,
  Trash2,
  RefreshCw,
  Loader2,
  AlertCircle,
  Check,
  X,
  Server,
  CircleCheck,
  CircleAlert,
  CircleDashed,
  SquareTerminal,
} from "lucide-react";
import {
  mcpApi,
  type McpServer,
  type McpMode,
  type McpTransport,
  type AddMcpServerBody,
} from "@/api/mcp";
import { useI18n } from "@/i18n";

type TransportKey = McpTransport;

const TRANSPORT_OPTIONS: TransportKey[] = ["stdio", "http", "sse", "unix"];

function HealthBadge({ server }: { server: McpServer }) {
  const { t } = useI18n();
  if (!server.enabled) {
    return (
      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
        <CircleDashed className="w-3 h-3" /> {t("Disabled")}
      </span>
    );
  }
  if (server.last_health_status === "healthy") {
    return (
      <span className="flex items-center gap-1 text-[11px] text-green-500">
        <CircleCheck className="w-3 h-3" /> {t("Healthy")}
      </span>
    );
  }
  if (server.last_health_status === "unhealthy") {
    return (
      <span
        className="flex items-center gap-1 text-[11px] text-red-400"
        title={server.last_error || t("Unhealthy")}
      >
        <CircleAlert className="w-3 h-3" /> {t("Unhealthy")}
      </span>
    );
  }
  return (
    <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
      <CircleDashed className="w-3 h-3" /> {t("Unknown")}
    </span>
  );
}

interface AddFormState {
  name: string;
  transport: TransportKey;
  command: string;
  args: string;
  env: string;
  cwd: string;
  url: string;
  headers: string;
  socketPath: string;
  description: string;
  mode: McpMode;
}

const EMPTY_FORM: AddFormState = {
  name: "",
  transport: "stdio",
  command: "",
  args: "",
  env: "",
  cwd: "",
  url: "",
  headers: "",
  socketPath: "",
  description: "",
  mode: "tools",
};

// The "From the terminal" choice: mcp2cli can't reach a unix socket.
const CLI_TRANSPORTS: TransportKey[] = ["stdio", "http", "sse"];

function parseKeyValueLines(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean)
    .forEach((line) => {
      const eq = line.indexOf("=");
      if (eq < 0) return;
      const k = line.slice(0, eq).trim();
      const v = line.slice(eq + 1).trim();
      if (k) out[k] = v;
    });
  return out;
}

function buildAddBody(form: AddFormState): AddMcpServerBody {
  const body: AddMcpServerBody = {
    name: form.name.trim(),
    transport: form.transport,
    description: form.description.trim() || undefined,
    mode: CLI_TRANSPORTS.includes(form.transport) ? form.mode : "tools",
  };
  if (form.transport === "stdio") {
    body.command = form.command.trim();
    const args = form.args
      .split("\n")
      .map((s) => s.trim())
      .filter(Boolean);
    if (args.length) body.args = args;
    const env = parseKeyValueLines(form.env);
    if (Object.keys(env).length) body.env = env;
    if (form.cwd.trim()) body.cwd = form.cwd.trim();
  } else if (form.transport === "http" || form.transport === "sse") {
    body.url = form.url.trim();
    const headers = parseKeyValueLines(form.headers);
    if (Object.keys(headers).length) body.headers = headers;
  } else if (form.transport === "unix") {
    body.socket_path = form.socketPath.trim();
  }
  return body;
}

export default function McpServersPanel({ addOpen, setAddOpen }: { addOpen?: boolean; setAddOpen?: (v: boolean) => void } = {}) {
  const { t } = useI18n();
  const [servers, setServers] = useState<McpServer[] | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [addingInternal, setAddingInternal] = useState(false);
  // Allow parent to control the add modal, or fall back to internal state.
  const adding = addOpen ?? addingInternal;
  const setAdding = setAddOpen ?? setAddingInternal;
  const [form, setForm] = useState<AddFormState>(EMPTY_FORM);
  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const [busyByName, setBusyByName] = useState<Record<string, boolean>>({});

  const refresh = async () => {
    setLoading(true);
    setError(null);
    try {
      const { servers } = await mcpApi.listServers();
      setServers(servers);
    } catch (e: unknown) {
      setError((e as Error)?.message || t("Failed to load MCP servers"));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    refresh();
  }, []);

  const setBusy = (name: string, v: boolean) =>
    setBusyByName((p) => ({ ...p, [name]: v }));

  const handleToggle = async (server: McpServer) => {
    setBusy(server.name, true);
    try {
      await mcpApi.setEnabled(server.name, !server.enabled);
      await refresh();
    } catch (e: unknown) {
      setError((e as Error)?.message || t("Toggle failed"));
    } finally {
      setBusy(server.name, false);
    }
  };

  const handleMode = async (server: McpServer) => {
    setBusy(server.name, true);
    try {
      await mcpApi.setMode(server.name, server.mode === "cli" ? "tools" : "cli");
      await refresh();
    } catch (e: unknown) {
      const body = (e as { body?: { error?: string } }).body;
      setError(body?.error || (e as Error)?.message || t("Mode change failed"));
    } finally {
      setBusy(server.name, false);
    }
  };

  const handleRemove = async (server: McpServer) => {
    if (!confirm(t("Remove MCP server \"{name}\"?", { name: server.name }))) return;
    setBusy(server.name, true);
    try {
      await mcpApi.removeServer(server.name);
      await refresh();
    } catch (e: unknown) {
      const body = (e as { body?: { error?: string } }).body;
      setError(body?.error || (e as Error)?.message || t("Remove failed"));
    } finally {
      setBusy(server.name, false);
    }
  };

  const handleHealth = async (server: McpServer) => {
    setBusy(server.name, true);
    try {
      await mcpApi.checkHealth(server.name);
      await refresh();
    } catch (e: unknown) {
      setError((e as Error)?.message || t("Health check failed"));
    } finally {
      setBusy(server.name, false);
    }
  };

  const canSubmit = (() => {
    if (!form.name.trim()) return false;
    if (form.transport === "stdio") return !!form.command.trim();
    if (form.transport === "http" || form.transport === "sse")
      return !!form.url.trim();
    if (form.transport === "unix") return !!form.socketPath.trim();
    return false;
  })();

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setSubmitting(true);
    setSubmitError(null);
    try {
      const body = buildAddBody(form);
      const { server } = await mcpApi.addServer(body);
      // Best-effort: auto-run health check so the UI shows tool count.
      try {
        await mcpApi.checkHealth(server.name);
      } catch {
        /* health check is informational; don't block the add flow */
      }
      setAdding(false);
      setForm(EMPTY_FORM);
      await refresh();
    } catch (e: unknown) {
      const body = (e as { body?: { error?: string; fix?: string } }).body;
      setSubmitError(
        [body?.error, body?.fix].filter(Boolean).join(" — ") ||
          (e as Error)?.message ||
          t("Add failed"),
      );
    } finally {
      setSubmitting(false);
    }
  };

  // Group by origin. "local" = user-registered via the UI or CLI. Everything
  // else (built-in package entries, registry-installed entries) sits under
  // "Built-in" since the user can't remove them from the UI.
  const builtIns = (servers || []).filter((s) => s.source !== "local");
  const custom = (servers || []).filter((s) => s.source === "local");

  return (
    <div className="flex flex-col gap-5">

      {error && (
        <div className="flex items-start gap-2 text-xs text-destructive p-2.5 rounded-md bg-destructive/10 border border-destructive/30">
          <AlertCircle className="w-3.5 h-3.5 mt-0.5 flex-shrink-0" />
          <span className="flex-1">{error}</span>
          <button
            onClick={() => setError(null)}
            className="text-destructive/70 hover:text-destructive"
          >
            <X className="w-3 h-3" />
          </button>
        </div>
      )}

      {loading && !servers && (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="w-3 h-3 animate-spin" /> {t("Loading MCP servers…")}
        </div>
      )}

      {servers && (
        <>
          {custom.length > 0 && (
            <Section title={t("My Custom")}>
              {custom.map((s) => (
                <ServerRow
                  key={s.name}
                  server={s}
                  busy={!!busyByName[s.name]}
                  onToggle={() => handleToggle(s)}
                  onRemove={() => handleRemove(s)}
                  onHealth={() => handleHealth(s)}
                  onMode={() => handleMode(s)}
                  isLocal
                />
              ))}
            </Section>
          )}
          <Section title={t("Built-in")}>
            {builtIns.length === 0 ? (
              <p className="text-xs text-muted-foreground px-2 py-2">
                {t("No built-in servers registered.")}
              </p>
            ) : (
              builtIns.map((s) => (
                <ServerRow
                  key={s.name}
                  server={s}
                  busy={!!busyByName[s.name]}
                  onToggle={() => handleToggle(s)}
                  onRemove={() => handleRemove(s)}
                  onHealth={() => handleHealth(s)}
                  onMode={() => handleMode(s)}
                />
              ))
            )}
          </Section>
        </>
      )}

      {/* Add MCP modal */}
      {adding && (
        <div className="fixed inset-0 z-[60] flex items-center justify-center">
          <div
            className="absolute inset-0 bg-black/50"
            onClick={() => !submitting && setAdding(false)}
          />
          <div className="relative bg-card border border-border/60 rounded-xl shadow-2xl w-full max-w-lg p-5 space-y-4 max-h-[85vh] overflow-y-auto">
            <div className="flex items-center justify-between">
              <h3 className="text-sm font-semibold text-foreground">
                {t("Add MCP Server")}
              </h3>
              <button
                onClick={() => !submitting && setAdding(false)}
                className="p-1 rounded text-muted-foreground hover:text-foreground"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            <FieldRow label={t("Name *")} hint={t("Unique identifier, e.g. my-search-tool")}>
              <input
                autoFocus
                value={form.name}
                onChange={(e) =>
                  setForm((f) => ({
                    ...f,
                    name: e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ""),
                  }))
                }
                placeholder="my-search-tool"
                className={inputCls}
              />
            </FieldRow>

            <FieldRow label={t("Transport *")}>
              <div className="flex gap-1">
                {TRANSPORT_OPTIONS.map((t) => (
                  <button
                    key={t}
                    onClick={() => setForm((f) => ({ ...f, transport: t }))}
                    className={`flex-1 px-3 py-1.5 rounded-md text-xs font-medium border ${
                      form.transport === t
                        ? "bg-primary/15 text-primary border-primary/40"
                        : "text-muted-foreground hover:text-foreground border-border/60 hover:bg-muted/30"
                    }`}
                  >
                    {t}
                  </button>
                ))}
              </div>
            </FieldRow>

            {form.transport === "stdio" && (
              <>
                <FieldRow
                  label={t("Command *")}
                  hint={t("Executable that speaks MCP over stdin/stdout")}
                >
                  <input
                    value={form.command}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, command: e.target.value }))
                    }
                    placeholder="uv"
                    className={inputCls}
                  />
                </FieldRow>
                <FieldRow label={t("Args (one per line)")}>
                  <textarea
                    value={form.args}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, args: e.target.value }))
                    }
                    rows={3}
                    placeholder={"run\npython\nmy_server.py\n--stdio"}
                    className={textareaCls}
                  />
                </FieldRow>
                <FieldRow label={t("Env (KEY=VALUE, one per line)")}>
                  <textarea
                    value={form.env}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, env: e.target.value }))
                    }
                    rows={2}
                    placeholder="API_KEY=abc123"
                    className={textareaCls}
                  />
                </FieldRow>
                <FieldRow label={t("Working directory")}>
                  <input
                    value={form.cwd}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, cwd: e.target.value }))
                    }
                    placeholder="/path/to/repo"
                    className={inputCls}
                  />
                </FieldRow>
              </>
            )}

            {(form.transport === "http" || form.transport === "sse") && (
              <>
                <FieldRow label={t("URL *")}>
                  <input
                    value={form.url}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, url: e.target.value }))
                    }
                    placeholder="https://example.com/mcp"
                    className={inputCls}
                  />
                </FieldRow>
                <FieldRow label={t("Headers (KEY=VALUE, one per line)")}>
                  <textarea
                    value={form.headers}
                    onChange={(e) =>
                      setForm((f) => ({ ...f, headers: e.target.value }))
                    }
                    rows={2}
                    placeholder="Authorization=Bearer ..."
                    className={textareaCls}
                  />
                </FieldRow>
              </>
            )}

            {form.transport === "unix" && (
              <FieldRow label={t("Socket path *")}>
                <input
                  value={form.socketPath}
                  onChange={(e) =>
                    setForm((f) => ({ ...f, socketPath: e.target.value }))
                  }
                  placeholder="/tmp/mcp.sock"
                  className={inputCls}
                />
              </FieldRow>
            )}

            {CLI_TRANSPORTS.includes(form.transport) && (
              <FieldRow
                label={t("Agents use it")}
                hint={
                  form.mode === "cli"
                    ? (
                        <>
                          {t("They run")}{" "}
                          <code className="font-mono text-foreground/80">
                            hive mcp run {form.name || "<name>"} …
                          </code>{" "}
                          {t("in the terminal. Its tools stay out of every prompt until a command is used: best for servers with many tools.")}
                        </>
                      )
                    : t("Its tools load into every agent's tool list.")
                }
              >
                <div className="flex gap-1">
                  {(["tools", "cli"] as const).map((m) => (
                    <button
                      key={m}
                      onClick={() => setForm((f) => ({ ...f, mode: m }))}
                      className={`flex-1 px-3 py-1.5 rounded-md text-xs font-medium border ${
                        form.mode === m
                          ? "bg-primary/15 text-primary border-primary/40"
                          : "text-muted-foreground hover:text-foreground border-border/60 hover:bg-muted/30"
                      }`}
                    >
                      {m === "tools" ? t("As tools") : t("From the terminal")}
                    </button>
                  ))}
                </div>
              </FieldRow>
            )}

            <FieldRow label={t("Description")}>
              <input
                value={form.description}
                onChange={(e) =>
                  setForm((f) => ({ ...f, description: e.target.value }))
                }
                placeholder={t("What this server does")}
                className={inputCls}
              />
            </FieldRow>

            {submitError && (
              <div className="flex items-start gap-2 text-xs text-destructive p-2 rounded-md bg-destructive/10 border border-destructive/30">
                <AlertCircle className="w-3.5 h-3.5 mt-0.5 flex-shrink-0" />
                <span>{submitError}</span>
              </div>
            )}

            <div className="flex justify-end gap-2 pt-1">
              <button
                onClick={() => setAdding(false)}
                disabled={submitting}
                className="px-3 py-1.5 rounded-md text-xs font-medium text-muted-foreground hover:text-foreground hover:bg-muted/30"
              >
                {t("Cancel")}
              </button>
              <button
                onClick={handleSubmit}
                disabled={!canSubmit || submitting}
                className="flex items-center gap-1.5 px-3 py-1.5 rounded-md bg-primary text-primary-foreground text-xs font-semibold hover:bg-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
              >
                {submitting ? (
                  <Loader2 className="w-3 h-3 animate-spin" />
                ) : (
                  <Check className="w-3 h-3" />
                )}
                {submitting ? t("Adding…") : t("Add")}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

const inputCls =
  "w-full bg-muted/30 border border-border/50 rounded-lg px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-1 focus:ring-primary/40";
const textareaCls = `${inputCls} resize-none font-mono text-xs`;

function FieldRow({
  label,
  hint,
  children,
}: {
  label: string;
  hint?: React.ReactNode;
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <div>
      <label className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider mb-1.5 block">
        {t(label)}
      </label>
      {children}
      {hint && (
        <p className="text-[11px] text-muted-foreground/70 mt-1">{hint}</p>
      )}
    </div>
  );
}

function Section({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <div>
      <p className="text-[11px] font-semibold text-muted-foreground/60 uppercase tracking-wider mb-2">
        {t(title)}
      </p>
      <div className="flex flex-col gap-1">{children}</div>
    </div>
  );
}

function ServerRow({
  server,
  busy,
  onToggle,
  onRemove,
  onHealth,
  onMode,
  isLocal,
}: {
  server: McpServer;
  busy: boolean;
  onToggle: () => void;
  onRemove: () => void;
  onHealth: () => void;
  onMode: () => void;
  isLocal?: boolean;
}) {
  const { t } = useI18n();
  // Package-baked servers live in the repo and aren't managed by
  // MCPRegistry, so toggling / removing / health-checking them would
  // fail against the backend. Show them as read-only.
  const isBuiltIn = server.source === "built-in";
  const isCli = server.mode === "cli";
  return (
    <div className="flex items-center gap-3 py-2.5 px-2 rounded-lg hover:bg-muted/20">
      <div className="w-9 h-9 rounded-full bg-primary/10 flex items-center justify-center flex-shrink-0">
        <Server className="w-4 h-4 text-primary" />
      </div>
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <p className="text-sm font-medium text-foreground truncate">
            {server.name}
          </p>
          <span className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
            {server.transport}
          </span>
          {isBuiltIn && (
            <span className="text-[10px] uppercase tracking-wider text-muted-foreground/80 bg-muted/40 px-1.5 py-0.5 rounded">
              {t("Built-in")}
            </span>
          )}
          {server.tool_count !== null && server.tool_count !== undefined && (
            <span className="text-[11px] text-muted-foreground">
              {isCli ? t("{count} commands", { count: server.tool_count }) : t("{count} tools", { count: server.tool_count })}
            </span>
          )}
          {isCli && (
            <span
              className="flex items-center gap-1 text-[10px] uppercase tracking-wider text-primary bg-primary/10 px-1.5 py-0.5 rounded"
              title={t("Agents run \"hive mcp run {name} …\" in the terminal", { name: server.name })}
            >
              <SquareTerminal className="w-3 h-3" /> CLI
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {!isBuiltIn && <HealthBadge server={server} />}
          {server.description && (
            <span className="text-xs text-muted-foreground truncate">
              {isBuiltIn ? server.description : `· ${server.description}`}
            </span>
          )}
        </div>
      </div>
      {!isBuiltIn && (
        <>
          {server.transport !== "unix" && (
            <button
              onClick={onMode}
              disabled={busy}
              className="px-2.5 py-1 rounded-md text-[11px] font-medium text-muted-foreground border border-border/60 hover:text-foreground hover:bg-muted/30 disabled:opacity-50"
              title={
                isCli
                  ? t("Load its tools into agents' tool lists instead")
                  : t("Keep its tools out of prompts; agents run it from the terminal")
              }
            >
              {isCli ? t("Use as tools") : t("Use from terminal")}
            </button>
          )}
          <button
            onClick={onHealth}
            disabled={busy}
            className="p-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-muted/40 disabled:opacity-50"
            title={t("Health check")}
          >
            {busy ? (
              <Loader2 className="w-3.5 h-3.5 animate-spin" />
            ) : (
              <RefreshCw className="w-3.5 h-3.5" />
            )}
          </button>
          <button
            onClick={onToggle}
            disabled={busy}
            className={`px-3 py-1 rounded-md text-[11px] font-semibold border disabled:opacity-50 ${
              server.enabled
                ? "text-muted-foreground border-border/60 hover:bg-muted/30"
                : "bg-primary/15 text-primary border-primary/40 hover:bg-primary/25"
            }`}
          >
            {server.enabled ? t("Disable") : t("Enable")}
          </button>
        </>
      )}
      {isLocal && !isBuiltIn && (
        <button
          onClick={onRemove}
          disabled={busy}
          className="p-1.5 rounded-md text-muted-foreground hover:text-red-400 hover:bg-red-500/10 disabled:opacity-50"
          title={t("Remove")}
        >
          <Trash2 className="w-3.5 h-3.5" />
        </button>
      )}
    </div>
  );
}
