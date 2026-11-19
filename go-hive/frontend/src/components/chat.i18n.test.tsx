/**
 * zh smoke test for the chat lane: proves the per-lane dictionary is merged,
 * the components consume `useI18n`, and placeholders interpolate. The full
 * suite runs in `en`, where the English key is rendered verbatim — this file
 * is the only place the Chinese copy is exercised end to end.
 */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { I18nProvider, LOCALE_STORAGE_KEY, translate } from "@/i18n";
import type { TaskRecord } from "@/api/tasks";
import ThinkingBlock from "./ThinkingBlock";
import ModelErrorCard from "./ModelErrorCard";
import QuestionWidget from "./QuestionWidget";
import TaskItem from "./TaskItem";

const task: TaskRecord = {
  id: 1,
  subject: "Draft the launch note",
  description: "",
  active_form: null,
  owner: null,
  status: "pending",
  blocks: [],
  blocked_by: [3],
  metadata: {},
  created_at: 1_700_000_000,
  updated_at: 1_700_000_000,
};

function renderZh(node: React.ReactNode) {
  localStorage.setItem(LOCALE_STORAGE_KEY, "zh-CN");
  return render(<I18nProvider>{node}</I18nProvider>);
}

beforeEach(() => localStorage.clear());
afterEach(() => {
  cleanup();
  localStorage.clear();
});

describe("chat surface — Chinese", () => {
  it("translates the thinking header", () => {
    renderZh(<ThinkingBlock content="weighing options" />);
    expect(screen.getByText("思考")).toBeTruthy();
  });

  it("translates a model failure card, including the settings link", () => {
    renderZh(<ModelErrorCard error={{ detail: "boom", keyProblem: true }} />);
    expect(screen.getByText("模型未能回复。")).toBeTruthy();
    expect(screen.getByText("API 密钥缺失或已被拒绝。")).toBeTruthy();
    expect(screen.getByRole("button", { name: /设置模型/ })).toBeTruthy();
  });

  it("translates question-widget chrome without touching the question text", () => {
    renderZh(
      <QuestionWidget
        question="Pick a direction"
        options={["A", "B"]}
        onSubmit={() => {}}
      />,
    );
    expect(screen.getByText("Pick a direction")).toBeTruthy();
    expect(screen.getByPlaceholderText("输入自定义回复…")).toBeTruthy();
    expect(screen.getByRole("button", { name: /提交/ })).toBeTruthy();
  });

  it("translates task status affordances and blocker copy", () => {
    renderZh(<TaskItem task={task} unresolvedBlockers={[3]} />);
    expect(screen.getByLabelText("等待依赖")).toBeTruthy();
    expect(screen.getByText(/被阻塞于/)).toBeTruthy();
    // User/agent-authored data stays as-is.
    expect(screen.getByText("Draft the launch note")).toBeTruthy();
  });

  it("interpolates chat-lane placeholders", () => {
    expect(translate("zh-CN", "{count} questions", { count: 2 })).toBe("2 个问题");
    expect(translate("zh-CN", "No activity for {seconds}s", { seconds: 35 })).toBe(
      "已 35 秒无活动",
    );
  });
});
