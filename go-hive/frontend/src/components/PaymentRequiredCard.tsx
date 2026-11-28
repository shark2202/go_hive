import { useState } from "react";
import { AlertTriangle, ChevronRight } from "lucide-react";
import { useI18n } from "@/i18n";

/** Payload of a `payment_required` SSE event, as published by the
 *  runtime's EventBus for a provider 402:
 *
 *    { message: string, upstream_status: 402 | null }
 *
 *  `message` is the provider's own failure text; `upstream_status` is the
 *  HTTP status when the LLM layer could classify one. */
export interface PaymentRequired {
  /** The provider's message, as the runtime reported it. */
  detail: string;
  /** Upstream HTTP status (402 when the provider classified it). */
  upstreamStatus: number | null;
}

/** Read the JSON content of a `payment_required` chat message. Tolerates
 *  plain text (older logs, or a synthesised message) so the card still
 *  renders something useful. */
export function parsePaymentRequired(content: string): PaymentRequired {
  let detail = content.trim();
  let upstreamStatus: number | null = null;
  try {
    const parsed = JSON.parse(content) as {
      message?: unknown;
      upstream_status?: unknown;
    };
    if (typeof parsed.message === "string" && parsed.message.trim()) {
      detail = parsed.message.trim();
    }
    if (typeof parsed.upstream_status === "number") {
      upstreamStatus = parsed.upstream_status;
    }
  } catch {
    // Plain text, not the event's JSON envelope.
  }
  if (!detail) detail = "The LLM provider returned payment required.";
  return { detail, upstreamStatus };
}

/** A 402 from the model provider, in the chat: what happened, the way to
 *  fix it (credit or another model), and the raw error folded away.
 *  Mirrors ModelErrorCard's layout so both failures read the same way. */
export default function PaymentRequiredCard({ error }: { error: PaymentRequired }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  return (
    <div
      role="alert"
      className="mx-auto my-1 w-full max-w-2xl rounded-lg border border-amber-500/30 bg-amber-500/[0.06] px-3.5 py-2.5 text-xs"
    >
      <div className="flex items-start gap-3">
        <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-md bg-amber-500/15 text-amber-600 dark:text-amber-400">
          <AlertTriangle className="h-3.5 w-3.5" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-foreground">
            <span className="font-medium">{t("Payment required.")}</span>{" "}
            <span className="text-muted-foreground">
              {t(
                "The model provider rejected this request with 402 Payment Required. Add credit with the provider or switch models, then try again.",
              )}
            </span>
          </p>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1">
            <button
              type="button"
              onClick={() => (window.location.hash = "#/models")}
              className="font-medium text-primary underline-offset-2 hover:underline"
            >
              {t("Model settings →")}
            </button>
            <button
              type="button"
              onClick={() => setOpen((o) => !o)}
              aria-expanded={open}
              className="inline-flex items-center gap-0.5 text-muted-foreground hover:text-foreground"
            >
              <ChevronRight className={`h-3 w-3 transition-transform ${open ? "rotate-90" : ""}`} />
              {t("Details")}
            </button>
            {error.upstreamStatus != null && (
              <span className="ml-auto font-mono text-[10px] text-muted-foreground">
                HTTP {error.upstreamStatus}
              </span>
            )}
          </div>
          {open && (
            <pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md bg-muted/50 p-2 font-mono text-[11px] text-muted-foreground">
              {error.detail}
            </pre>
          )}
        </div>
      </div>
    </div>
  );
}
