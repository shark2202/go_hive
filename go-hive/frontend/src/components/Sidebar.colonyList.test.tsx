/**
 * Regression guard for "a freshly created colony must appear in the sidebar".
 *
 * This is the assertion that the 2026-11-23 UI E2E run verified BY HAND in a
 * live browser (docs/e2e-assets/ui-e2e/REPORT.md). A hand-run screenshot cannot
 * fail a build, so the invariant lives here instead: /discover hands the runtime
 * a brand-new colony directory, and the sidebar must render a navigable row for
 * it whose href matches the route `/colony/:colonyId`.
 *
 * Why this test is shaped the way it is:
 *
 *  - The interesting colony is the one with NO live session and NO history. That
 *    is exactly the state of a colony one second after creation, and it is the
 *    state that a session-driven or history-driven list would silently drop.
 *    A colony that only shows up because it happens to have a session would pass
 *    a weaker test and still fail the user's first click.
 *
 *  - The slug → route-id mapping is `slugToColonyId`, which only rewrites `_`
 *    to `-` and strips an `email-` prefix / `-agent` suffix. It does NOT
 *    lower-case or title-case. So `ui_e2e_012144` must produce the href
 *    `/colony/ui-e2e-012144` — a mapping bug here yields a 404 on click even
 *    though the row rendered.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within, cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

// ── Fixture ──────────────────────────────────────────────────────────────────
// The colony under test: freshly bootstrapped, owned by queen_growth, no session.
const FRESH_SLUG = "ui_e2e_012144";
const FRESH_DISPLAY = "Ui E2E 012144";
const FRESH_ROUTE_ID = "ui-e2e-012144";

const DISCOVER = {
  "Your Agents": [
    {
      category: "Your Agents",
      created_at: 1795368110.64702,
      description: `Colony ${FRESH_SLUG}`,
      icon: null,
      is_loaded: false,
      last_active: null,
      name: FRESH_DISPLAY,
      node_count: 1,
      path: `/Users/mac/.hive-go/colonies/${FRESH_SLUG}`,
      run_count: 0,
      session_count: 0,
      tags: [],
      tool_count: 0,
      workers: [
        {
          colony_id: FRESH_SLUG,
          config_path: `/Users/mac/.hive-go/colonies/${FRESH_SLUG}/worker.json`,
          description: `Colony ${FRESH_SLUG}`,
          name: FRESH_DISPLAY,
          queen_name: "queen_growth",
          spawned_at: "",
          task: `Work on tasks for colony ${FRESH_SLUG}`,
          tool_count: 0,
        },
      ],
    },
  ],
  // The internal runtime category must be filtered out of the sidebar.
  Framework: [
    {
      category: "Framework",
      created_at: 1,
      description: "internal",
      icon: null,
      is_loaded: false,
      last_active: null,
      name: "Framework Agent",
      node_count: 1,
      path: "/Users/mac/.hive-go/colonies/framework_agent",
      run_count: 0,
      session_count: 0,
      tags: [],
      tool_count: 0,
      workers: [],
    },
  ],
};

// No session for the fresh colony — this is the whole point of the fixture.
const SESSIONS = { sessions: [] as unknown[] };
const HISTORY = { sessions: [] as unknown[] };
const QUEENS = {
  queens: [
    {
      id: "queen_growth",
      name: "Steve Jobs",
      title: "Head of Growth",
      portrait: null,
      has_avatar: false,
    },
  ],
};

vi.mock("@/api/agents", () => ({
  agentsApi: { discover: () => Promise.resolve(DISCOVER) },
}));
vi.mock("@/api/sessions", () => ({
  sessionsApi: {
    list: () => Promise.resolve(SESSIONS),
    history: () => Promise.resolve(HISTORY),
  },
}));
vi.mock("@/api/queens", () => ({
  queensApi: { list: () => Promise.resolve(QUEENS) },
}));
vi.mock("@/api/config", () => ({
  configApi: {
    getProfile: () => Promise.resolve({ displayName: "", about: "", has_avatar: false }),
    setProfile: () => Promise.resolve(),
  },
}));
vi.mock("@/api/colonies", () => ({
  coloniesApi: { delete: () => Promise.resolve(), rename: () => Promise.resolve() },
}));

// The live multi-queen feed opens an IPC subscription in its provider; the
// sidebar only needs the two aggregation maps, so stub it as empty.
vi.mock("@/hooks/use-live-sessions", () => ({
  useLiveSessions: () => ({ rows: [], byQueen: new Map(), byColony: new Map(), connected: false }),
}));

// i18n: identity translator so assertions read the literal English label.
vi.mock("@/i18n", () => ({
  useI18n: () => ({ t: (s: string) => s, locale: "en", setLocale: () => {} }),
  I18nProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

// The tutorial overlay drives a scripted demo colony into the sidebar; keep it
// dormant so the only colony rows are the real ones from /discover.
vi.mock("@/components/Tutorial/TutorialOverlay", () => ({
  useTutorial: () => ({ active: false, steps: [], index: 0 }),
  TutorialProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

import { ColonyProvider } from "@/context/ColonyContext";
import { CreateColonyProvider } from "@/context/CreateColonyContext";
import Sidebar from "@/components/Sidebar";

/** Each test renders the sidebar inside the same providers AppLayout mounts. */
function renderSidebar() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <ColonyProvider>
        <CreateColonyProvider>
          <Sidebar />
        </CreateColonyProvider>
      </ColonyProvider>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  cleanup();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("sidebar — a freshly created colony is navigable", () => {
  it("renders a colony with no session and no history, linked to its route id", async () => {
    renderSidebar();

    // The row exists and carries the display name the runtime reported.
    const link = await screen.findByRole("link", { name: FRESH_DISPLAY });
    expect(link).toBeInTheDocument();

    // Critically: the href is the route id derived from the slug, not the slug
    // itself. `ui_e2e_012144` → `/colony/ui-e2e-012144`. A row that renders but
    // links to the raw slug 404s on click.
    expect(link).toHaveAttribute("href", `/colony/${FRESH_ROUTE_ID}`);
  });

  it("counts the fresh colony in the Colonies section total", async () => {
    renderSidebar();

    await screen.findByRole("link", { name: FRESH_DISPLAY });

    // The section header button reads "Colonies" and carries the count badge.
    const header = screen.getByRole("button", { name: /Colonies/ });
    expect(within(header).getByText("1")).toBeInTheDocument();
  });

  it("does not leak the internal Framework category into the sidebar", async () => {
    renderSidebar();

    await screen.findByRole("link", { name: FRESH_DISPLAY });

    // Negative assertion: /discover carries a "Framework" bucket that must be
    // filtered. If the filter regresses, every user sees internal agents.
    expect(screen.queryByRole("link", { name: "Framework Agent" })).toBeNull();
    expect(screen.queryByText("Framework Agent")).toBeNull();
  });
});
