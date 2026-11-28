import { describe, it, expect, vi, beforeEach } from "vitest";
import type { ReactNode } from "react";
import { render, screen, act, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

// ── Controllable dependency surface ─────────────────────────────────────────
// ColonyChat's job for this bug is turning "no colony after the lookup settled"
// into not-found, and turning a cold colony session into a resume under the
// queen that actually owns the transcript. Everything else (SSE, chat panel,
// API) is stubbed to keep the render deterministic and offline.
const h = vi.hoisted(() => ({
  colonies: [] as Array<{
    id: string;
    name: string;
    agentPath: string;
    queenProfileId?: string | null;
    queenName?: string;
  }>,
  llmReady: false as boolean,
  refresh: vi.fn<() => Promise<void>>(),
  markVisited: vi.fn(),
  setActions: vi.fn(),
  setLeftActions: vi.fn(),
  toggleColonyWorkers: vi.fn(),
  setTriggers: vi.fn(),
  setSessionId: vi.fn(),
  setColonyName: vi.fn(),
  setRequestQueenPrompt: vi.fn(),
  addUsage: vi.fn(),
  noteCreditSpend: vi.fn(),
  pushEvent: vi.fn(),
  create: vi.fn(),
  list: vi.fn(),
  history: vi.fn(),
  eventsHistory: vi.fn(),
  colonySessionsList: vi.fn(),
}));

vi.mock("@/context/ColonyContext", () => ({
  useColony: () => ({
    colonies: h.colonies,
    queenProfiles: [],
    markVisited: h.markVisited,
    // Promise-returning refresh (the production contract) — tests control when
    // (and whether) the lookup settles.
    refresh: h.refresh,
  }),
}));

vi.mock("@/context/HeaderActionsContext", () => ({
  useHeaderActions: () => ({ setActions: h.setActions, setLeftActions: h.setLeftActions }),
}));

vi.mock("@/context/ColonyWorkersContext", () => ({
  useColonyWorkers: () => ({
    toggleColonyWorkers: h.toggleColonyWorkers,
    setTriggers: h.setTriggers,
    setSessionId: h.setSessionId,
    setColonyName: h.setColonyName,
    setRequestQueenPrompt: h.setRequestQueenPrompt,
    workers: [],
  }),
}));

vi.mock("@/context/SessionUsageContext", () => ({
  useSessionUsage: () => ({ addUsage: h.addUsage, usage: null }),
}));

// ``llmReady`` selects the free read-only restore path or the paid
// create/resume path. Default false keeps the unknown-colony tests offline.
vi.mock("@/lib/me", () => ({
  useMe: () => ({ me: null, noteCreditSpend: h.noteCreditSpend }),
  canMakeLLMCalls: () => h.llmReady,
}));

vi.mock("@/components/DebugStateContext", () => ({
  useDebugState: () => ({ pushEvent: h.pushEvent }),
}));

vi.mock("@/hooks/use-sse", () => ({ useMultiSSE: vi.fn() }));

vi.mock("@/hooks/use-pending-queue", () => ({
  usePendingQueue: () => ({
    enqueue: vi.fn(),
    steer: vi.fn(),
    cancelQueued: vi.fn(),
    flushNext: vi.fn(),
    flushNextRef: { current: vi.fn() },
    clear: vi.fn(),
  }),
}));

// Marker for "the real chat surface rendered" (i.e. not the not-found branch),
// plus the bound session/queen so resume tests can prove no fresh session was
// adopted.
vi.mock("@/components/ChatPanel", () => ({
  default: ({
    sessionId,
    queenProfileId,
  }: {
    sessionId?: string | null;
    queenProfileId?: string | null;
  }) => (
    <div
      data-testid="chat-panel"
      data-session-id={sessionId ?? ""}
      data-queen-profile-id={queenProfileId ?? ""}
    />
  ),
}));
vi.mock("@/components/CredentialsModal", () => ({
  default: () => null,
  clearCredentialCache: vi.fn(),
}));
vi.mock("@/components/AgentCredentialForm", () => ({ default: () => null }));
vi.mock("@/components/SkillInstallConsent", () => ({ default: () => null }));
vi.mock("@/components/SessionReportAction", () => ({ default: () => null }));
vi.mock("@/components/Tooltip", () => ({
  Tooltip: ({ children }: { children?: ReactNode }) => <>{children}</>,
}));

vi.mock("@/api/sessions", () => ({
  sessionsApi: {
    list: h.list,
    history: h.history,
    create: h.create,
    listTriggers: vi.fn(async () => ({ triggers: [] })),
    eventsHistory: h.eventsHistory,
    dismissColonyPivot: vi.fn(async () => ({ dismissed: true })),
  },
  colonySessionsApi: { list: h.colonySessionsList },
}));

import ColonyChat from "./colony-chat";

/** Minimal LiveSession fixture with the fields the page reads. */
function liveSession(overrides: Record<string, unknown> = {}) {
  return {
    session_id: "sess-new",
    colony_id: null,
    has_worker: false,
    agent_path: "/root/.hive/colonies/alpha",
    description: "",
    goal: "",
    node_count: 0,
    loaded_at: 1,
    uptime_seconds: 0,
    queen_phase: "colony",
    queen_supports_images: true,
    queen_id: "queen_custom_72601",
    queen_name: "小周",
    ...overrides,
  };
}

function emptyEventsPage(overrides: Record<string, unknown> = {}) {
  return {
    session_id: "",
    total: 0,
    returned: 0,
    truncated: false,
    limit: 500,
    events: [],
    start_index: 0,
    start_offset: 0,
    has_more_older: false,
    ...overrides,
  };
}

/** One cold colony session, as GET /colonies/<name>/sessions returns it. */
function coldColonySession(overrides: Record<string, unknown> = {}) {
  return {
    session_id: "sess-cold",
    cold: true,
    live: false,
    has_messages: true,
    created_at: 1,
    last_active_at: 1,
    agent_path: "/root/.hive/colonies/go_hive",
    queen_id: "queen_custom_72601",
    ...overrides,
  };
}

function renderColonyChat(entry: string | { pathname: string; state?: unknown }) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/colony/:colonyId" element={<ColonyChat />} />
      </Routes>
    </MemoryRouter>,
  );
}

