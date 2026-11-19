import { useCallback } from "react";
import { coloniesApi } from "@/api/colonies";
import ToolsEditor from "./ToolsEditor";
import { useI18n } from "@/i18n";

export default function ColonyToolsSection({
  colonyName,
}: {
  colonyName: string;
}) {
  const { t } = useI18n();
  const fetchSnapshot = useCallback(
    () => coloniesApi.getTools(colonyName),
    [colonyName],
  );
  const saveAllowlist = useCallback(
    (enabled: string[] | null) => coloniesApi.updateTools(colonyName, enabled),
    [colonyName],
  );
  return (
    <ToolsEditor
      subjectKey={`colony:${colonyName}`}
      title={t("Tools")}
      caveat={t("Changes apply to the next worker spawn. Running workers keep the tool list they booted with.")}
      fetchSnapshot={fetchSnapshot}
      saveAllowlist={saveAllowlist}
    />
  );
}
