import { useCallback } from "react";
import { queensApi } from "@/api/queens";
import ToolsEditor from "./ToolsEditor";
import { useI18n } from "@/i18n";

export default function QueenToolsSection({ queenId }: { queenId: string }) {
  const { t } = useI18n();
  const fetchSnapshot = useCallback(
    () => queensApi.getTools(queenId),
    [queenId],
  );
  const saveAllowlist = useCallback(
    (enabled: string[] | null) => queensApi.updateTools(queenId, enabled),
    [queenId],
  );
  const resetToRoleDefault = useCallback(
    () => queensApi.resetTools(queenId),
    [queenId],
  );
  return (
    <ToolsEditor
      subjectKey={`queen:${queenId}`}
      title={t("Tools")}
      fetchSnapshot={fetchSnapshot}
      saveAllowlist={saveAllowlist}
      resetToRoleDefault={resetToRoleDefault}
    />
  );
}
