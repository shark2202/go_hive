import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";

// ── Controllable API surface ────────────────────────────────────────────────
// Fetch path under test: fetchColonies() builds each Colony's queen id from
// live sessions, queen-DM history, and (for cold colonies) the colony session
// tree. Metadata's queen_name is deliberately stale here to pin the rename
// regression: the newest cold colony session's queen_id must win.
const h = vi.hoisted(() => ({
  discover: vi.fn(),
  sessionsList: vi.fn(),
  history: vi.fn(),
  colonySessionsList: vi.fn(),
  queensList: vi.fn(),
}));

vi.mock("@/api/agents", () => ({ agentsApi: { discover: h.discover } }));
vi.mock("@/api/colonies", () => ({ coloniesApi: { delete: vi.fn() } }));
vi.mock("@/api/sessions", () => ({
  sessionsApi: { list: h.sessionsList, history: h.history },
  colonySessionsApi: { list: h.colonySessionsList },
}));
vi.mock("@/api/queens", () => ({ queensApi: { list: h.queensList } }));
vi.mock("@/api/config", () => ({
  configApi: {
    getProfile: vi.fn(async () => ({
      displayName: "",
      about: "",
      theme: "",
      has_avatar: false,
    })),
    setProfile: vi.fn(),
  },
}));
vi.mock("@/lib/me", () => ({ useMe: () => ({ me: null }) }));

import { ColonyProvider, useColony } from "./ColonyContext";

function Probe() {
  const { colonies, queenProfiles } = useColony();
  const colony = colonies[0];
  const profile = colony?.queenProfileId
    ? queenProfiles.find((q) => q.id === colony.queenProfileId)
    : null;
  return (
    <div data-testid="probe">
      {`${colony?.id ?? ""}|${colony?.queenProfileId ?? ""}|${profile?.title ?? ""}`}
    </div>
  );
}

function discoverEntry(queenName: string) {
  return {
    path: "/root/.hive/colonies/go_hive",
    name: "Go Hive",
    description: "",
    category: "Your Agents",
    session_count: 1,
    run_count: 0,
    node_count: 0,
    tool_count: 0,
    tags: [],
    last_active: null,
    created_at: null,
    icon: null,
    is_loaded: false,
    workers: [
      {
        name: "worker",
        config_path: "",
        description: "",
        tool_count: 0,
        task: "",
        spawned_at: "",
        queen_name: queenName,
        colony_id: "go_hive",
      },
    ],
  };
}

describe("ColonyContext — colony queen mapping", () => {
  beforeEach(() => {
    localStorage.clear();
    h.discover.mockReset().mockResolvedValue({ "Your Agents": [discoverEntry("queen_growth")] });
    h.sessionsList.mockReset().mockResolvedValue({ sessions: [] });
    h.history.mockReset().mockResolvedValue({ sessions: [] });
    h.colonySessionsList.mockReset().mockResolvedValue({
      sessions: [
        {
          session_id: "sess-old",
          cold: true,
          live: false,
          has_messages: true,
          created_at: 1,
          queen_id: "queen_custom_72601",
        },
      ],
    });
    h.queensList.mockReset().mockResolvedValue({
      queens: [
        { id: "queen_growth", name: "Steve Jobs", title: "Head of Growth" },
        { id: "queen_custom_72601", name: "小周", title: "软件研发" },
      ],
    });
  });

  // Parity-exempt: frontend-only regression test for the colony-rename bug
  // (no upstream test exercises the renderer's colony queen mapping).
  it("prefers a cold colony session's queen_id over stale metadata", async () => {
    render(
      <ColonyProvider>
        <Probe />
      </ColonyProvider>,
    );

    await waitFor(() =>
      expect(screen.getByTestId("probe").textContent).toBe(
        "go-hive|queen_custom_72601|软件研发",
      ),
    );
    // The API is keyed by the disk slug, never the route display slug.
    expect(h.colonySessionsList).toHaveBeenCalledWith("go_hive");
    // A header rendering this colony's queen profile must not show the stale
    // metadata queen's title ("Head of Growth") for 小周's session.
    expect(screen.getByTestId("probe").textContent).not.toContain("Head of Growth");
  });
});
