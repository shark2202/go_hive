import { describe, it, expect, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { I18nProvider, LOCALE_STORAGE_KEY } from "@/i18n";

import NotFound from "./not-found";

function renderPage(provider = false) {
  const page = (
    <MemoryRouter>
      <NotFound />
    </MemoryRouter>
  );
  return render(provider ? <I18nProvider>{page}</I18nProvider> : page);
}

describe("NotFound", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("reads in English by default", () => {
    renderPage();

    expect(screen.getByText("Page not found")).toBeTruthy();
    expect(screen.getByText("The page you’re looking for doesn’t exist.")).toBeTruthy();
    expect(screen.getByText("Back to Home")).toBeTruthy();
  });

  it("reads in Chinese when that is the chosen language", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "zh-CN");
    renderPage(true);

    expect(screen.getByText("页面不存在")).toBeTruthy();
    expect(screen.getByText("你要找的页面不存在。")).toBeTruthy();
    expect(screen.getByText("返回首页")).toBeTruthy();
  });
});