function resetHarness() {
  localStorage.clear();
  h.colonies = [];
  h.llmReady = false;
  h.refresh.mockReset().mockResolvedValue(undefined);
  h.markVisited.mockReset();
  h.create.mockReset().mockResolvedValue(liveSession());
  h.list.mockReset().mockResolvedValue({ sessions: [] });
  h.history.mockReset().mockResolvedValue({ sessions: [] });
  h.eventsHistory.mockReset().mockResolvedValue(emptyEventsPage());
  h.colonySessionsList.mockReset().mockResolvedValue({ sessions: [] });
}

describe("ColonyChat — unknown colony route", () => {
  beforeEach(resetHarness);

  it("replaces the permanent Connecting overlay with Colony not found for an unknown id", async () => {
    let resolveRefresh: () => void = () => {};
    h.refresh.mockImplementation(
      () => new Promise<void>((resolve) => {
        resolveRefresh = resolve;
      }),
    );

    renderColonyChat("/colony/ghost-colony");

    // While the lookup is in flight the connecting state is legitimate.
    expect(screen.getByText("Connecting to agent...")).toBeTruthy();
    expect(screen.queryByText("Colony not found: ghost-colony")).toBeNull();

    await act(async () => {
      resolveRefresh();
    });

    // Regression: the page used to spin on "Connecting to agent..." forever
    // because loadSession never ran to clear `loading`.
    expect(await screen.findByText("Colony not found: ghost-colony")).toBeTruthy();
    expect(screen.queryByText("Connecting to agent...")).toBeNull();
    expect(screen.queryByTestId("chat-panel")).toBeNull();
  });

  it("does not report not-found while the colony lookup is still in flight", async () => {
    h.refresh.mockImplementation(() => new Promise<void>(() => {}));

    renderColonyChat("/colony/slow-colony");
    await act(async () => {
      await Promise.resolve();
    });

    expect(screen.queryByText(/Colony not found/)).toBeNull();
    expect(screen.getByText("Connecting to agent...")).toBeTruthy();
  });

  it("still loads a colony that is present in the cached list", async () => {
    h.refresh.mockResolvedValue(undefined);
    h.colonies = [
      {
        id: "alpha",
        name: "Alpha",
        agentPath: "/root/.hive/colonies/alpha",
        queenProfileId: null,
      },
    ];

    renderColonyChat("/colony/alpha");

    expect(await screen.findByTestId("chat-panel")).toBeTruthy();
    // Wait for the session load to settle (the overlay clears), then verify the
    // known colony was never reclassified as missing.
    await waitFor(() => expect(screen.queryByText("Connecting to agent...")).toBeNull());
    expect(screen.queryByText(/Colony not found/)).toBeNull();
  });

  it("treats a route carrying agentPath as connecting, never not-found", async () => {
    h.refresh.mockResolvedValue(undefined);

    // Colony-create handoff: the agent path arrives in route state, so the
    // session can boot even though the colony list hasn't caught up yet.
    renderColonyChat({
      pathname: "/colony/queen-build",
      state: { agentPath: "/root/.hive/colonies/alpha" },
    });

    expect(await screen.findByTestId("chat-panel")).toBeTruthy();
    await waitFor(() => expect(screen.queryByText("Connecting to agent...")).toBeNull());
    expect(screen.queryByText(/Colony not found/)).toBeNull();
  });
});

