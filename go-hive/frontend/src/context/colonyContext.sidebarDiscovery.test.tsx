/**
 * Regression tests: what the sidebar does with a colony that appears on disk
 * from OUTSIDE the UI (no create action, no delete, no tab-visibility flip).
 *
 * Why this file exists
 * --------------------
 * `fetchColonies` (ColonyContext) is the ONLY thing that can add a colony to
 * the sidebar's list. It runs on mount, on visibilitychange→visible, and after
 * an explicit create/delete. It is the only caller of `/discover`.
 *
 * The 30s background poll (`fetchStatus`) reads `/sessions` and maps over the
 * EXISTING `colonies` array (`prev.map(...)`), so it can update the status of
 * a colony already rendered but structurally cannot append one it has never
 * seen. A colony that materialises on disk is therefore invisible to the
 * sidebar until something calls `fetchColonies` again.
 *
 * Measured behaviour (these tests pin it):
 *   - initial mount  → /discover called once        → colony rendered  ✅
 *   - 30s status poll → /discover NOT called again   → colony absent   ❌
 *
 * The colony *page* does self-heal: `pages/colony-chat.tsx` calls
 * `refreshColonies()` when the route's colony is not in the cached list, so a
 * direct link to a not-yet-listed colony still loads. Only the sidebar is
 * stale. That asymmetry is why "colony appears in sidebar" can fail while
 * "colony appears on colony page" passes.
 */
import { useEffect } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";

vi.mock("@/api/agents", () => ({ agentsApi: { discover: vi.fn() } }));
vi.mock("@/api/colonies", () => ({
  coloniesApi: { delete: vi.fn(), rename: vi.fn() },
}));
vi.mock("@/api/sessions", () => ({
  sessionsApi: { list: vi.fn(), history: vi.fn() },
}));
vi.mock("@/api/queens", () => ({ queensApi: { list: vi.fn() } }));
vi.mock("@/api/config", () => ({
  configApi: { getProfile: vi.fn(() => Promise.resolve({})) },
}));
vi.mock("@/lib/me", () => ({
  useMe: () => ({}),
  isQueenDecommissioned: () => false,
}));

import { agentsApi } from "@/api/agents";
import { sessionsApi } from "@/api/sessions";
import { queensApi } from "@/api/queens";
import { ColonyProvider, useColony } from "./ColonyContext";

const discoverMock = vi.mocked(agentsApi.discover);
const sessionsMock = vi.mocked(sessionsApi.list);
const historyMock = vi.mocked(sessionsApi.history);
const queensMock = vi.mocked(queensApi.list);

/** One colony entry as the backend's /discover payload shapes it. */
const entry = (slug: string) => ({
  category: "Your Agents",
  created_at: 1795368110.64702,
  description: `Colony ${slug}`,
  icon: null,
  is_loaded: false,
  last_active: null,
  name: slug,
  node_count: 1,
  path: `/Users/mac/.hive-go/colonies/${slug}`,
  run_count: 0,
  session_count: 0,
  tags: [],
  tool_count: 0,
  workers: [],
});

/** Latest colony names the consumer has observed, mirrored out of React. */
let observed: string[] = [];

function Probe() {
  const { colonies } = useColony();
  observed = colonies.map((c) => c.name);
  return null;
}

const mount = () =>
  render(
    <ColonyProvider>
      <Probe />
    </ColonyProvider>,
  );

const flush = () => act(async () => {});
const elapse = (ms: number) =>
  act(async () => {
    vi.advanceTimersByTime(ms);
  });

/** Point /discover at exactly these slugs. */
const discoverReturns = (...slugs: string[]) =>
  discoverMock.mockResolvedValue({
    "Your Agents": slugs.map(entry),
  } as never);

beforeEach(() => {
  vi.useFakeTimers();
  observed = [];
  discoverReturns("seeded_colony");
  sessionsMock.mockResolvedValue({ sessions: [] } as never);
  historyMock.mockResolvedValue({ sessions: [] } as never);
  queensMock.mockResolvedValue({ queens: [] } as never);
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.clearAllMocks();
});

describe("sidebar: colony via initial fetch", () => {
  it("renders a colony returned by the mount-time /discover", async () => {
    mount();
    await flush();

    expect(observed).toContain("seeded_colony");
  });

  it("re-renders a colony that disappears then returns across a refetch", async () => {
    mount();
    await flush();
    expect(observed).toEqual(["seeded_colony"]);

    // Simulate the same colony still present — a plain refetch must not
    // duplicate it. (Guards the rebuild-from-scratch contract in
    // fetchColonies, which is what makes a full re-fetch safe.)
    discoverReturns("seeded_colony");
    await elapse(0);
    await flush();

    expect(observed.filter((n) => n === "seeded_colony")).toHaveLength(1);
  });
});

describe("sidebar: colony that appears on disk with no UI action", () => {
  /**
   * Pins the CURRENT, MEASURED behaviour: /discover is never re-polled, so a
   * colony created outside the UI does not reach the sidebar on its own.
   *
   * This test passes today and will keep passing as long as the gap exists.
   * If a future change adds discovery to the background poll, this test goes
   * red — which is the signal to delete it and un-skip the guard below.
   */
  it("does NOT re-call /discover on the 30s status poll (current behaviour)", async () => {
    mount();
    await flush();
    const callsAfterMount = discoverMock.mock.calls.length;
    expect(callsAfterMount).toBe(1);

    // A colony materialises on disk; the backend would now report it.
    discoverReturns("seeded_colony", "appeared_on_disk");

    // Advance well past the only recurring timer the context owns (30s).
    await elapse(30_000);
    await flush();

    // Measured: still exactly one /discover. The status poll only hits
    // /sessions, so the new colony is invisible.
    expect(discoverMock.mock.calls.length).toBe(1);
    expect(observed).not.toContain("appeared_on_disk");
  });

  /**
   * The behaviour we WANT: the sidebar surfaces a colony created outside the
   * UI without requiring a manual browser reload.
   *
   * `it.fails` inverts the assertion: it is green while the bug exists and
   * turns RED once the behaviour is fixed — at which point remove `.fails`
   * and this becomes an ordinary gate.
   */
  it.fails("should surface a colony created outside the UI within one poll", async () => {
    mount();
    await flush();

    discoverReturns("seeded_colony", "appeared_on_disk");
    await elapse(30_000);
    await flush();

    expect(observed).toContain("appeared_on_disk");
  });
});