import React, { useCallback, useState } from "react";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import {
  refreshPythonCatalogs,
  refreshPythonLifecycle,
  refreshPythonPackages,
} from "src/core/StashService";
import { usePythonSettingsQuery } from "src/core/generated-graphql";
import { JobFragment } from "src/utils/job";
import { SettingSection } from "../SettingSection";
import { PythonIndexes } from "./PythonIndexes";
import { PythonJobProgress } from "./PythonJobProgress";
import { PythonPackages } from "./PythonPackages";
import { PythonRuntimeTable } from "./PythonRuntimeTable";
import { PythonStatusCard } from "./PythonStatusCard";
import {
  ActivePythonJob,
  PythonJobImpact,
  StartPythonJob,
} from "./types";

export const SettingsPythonPanel: React.FC = () => {
  const [allVersions, setAllVersions] = useState(false);
  const [activeJob, setActiveJob] = useState<ActivePythonJob>();
  const settings = usePythonSettingsQuery({
    variables: { allVersions },
    notifyOnNetworkStatusChange: true,
  });
  const refetchSettings = settings.refetch;

  const startJob: StartPythonJob = useCallback((id, impact) => {
    setActiveJob({ id, impact });
  }, []);

  const finishJob = useCallback(
    (_job: JobFragment | undefined, impact: PythonJobImpact) => {
      setActiveJob(undefined);
      if (impact === "lifecycle") refreshPythonLifecycle();
      if (impact === "packages") refreshPythonPackages();
      if (impact === "catalogs") refreshPythonCatalogs();
      void refetchSettings();
    },
    [refetchSettings]
  );

  if (settings.loading && !settings.data) return <LoadingIndicator />;
  if (settings.error && !settings.data) {
    return <div className="text-danger">{settings.error.message}</div>;
  }
  if (!settings.data) return null;

  const { pythonStatus, pythonRuntimes, pythonIndexes, pythonCatalogs } =
    settings.data;
  const managerAvailable = pythonStatus.manager.installed;
  const busy = !!activeJob;

  return (
    <div id="python-settings">
      <SettingSection
        headingID="config.categories.python"
        subHeadingID="config.python.description"
      >
        <PythonStatusCard
          status={pythonStatus}
          busy={busy}
          onStartJob={startJob}
        />
        <PythonJobProgress active={activeJob} onFinished={finishJob} />
      </SettingSection>

      <SettingSection
        headingID="config.python.available_versions"
        subHeadingID="config.python.available_versions_description"
      >
        <PythonRuntimeTable
          runtimes={pythonRuntimes}
          managerAvailable={managerAvailable}
          managedEnvironment={pythonStatus.managedEnvironment}
          busy={busy}
          allVersions={allVersions}
          onAllVersionsChange={setAllVersions}
          onStartJob={startJob}
        />
      </SettingSection>

      <SettingSection
        headingID="config.python.installed_packages"
        subHeadingID="config.python.installed_packages_description"
      >
        <PythonPackages
          indexes={pythonIndexes}
          catalogs={pythonCatalogs}
          managerAvailable={managerAvailable}
          runtimeAvailable={!!pythonStatus.selectedRuntime}
          managedEnvironment={pythonStatus.managedEnvironment}
          busy={busy}
          onStartJob={startJob}
        />
      </SettingSection>

      <SettingSection
        headingID="config.python.package_indexes"
        subHeadingID="config.python.package_indexes_description"
      >
        <PythonIndexes
          indexes={pythonIndexes}
          catalogs={pythonCatalogs}
          busy={busy}
          onStartJob={startJob}
        />
      </SettingSection>
    </div>
  );
};