describe("ColonyChat — colony session queen resume", () => {
  beforeEach(resetHarness);

  // Parity-exempt: frontend-only regression test for the colony-rename bug
  // (no upstream renderer test covers this resume path).
  it("resumes the cold custom-queen session instead of creating a default-queen session", async () => {
    h.llmReady = true;
    h.colonies = [
      {
        id: "go-hive",
        name: "Go Hive",
        agentPath: "/root/.hive/colonies/go_hive",
        // Metadata still carries the old/default queen after the rename; the
        // cold session on disk is the source of truth for the resume.
        queenProfileId: "queen_growth",
        queenName: "queen_growth",
      },
    ];
    h.colonySessionsList.mockResolvedValue({
      sessions: [coldColonySession()],
    });
    // Mirror the backend: only a resume that names the cold session's own
    // queen reopens colonies/<c>/queens/queen_custom_72601/sessions/<id>;
    // anything else mints a fresh session (the baseline bug returned
    // "sess-20261124_171652_e9ad74d8" under queen_growth).
    h.create.mockImplementation(
      async (opts: { queenName?: string; queenResumeFrom?: string }) => {
        if (
          opts.queenName === "queen_custom_72601" &&
          opts.queenResumeFrom === "sess-cold"
        ) {
          return liveSession({
            session_id: "sess-cold",
            colony_id: "go_hive",
            agent_path: "/root/.hive/colonies/go_hive",
            queen_id: "queen_custom_72601",
            queen_name: "小周",
          });
        }
        return liveSession({
          session_id: "sess-20261124_171652_e9ad74d8",
          colony_id: "go_hive",
          agent_path: "/root/.hive/colonies/go_hive",
          queen_id: "queen_growth",
          queen_name: "Growth",
        });
      },
    );

    renderColonyChat("/colony/go-hive");

    await waitFor(() => expect(h.create).toHaveBeenCalledTimes(1));
    // API calls use the disk slug. The route's display slug ("go-hive") must
    // never reach the backend.
    expect(h.colonySessionsList).toHaveBeenCalledWith("go_hive");
    const opts = h.create.mock.calls[0][0];
    expect(opts).toMatchObject({
      colonyId: "go_hive",
      queenResumeFrom: "sess-cold",
      queenName: "queen_custom_72601",
    });
    for (const [call] of h.create.mock.calls) {
      // No create may be a fresh session under the backend's default queen.
      expect(call?.queenName).toBeTruthy();
      expect(call?.queenName).not.toBe("queen_growth");
      expect(call?.queenResumeFrom).toBe("sess-cold");
    }

    // The page binds the restored session, not a newly minted one. (The
    // colony/header queen mapping itself is covered by ColonyContext.test.)
    await waitFor(() =>
      expect(screen.getByTestId("chat-panel").getAttribute("data-session-id")).toBe(
        "sess-cold",
      ),
    );
  });

  // Parity-exempt: frontend-only regression test for the colony-rename bug
  // (no upstream renderer test covers this failure path).
  it("surfaces an error when a cold colony resume mints a different session", async () => {
    h.llmReady = true;
    h.colonies = [
      {
        id: "go-hive",
        name: "Go Hive",
        agentPath: "/root/.hive/colonies/go_hive",
        queenProfileId: "queen_custom_72601",
      },
    ];
    h.colonySessionsList.mockResolvedValue({
      sessions: [coldColonySession()],
    });
    // Baseline failure mode: the backend could not find the custom queen's
    // transcript and created a fresh default-queen session.
    h.create.mockResolvedValue(liveSession({ session_id: "sess-new" }));

    renderColonyChat("/colony/go-hive");

    expect(
      await screen.findByText(/Could not restore this colony's saved chat/),
    ).toBeTruthy();
    // The fresh session must not be adopted by the chat surface.
    expect(screen.getByTestId("chat-panel").getAttribute("data-session-id")).toBe("");
  });

  // Parity-exempt: frontend-only regression test; pins the existing
  // not-found behavior for an unresolved colony route.
  it("does not auto-create or default-queen a session for an unresolved colony", async () => {
    h.llmReady = true;
    h.refresh.mockResolvedValue(undefined);

    renderColonyChat("/colony/ghost-colony");

    expect(await screen.findByText("Colony not found: ghost-colony")).toBeTruthy();
    expect(screen.queryByTestId("chat-panel")).toBeNull();
    expect(h.create).not.toHaveBeenCalled();
    expect(h.colonySessionsList).not.toHaveBeenCalled();
    // No list call without a resolved agentPath either — nothing can boot.
    expect(h.history).not.toHaveBeenCalled();
  });

  // Parity-exempt: frontend-only regression test; pins the untouched
  // new-colony path where no metadata queen exists.
  it("creates a new colony session without a queenName when no metadata queen exists", async () => {
    h.llmReady = true;
    h.create.mockResolvedValue(
      liveSession({
        session_id: "sess-fresh",
        colony_id: "fresh_colony",
        agent_path: "/root/.hive/colonies/fresh_colony",
        queen_id: "queen_growth",
      }),
    );

    // Colony-create handoff: agentPath arrives in route state and the colony
    // has no metadata queen yet, so the backend keeps choosing the queen.
    renderColonyChat({
      pathname: "/colony/fresh-colony",
      state: { agentPath: "/root/.hive/colonies/fresh_colony" },
    });

    await waitFor(() => expect(h.create).toHaveBeenCalledTimes(1));
    const opts = h.create.mock.calls[0][0];
    expect(opts.colonyId).toBe("fresh_colony");
    expect(opts.queenResumeFrom).toBeUndefined();
    expect(opts.queenName).toBeUndefined();
    // No colony metadata lookup ran for a route with no resolved colony.
    expect(h.colonySessionsList).not.toHaveBeenCalled();
  });
});
