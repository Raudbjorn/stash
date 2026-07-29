import React, { useCallback, useEffect, useState } from "react";
import { Badge, Button, ProgressBar } from "react-bootstrap";
import { FormattedMessage } from "react-intl";
import { mutateStopJob } from "src/core/StashService";
import { JobStatus } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";
import { JobFragment, useMonitorJob } from "src/utils/job";
import { ActivePythonJob, PythonJobImpact } from "./types";

interface PythonJobProgressProps {
  active?: ActivePythonJob;
  onFinished: (job: JobFragment | undefined, impact: PythonJobImpact) => void;
}

export const PythonJobProgress: React.FC<PythonJobProgressProps> = ({
  active,
  onFinished,
}) => {
  const Toast = useToast();
  const [terminal, setTerminal] = useState<JobFragment>();
  const [stopping, setStopping] = useState(false);

  useEffect(() => {
    if (active) {
      setTerminal(undefined);
      setStopping(false);
    }
  }, [active]);

  const handleFinished = useCallback(
    (job?: JobFragment) => {
      setTerminal(job);
      if (active) onFinished(job, active.impact);
    },
    [active, onFinished]
  );
  const { job } = useMonitorJob(active?.id, handleFinished);
  const displayed = job ?? terminal;

  if (!displayed) return null;

  const canStop =
    !!active &&
    !stopping &&
    (displayed.status === JobStatus.Ready ||
      displayed.status === JobStatus.Running);

  async function stop() {
    if (!active) return;
    try {
      setStopping(true);
      await mutateStopJob(active.id);
    } catch (error) {
      setStopping(false);
      Toast.error(error);
    }
  }

  return (
    <div className="python-job" aria-live="polite">
      <div className="python-job-heading">
        <strong>{displayed.description}</strong>
        {displayed.status === JobStatus.Finished && (
          <Badge variant="success">
            <FormattedMessage id="config.python.job_complete" />
          </Badge>
        )}
        {displayed.status === JobStatus.Cancelled && (
          <Badge variant="secondary">
            <FormattedMessage id="config.python.job_cancelled" />
          </Badge>
        )}
        {displayed.status === JobStatus.Failed && (
          <Badge variant="danger">
            <FormattedMessage id="config.python.job_failed" />
          </Badge>
        )}
        {canStop && (
          <Button size="sm" variant="outline-danger" onClick={stop}>
            <FormattedMessage id="config.python.stop_job" />
          </Button>
        )}
      </div>
      {displayed.status === JobStatus.Running &&
        (displayed.progress == null || displayed.progress < 0 ? (
          <div
            className="python-progress-indeterminate"
            role="progressbar"
            aria-label="Python job progress"
          >
            <span />
          </div>
        ) : (
          <ProgressBar
            animated
            now={displayed.progress * 100}
            label={`${(displayed.progress * 100).toFixed(0)}%`}
          />
        ))}
      {(displayed.subTasks ?? []).map((detail) => (
        <div className="python-job-detail" key={detail}>
          {detail}
        </div>
      ))}
      {displayed.error && <div className="text-danger">{displayed.error}</div>}
    </div>
  );
};
