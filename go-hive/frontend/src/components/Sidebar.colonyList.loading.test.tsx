/**
 * Regression guard for the sidebar's loading window.
 *
 * The sibling Sidebar.colonyList.test.tsx guards the happy path: once /discover
 * resolves, a freshly created colony renders as a navigable row. It does NOT
 * cover the window BEFORE that response lands.
 *
 * That gap was a real, user-visible bug (found 2026-11-23 while re-verifying the
 * UI E2E). `colonies` starts as `[]` and the sidebar rendered the empty state
 * unconditionally, so on every cold load — and on every slow /discover — the
 * user was told "No colonies yet" even when the runtime had 21 of them. It
 * rendered *simultaneously* with the "Loading queens..." line, because that
 * section gates on its own data and this one did not.
 *
 * The invariant: an empty colony list means "no colonies" ONLY once the fetch
 * has completed. While `loading` is true the sidebar must say it is loading.
 *
 * The fixture delays /discover so the loading window is observable; with an
 * instant mock the assertion would pass trivially and catch nothing.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

const FRESH_DISPLAY = "Ui E2E 012144";
const SLUG = "ui_e2e_012144";

const DISCOVER = {
  "Your Agents": [
    {
      category: "Your Agents",
      created_at: 1795368110.64702,
      description: `Colony ${SLUG}`,
      icon: null,
      is_loaded: false,
      last_active: null,
      name: FRESH_DISPLAY,
      node_count: 1,
      path: `/Users/mac/.hive-go/colonies/${SLUG}`,
      run_count: 0,
      session_count: 0,
      tags: [],
      tool_count: 0,
      workers: [],
    },
  ],
};

vi.mock("@/api/agents", () => ({
  agentsApi: {
    // Deliberately slow: this is what exposes the loading window.
    discover: () => new Promise((res) => setTimeout(() => res(DISCOVER), 50)),
  },
}));
vi.mock("@/api/sessions", () => ({
  sessionsApi: {
    list: () => Promise.resolve({ sessions: [] }),
    history: () => Promise.resolve({ sessions: [] }),
  },
}));
vi.mock("@/api/queens", () => ({
  queensApi: {
    list: () =>
      Promise.resolve({
        queens: [
          {
            id: "queen_growth",
            name: "Steve Jobs",
            title: "Head of Growth",
            portrait: null,
            has_avatar: false,
          },
        ],
      }),
  },
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
vi.mock("@/hooks/use-live-sessions", () => ({
  useLiveSessions: () => ({ rows: [], byQueen: new Map(), byColony: new Map(), connected: false }),
}));
vi.mock("@/i18n", () => ({
  useI18n: () => ({ t: (s: string) => s, locale: "en", setLocale: () => {} }),
  I18nProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));
vi.mock("@/components/Tutorial/TutorialOverlay", () => ({
  useTutorial: () => ({ active: false, steps: [], index: 0 }),
  TutorialProvider: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

import { ColonyProvider } from "@/context/ColonyContext";
import { CreateColonyProvider } from "@/context/CreateColonyContext";
import Sidebar from "@/components/Sidebar";

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

beforeEach(() => cleanup());
afterEach(() => vi.clearAllMocks());

describe("sidebar loading window", () => {
  it("does NOT claim 'No colonies yet' before /discover resolves", async () => {
    renderSidebar();

    // The fetch is still in flight. Claiming emptiness here tells the user
    // their colonies are gone when the app simply has not heard back yet.
    expect(
      screen.queryByText("No colonies yet"),
      "sidebar asserted an empty colony list before /discover resolved",
    ).toBeNull();

    // And it must not simultaneously look idle: it should say it is loading.
    expect(screen.getByText("Loading colonies...")).toBeInTheDocument();

    // Once the response lands the real colony renders and the placeholder goes.
    expect(await screen.findByRole("link", { name: FRESH_DISPLAY })).toBeInTheDocument();
    expect(screen.queryByText("Loading colonies...")).toBeNull();
  });
});
