import { Link } from "react-router-dom";
import { useI18n } from "@/i18n";

export default function NotFound() {
  const { t } = useI18n();
  return (
    <div className="min-h-screen bg-background flex flex-col items-center justify-center px-6 text-center">
      <h1 className="text-5xl font-semibold text-foreground">404</h1>
      <p className="mt-3 text-sm text-muted-foreground">{t("Page not found")}</p>
      <p className="mt-1 text-sm text-muted-foreground/80">
        {t("The page you’re looking for doesn’t exist.")}
      </p>
      <Link
        to="/"
        className="mt-6 inline-flex items-center rounded-lg border border-border/40 px-4 py-2 text-sm font-medium text-foreground hover:bg-muted/40 transition-colors"
      >
        {t("Back to Home")}
      </Link>
    </div>
  );
}
