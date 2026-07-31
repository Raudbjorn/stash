import React, { useState } from "react";
import { Badge, Button, Form, Table } from "react-bootstrap";
import { FormattedMessage, useIntl } from "react-intl";
import { AlertModal } from "src/components/Shared/Alert";
import {
  mutateInstallPythonRuntime,
  mutateSelectPythonRuntime,
} from "src/core/StashService";
import {
  PythonRuntimeDataFragment,
  PythonRuntimeSource,
} from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { StartPythonJob } from "./types";

interface PythonRuntimeTableProps {
  runtimes: PythonRuntimeDataFragment[];
  managerAvailable: boolean;
  managedEnvironment: boolean;
  busy: boolean;
  allVersions: boolean;
  onAllVersionsChange: (value: boolean) => void;
  onStartJob: StartPythonJob;
}

export const PythonRuntimeTable: React.FC<PythonRuntimeTableProps> = ({
  runtimes,
  managerAvailable,
  managedEnvironment,
  busy,
  allVersions,
  onAllVersionsChange,
  onStartJob,
}) => {
  const Toast = useToast();
  const intl = useIntl();
  const [selecting, setSelecting] = useState<PythonRuntimeDataFragment>();

  async function install(runtime: PythonRuntimeDataFragment) {
    try {
      const result = await mutateInstallPythonRuntime(runtime.id);
      const id = result.data?.installPythonRuntime;
      if (id) onStartJob(id, "lifecycle");
    } catch (error) {
      Toast.error(error);
    }
  }

  async function useRuntime() {
    if (!selecting) return;
    try {
      const result = await mutateSelectPythonRuntime(selecting.id);
      const id = result.data?.selectPythonRuntime;
      if (id) onStartJob(id, "lifecycle");
      setSelecting(undefined);
    } catch (error) {
      Toast.error(error);
    }
  }

  return (
    <>
      <div className="python-toolbar">
        <Form.Check
          id="python-all-versions"
          type="switch"
          checked={allVersions}
          onChange={(event) => onAllVersionsChange(event.currentTarget.checked)}
          label={<FormattedMessage id="config.python.show_all_versions" />}
          disabled={busy || !managerAvailable}
        />
      </div>
      <div className="python-table-wrap">
        <Table responsive hover className="python-runtime-table">
          <thead>
            <tr>
              <th><FormattedMessage id="config.python.runtime" /></th>
              <th><FormattedMessage id="config.python.provenance" /></th>
              <th><FormattedMessage id="config.python.location" /></th>
              <th><FormattedMessage id="config.python.actions" /></th>
            </tr>
          </thead>
          <tbody>
            {runtimes.map((runtime) => (
              <tr key={runtime.id}>
                <td>
                  <strong>{runtime.implementation} {runtime.version}</strong>
                  <div className="text-muted">
                    {runtime.architecture}
                    {runtime.libc ? ` / ${runtime.libc}` : ""} / {runtime.variant}
                  </div>
                  {runtime.selected && (
                    <Badge variant="success">
                      <FormattedMessage id="config.python.current" />
                    </Badge>
                  )}
                  {runtime.installed && (
                    <Badge variant="secondary">
                      <FormattedMessage id="config.python.installed" />
                    </Badge>
                  )}
                </td>
                <td>{runtime.source}</td>
                <td>
                  {runtime.downloadURL && (
                    <a href={runtime.downloadURL} target="_blank" rel="noreferrer">
                      <FormattedMessage id="config.python.managed_by_uv" />
                    </a>
                  )}
                  {runtime.path && <div className="python-mono">{runtime.path}</div>}
                  {runtime.environmentPath && (
                    <div className="python-mono">{runtime.environmentPath}</div>
                  )}
                </td>
                <td>
                  {!runtime.installed &&
                    runtime.source === PythonRuntimeSource.Managed && (
                      <Button
                        size="sm"
                        disabled={busy || !managerAvailable}
                        onClick={() => install(runtime)}
                      >
                        <FormattedMessage id="package_manager.install" />
                      </Button>
                    )}
                  {runtime.installed && (!runtime.selected || !managedEnvironment) && (
                    <Button
                      size="sm"
                      variant="secondary"
                      disabled={busy || !managerAvailable}
                      onClick={() => setSelecting(runtime)}
                    >
                      <FormattedMessage id="config.python.use" />
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
      </div>
      <AlertModal
        show={!!selecting}
        text={<FormattedMessage id="config.python.use_confirmation" />}
        confirmVariant="primary"
        confirmButtonText={intl.formatMessage({ id: "config.python.use" })}
        onConfirm={useRuntime}
        onCancel={() => setSelecting(undefined)}
      />
    </>
  );
};
