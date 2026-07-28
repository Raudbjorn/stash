import React, { useEffect, useState } from "react";
import { Badge, Button, Form } from "react-bootstrap";
import { FormattedMessage } from "react-intl";
import { Setting } from "../Inputs";
import { useSettings } from "../context";
import { mutateInstallPythonManager } from "src/core/StashService";
import { PythonStatusDataFragment } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { StartPythonJob } from "./types";

interface PythonStatusCardProps {
  status: PythonStatusDataFragment;
  busy: boolean;
  onStartJob: StartPythonJob;
}

export const PythonStatusCard: React.FC<PythonStatusCardProps> = ({
  status,
  busy,
  onStartJob,
}) => {
  const Toast = useToast();
  const { advancedMode, saveGeneral } = useSettings();
  const [externalPath, setExternalPath] = useState(
    status.configuredPythonPath
  );

  useEffect(() => {
    setExternalPath(status.configuredPythonPath);
  }, [status.configuredPythonPath]);

  async function installManager() {
    try {
      const result = await mutateInstallPythonManager();
      const id = result.data?.installPythonManager;
      if (id) onStartJob(id, "lifecycle");
    } catch (error) {
      Toast.error(error);
    }
  }

  return (
    <>
      <div className="python-runtime-chain" aria-label="Python runtime chain">
        <span className={status.manager.installed ? "ready" : "missing"}>
          uv
        </span>
        <i aria-hidden="true">→</i>
        <span className={status.selectedRuntime ? "ready" : "missing"}>
          Python
        </span>
        <i aria-hidden="true">→</i>
        <span className={status.managedEnvironment ? "ready" : "missing"}>
          environment
        </span>
        <i aria-hidden="true">→</i>
        <span>plugins &amp; scrapers</span>
      </div>

      <Setting
        heading={<FormattedMessage id="config.python.manager_heading" />}
        subHeadingID="config.python.manager_description"
      >
        <div>
          {status.manager.installed ? (
            <>
              <Badge variant="success">
                uv {status.manager.version ?? ""}
              </Badge>
              <div className="python-mono">{status.manager.path}</div>
            </>
          ) : (
            <Button disabled={busy} onClick={installManager}>
              <FormattedMessage id="config.python.install_uv" />
            </Button>
          )}
          {status.manager.error && (
            <div className="text-danger">{status.manager.error}</div>
          )}
        </div>
      </Setting>

      <Setting
        heading={<FormattedMessage id="config.python.current_environment" />}
        subHeadingID="config.python.current_environment_description"
      >
        <div>
          {status.selectedRuntime ? (
            <>
              <Badge variant={status.managedEnvironment ? "success" : "warning"}>
                {status.managedEnvironment ? (
                  <FormattedMessage id="config.python.managed" />
                ) : (
                  <FormattedMessage id="config.python.unmanaged" />
                )}
              </Badge>
              <div>{status.selectedRuntime.version}</div>
            </>
          ) : (
            <FormattedMessage id="config.python.no_runtime" />
          )}
          <div className="python-mono">{status.configuredPythonPath}</div>
        </div>
      </Setting>

      {advancedMode && (
        <Setting
          heading={<FormattedMessage id="config.python.external_path" />}
          subHeadingID="config.python.external_path_description"
        >
          <div className="python-external-editor">
            <Form.Label htmlFor="python-external-path" className="sr-only">
              <FormattedMessage id="config.python.external_path" />
            </Form.Label>
            <Form.Control
              id="python-external-path"
              value={externalPath}
              onChange={(event) => setExternalPath(event.currentTarget.value)}
              disabled={busy}
            />
            <Button
              variant="secondary"
              disabled={busy || externalPath === status.configuredPythonPath}
              onClick={() => saveGeneral({ pythonPath: externalPath })}
            >
              <FormattedMessage id="actions.save" />
            </Button>
          </div>
        </Setting>
      )}
    </>
  );
};
