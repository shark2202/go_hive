/**
 * Hand-rolled i18n for the dashboard — no runtime dependencies.
 *
 * Design:
 *   - The English source text IS the translation key. `en` renders the key,
 *     so untranslated strings always read correctly instead of showing
 *     `[missing]` markers.
 *   - `zh-CN` looks the key up in a dictionary merged from per-lane files.
 *   - Missing keys fall back to the key itself.
 *   - `{name}` placeholders are interpolated for both locales.
 *
 * `useI18n()` works without a provider (defaults to `en`, `setLocale` is a
 * no-op) so existing tests and isolated component renders need no wrapper.
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { coreZh } from "./zh/core";
import { chatZh } from "./zh/chat";
import { pagesZh } from "./zh/pages";
import { settingsZh } from "./zh/settings";

export type Locale = "en" | "zh-CN";

export type TranslateVars = Record<string, string | number>;

/** localStorage key holding the user's locale choice. */
export const LOCALE_STORAGE_KEY = "hive.locale";

/**
 * Merged Chinese dictionary. Per-lane files are spread first and `coreZh`
 * last so the shell's strings win if two lanes ever define the same key.
 */
export const zh: Record<string, string> = {
  ...chatZh,
  ...pagesZh,
  ...settingsZh,
  ...coreZh,
};

const PLACEHOLDER = /\{(\w+)\}/g;

function interpolate(template: string, vars?: TranslateVars): string {
  if (!vars) return template;
  return template.replace(PLACEHOLDER, (match, name: string) =>
    Object.prototype.hasOwnProperty.call(vars, name) ? String(vars[name]) : match,
  );
}

/**
 * Translate `key` for `locale`. `en` returns the key verbatim (the key is the
 * English copy); `zh-CN` falls back to the key when the dictionary has no
 * entry. `{name}` placeholders are filled from `vars` in both locales.
 */
export function translate(
  locale: Locale,
  key: string,
  vars?: TranslateVars,
): string {
  if (locale === "en") return interpolate(key, vars);
  const template = Object.prototype.hasOwnProperty.call(zh, key) ? zh[key] : undefined;
  return interpolate(template || key, vars);
}

/** Normalise a stored/BCP-47 tag to a supported locale, or null. */
function normalizeLocale(value: string | null | undefined): Locale | null {
  if (!value) return null;
  const tag = value.trim().toLowerCase();
  if (tag === "zh" || tag.startsWith("zh-") || tag.startsWith("zh_")) return "zh-CN";
  if (tag === "en" || tag.startsWith("en-") || tag.startsWith("en_")) return "en";
  return null;
}

function detectFromNavigator(): Locale {
  if (typeof navigator === "undefined") return "en";
  return normalizeLocale(navigator.language) ?? "en";
}

/**
 * Resolve the initial locale: saved choice first, then the browser language
 * (`zh*` → `zh-CN`, everything else → `en`).
 */
export function detectLocale(): Locale {
  if (typeof window !== "undefined") {
    try {
      const stored = normalizeLocale(window.localStorage.getItem(LOCALE_STORAGE_KEY));
      if (stored) return stored;
    } catch {
      /* storage unavailable (private mode, SSR) — fall through */
    }
  }
  return detectFromNavigator();
}

export interface I18nContextValue {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: string, vars?: TranslateVars) => string;
}

const noopSetLocale = (_locale: Locale): void => {
  /* no provider — nothing to persist */
};

/**
 * Default context: English and a no-op setter. This is what `useI18n()`
 * returns outside `<I18nProvider>`, so components (and their tests) never
 * have to know whether a provider is mounted.
 */
const I18nContext = createContext<I18nContextValue>({
  locale: "en",
  setLocale: noopSetLocale,
  t: (key, vars) => translate("en", key, vars),
});

export function I18nProvider({ children }: { children: ReactNode }) {
  const [locale, setLocaleState] = useState<Locale>(() => detectLocale());

  const setLocale = useCallback((next: Locale) => {
    setLocaleState(next);
    if (typeof window !== "undefined") {
      try {
        window.localStorage.setItem(LOCALE_STORAGE_KEY, next);
      } catch {
        /* ignore quota/security errors — the in-memory choice still applies */
      }
    }
  }, []);

  // Keep localStorage and <html lang> in sync with the active locale. Runs on
  // mount too, so a first-run detection (browser language) is remembered.
  useEffect(() => {
    if (typeof document !== "undefined") {
      document.documentElement.lang = locale;
    }
    if (typeof window !== "undefined") {
      try {
        window.localStorage.setItem(LOCALE_STORAGE_KEY, locale);
      } catch {
        /* ignore */
      }
    }
  }, [locale]);

  // Follow changes made in another tab/window.
  useEffect(() => {
    if (typeof window === "undefined") return;
    const onStorage = (event: StorageEvent) => {
      if (event.key !== LOCALE_STORAGE_KEY) return;
      if (event.newValue === null) {
        setLocaleState(detectFromNavigator());
        return;
      }
      const next = normalizeLocale(event.newValue);
      if (next) setLocaleState(next);
    };
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const t = useCallback(
    (key: string, vars?: TranslateVars) => translate(locale, key, vars),
    [locale],
  );

  const value = useMemo<I18nContextValue>(
    () => ({ locale, setLocale, t }),
    [locale, setLocale, t],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18nContextValue {
  return useContext(I18nContext);
}
