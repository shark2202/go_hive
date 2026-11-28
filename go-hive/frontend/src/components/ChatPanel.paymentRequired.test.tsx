/**
 * A `payment_required` event must reach the transcript as the billing card,
 * not as a plain system row or silence. The unit tests cover the event →
 * message conversion and the card itself; this proves ChatPanel renders it.
 */
import { createElement } from "react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { installLayoutShim, uninstallLayoutShim } from "@/test/layout";
import type { ChatMessage } from "@/components/ChatPanel";

// Heavy / context-bound dependencies stubbed so ChatPanel renders in jsdom.
vi.mock("@/components/MarkdownContent", () => ({
  default: (p: { content?: string }) =>
    createElement("div", null, String(p?.content ?? "")),
}));
vi.mock("@/components/charts/ChartToolDetail", () => ({ default: () => null }));
vi.mock("@/components/TerminalToolDetail", () => ({ default: () => null }));
vi.mock("@/components/WorkerRunBubble", () => ({ default: () => null }));
vi.mock("@/components/QueenPortraitGlyph", () => ({ default: () => null }));
vi.mock("@/components/QuestionWidget", () => ({ default: () => null }));
vi.mock("@/components/MultiQuestionWidget", () => ({ default: () => null }));
vi.mock("@/components/ParallelSubagentBubble", () => ({ default: () => null }));
vi.mock("@/context/QueenProfileContext", () => ({
  useQueenProfile: () => ({ openQueenProfile: () => {} }),
}));
vi.mock("@/context/ColonyWorkersContext", () => ({
  useColonyWorkers: () => ({ openColonyWorkers: () => {} }),
}));
vi.mock("@/context/ColonyContext", () => ({
  useColony: () => ({ queenProfiles: [], queenAvatarVersion: () => 0 }),
}));
const emptyResult = () =>
  Object.assign([], { votes: [], items: [], sessions: [], skills: [], scopes: [] });
const apiProxy = () =>
  new Proxy({}, { get: () => () => Promise.resolve(emptyResult()) });
vi.mock("@/api/client", () => ({ apiUrl: (p: string) => p, api: apiProxy() }));
vi.mock("@/api/execution", () => ({ executionApi: apiProxy() }));

beforeAll(() => {
  installLayoutShim();
  (globalThis as Record<string, unknown>).IntersectionObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
    takeRecords() {
      return [];
    }
  };
});
afterAll(uninstallLayoutShim);

const paymentMessage: ChatMessage = {
  id: "payment-required-exec-1",
  agent: "System",
  agentColor: "",
  content: JSON.stringify({
    message: "insufficient_quota: You exceeded your current quota.",
    upstream_status: 402,
  }),
  timestamp: "",
  type: "payment_required",
  role: "queen",
  thread: "queen-dm",
  createdAt: Date.UTC(2026, 4, 14, 12, 0, 0),
};

describe("ChatPanel — payment_required", () => {
  it("renders the provider 402 as the billing card", async () => {
    const ChatPanel = (await import("@/components/ChatPanel")).default;
    render(
      createElement(ChatPanel, {
        messages: [paymentMessage],
        onSend: () => {},
        activeThread: "queen-dm",
      } as never),
    );

    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByText("Payment required.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /model settings/i })).toBeTruthy();
  });
});
