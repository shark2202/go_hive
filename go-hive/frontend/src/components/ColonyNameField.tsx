/**
 * Colony-name input shared by every place that creates a colony: the sidebar
 * modal, the queen-DM "Start Colony" dialog and the colony pivot dialog.
 *
 * The backend accepts a colony id matching `^[a-z0-9_]+$` only (it becomes a
 * directory name). The old inputs enforced that by rewriting the value on every
 * keystroke, which silently swallowed any character the rule rejects:
 *
 *   - a Chinese IME showed the pinyin during composition, then the committed
 *     characters were stripped, leaving the field blank;
 *   - capitals, spaces and hyphens vanished as the user typed, so the field
 *     never showed what they actually entered.
 *
 * Keep the raw text in the field and show what the slug will be (or why the
 * value cannot be used) underneath. Callers submit `colonySlug(value)`, never
 * the raw text, so the on-disk id is unchanged.
 */

import { useI18n } from "@/i18n";

/** Collapse raw text into a colony id. "" drops rejected chars; "_" folds
 *  each run of them into one underscore (the sidebar modal's convention). */
export function colonySlug(raw: string, separator: "" | "_" = ""): string {
  const pattern = separator === "_" ? /[^a-z0-9_]+/g : /[^a-z0-9_]/g;
  return raw.toLowerCase().replace(pattern, separator);
}

/** A slug is usable only when it carries at least one letter or digit;
 *  "_" / "__" match the backend regex but name nothing a person typed. */
export function isUsableColonySlug(slug: string): boolean {
  return /[a-z0-9]/.test(slug);
}

interface ColonyNameFieldProps {
  value: string;
  onChange: (next: string) => void;
  placeholder?: string;
  autoFocus?: boolean;
  /** Slug rule for this surface; defaults to dropping rejected characters. */
  separator?: "" | "_";
  inputClassName?: string;
}

const DEFAULT_INPUT_CLASS =
  "w-full rounded-md border border-border/60 bg-background px-3 py-2 text-sm text-foreground placeholder:text-muted-foreground/50 focus:outline-none focus:ring-1 focus:ring-primary";

export default function ColonyNameField({
  value,
  onChange,
  placeholder,
  autoFocus = false,
  separator = "",
  inputClassName = DEFAULT_INPUT_CLASS,
}: ColonyNameFieldProps) {
  const { t } = useI18n();
  const trimmed = value.trim();
  const slug = colonySlug(value, separator);
  const usable = isUsableColonySlug(slug);
  const normalized = trimmed !== "" && slug !== trimmed.toLowerCase();

  return (
    <>
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoFocus={autoFocus}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        aria-invalid={trimmed !== "" && !usable}
        className={inputClassName}
      />
      {trimmed !== "" &&
        (!usable ? (
          <p className="mt-1 text-[10.5px] leading-snug text-destructive">
            {t(
              "Colony names allow lowercase letters, digits and underscores only — put Chinese in the goal instead.",
            )}
          </p>
        ) : (
          normalized && (
            <p className="mt-1 text-[10.5px] leading-snug text-muted-foreground">
              {t("Will be created as “{slug}”", { slug })}
            </p>
          )
        ))}
    </>
  );
}
