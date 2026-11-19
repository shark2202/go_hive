/**
 * Compact EN | 中文 segmented control. Lives in the sidebar footer; switching
 * applies immediately (context re-render) and is persisted by the provider.
 */
import { useI18n, type Locale } from "./index";

const OPTIONS: { value: Locale; label: string }[] = [
  { value: "en", label: "EN" },
  { value: "zh-CN", label: "中文" },
];

export default function LocaleSwitcher({ className = "" }: { className?: string }) {
  const { locale, setLocale, t } = useI18n();

  return (
    <div
      role="group"
      aria-label={t("Language")}
      title={t("Language")}
      className={`inline-flex items-center gap-0.5 p-0.5 rounded-md bg-sidebar-item-hover/70 border border-border/50 ${className}`}
    >
      {OPTIONS.map((option) => {
        const selected = locale === option.value;
        return (
          <button
            key={option.value}
            type="button"
            onClick={() => setLocale(option.value)}
            aria-pressed={selected}
            className={`px-1.5 py-0.5 rounded text-[10px] leading-tight font-medium transition-colors ${
              selected
                ? "bg-primary/15 text-primary"
                : "text-sidebar-muted hover:text-foreground hover:bg-sidebar-item-hover"
            }`}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
