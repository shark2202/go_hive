import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, renderHook, fireEvent, cleanup } from "@testing-library/react";
import {
  I18nProvider,
  LOCALE_STORAGE_KEY,
  detectLocale,
  translate,
  useI18n,
} from "./index";
import LocaleSwitcher from "./LocaleSwitcher";
import { coreZh } from "./zh/core";

function Probe() {
  const { t, locale, setLocale } = useI18n();
  return (
    <div>
      <span data-testid="text">{t("New Chat")}</span>
      <span data-testid="locale">{locale}</span>
      <button type="button" onClick={() => setLocale("zh-CN")}>
        go-zh
      </button>
    </div>
  );
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.lang = "";
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("translate", () => {
  it("returns the key verbatim in en (the key IS the English copy)", () => {
    expect(translate("en", "New Chat")).toBe("New Chat");
    expect(translate("en", "A string nobody translated yet")).toBe(
      "A string nobody translated yet",
    );
  });

  it("looks the key up in the zh dictionary", () => {
    expect(translate("zh-CN", "New Chat")).toBe("新建对话");
    expect(translate("zh-CN", "Colonies")).toBe("蜂群");
  });

  it("falls back to the key when zh has no translation", () => {
    expect(translate("zh-CN", "Not translated anywhere")).toBe("Not translated anywhere");
  });

  it("never emits a [missing] marker", () => {
    const out = translate("zh-CN", "definitely-not-a-key");
    expect(out).not.toMatch(/missing/i);
    expect(out).toBe("definitely-not-a-key");
  });

  it("interpolates {name} placeholders in both locales", () => {
    expect(translate("en", "v{version} is now available", { version: "1.2.3" })).toBe(
      "v1.2.3 is now available",
    );
    expect(translate("zh-CN", "v{version} is now available", { version: "1.2.3" })).toBe(
      "v1.2.3 现已可用",
    );
    expect(
      translate("zh-CN", "View {name}'s profile", { name: "Ada" }),
    ).toBe("查看 Ada 的主页");
  });

  it("leaves unknown placeholders untouched and accepts numbers", () => {
    expect(translate("en", "Hi {name}", {})).toBe("Hi {name}");
    expect(translate("en", "Count {n}", { n: 3 })).toBe("Count 3");
  });

  it("has a non-empty zh value for every core key", () => {
    for (const [key, value] of Object.entries(coreZh)) {
      expect(value.trim(), `empty translation for ${key}`).not.toBe("");
    }
  });
});

describe("detectLocale", () => {
  it("prefers the stored locale", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "zh-CN");
    expect(detectLocale()).toBe("zh-CN");
  });

  it("accepts a bare zh tag from storage", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "zh");
    expect(detectLocale()).toBe("zh-CN");
  });

  it("falls back to navigator.language when storage is empty (zh → zh-CN)", () => {
    vi.spyOn(navigator, "language", "get").mockReturnValue("zh-TW");
    expect(detectLocale()).toBe("zh-CN");
  });

  it("falls back to en for a non-Chinese browser", () => {
    vi.spyOn(navigator, "language", "get").mockReturnValue("en-US");
    expect(detectLocale()).toBe("en");
  });
});

describe("I18nProvider", () => {
  it("renders zh from the stored locale and syncs <html lang>", () => {
    localStorage.setItem(LOCALE_STORAGE_KEY, "zh-CN");
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("locale").textContent).toBe("zh-CN");
    expect(screen.getByTestId("text").textContent).toBe("新建对话");
    expect(document.documentElement.lang).toBe("zh-CN");
  });

  it("defaults to the browser language when nothing is stored", () => {
    vi.spyOn(navigator, "language", "get").mockReturnValue("zh-CN");
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("locale").textContent).toBe("zh-CN");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh-CN");
  });

  it("persists a locale change and applies it immediately", () => {
    render(
      <I18nProvider>
        <Probe />
      </I18nProvider>,
    );
    expect(screen.getByTestId("text").textContent).toBe("New Chat");

    fireEvent.click(screen.getByRole("button", { name: "go-zh" }));

    expect(screen.getByTestId("text").textContent).toBe("新建对话");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });
});

describe("useI18n without a provider", () => {
  it("defaults to en, translates, and no-ops setLocale instead of crashing", () => {
    const { result } = renderHook(() => useI18n());
    expect(result.current.locale).toBe("en");
    expect(result.current.t("New Chat")).toBe("New Chat");

    expect(() => result.current.setLocale("zh-CN")).not.toThrow();
    expect(result.current.locale).toBe("en");
  });

  it("lets components render standalone", () => {
    render(<Probe />);
    expect(screen.getByTestId("text").textContent).toBe("New Chat");
  });
});

describe("LocaleSwitcher", () => {
  it("switches to 中文 and marks the active option", () => {
    render(
      <I18nProvider>
        <Probe />
        <LocaleSwitcher />
      </I18nProvider>,
    );

    const zhButton = screen.getByRole("button", { name: "中文" });
    const enButton = screen.getByRole("button", { name: "EN" });
    expect(enButton).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(zhButton);

    expect(screen.getByTestId("text").textContent).toBe("新建对话");
    expect(zhButton).toHaveAttribute("aria-pressed", "true");
    expect(enButton).toHaveAttribute("aria-pressed", "false");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("zh-CN");

    fireEvent.click(enButton);
    expect(screen.getByTestId("text").textContent).toBe("New Chat");
    expect(localStorage.getItem(LOCALE_STORAGE_KEY)).toBe("en");
  });
});
