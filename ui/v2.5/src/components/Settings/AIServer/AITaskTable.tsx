import React from "react";
import { Button } from "react-bootstrap";
import { FormattedMessage } from "react-intl";
import { mutateCancelAITask } from "src/core/StashService";
import { AiTaskDataFragment, AiTaskStatus } from "src/core/generated-graphql";
import { useToast } from "src/hooks/Toast";

interface AITaskTableProps {
  tasks: AiTaskDataFragment[];
  history: AiTaskDataFragment[];
}

function isActive(status: AiTaskStatus) {
  return (
    status === AiTaskStatus.Queued ||
    status === AiTaskStatus.Running ||
    status === AiTaskStatus.Streaming
  );
}

const TaskRow: React.FC<{ task: AiTaskDataFragment }> = ({ task }) => {
  const Toast = useToast();

  async function cancel() {
    try {
      await mutateCancelAITask(task.id);
    } catch (error) {
      Toast.error(error);
    }
  }

  return (
    <tr>
      <td className="job-description">{task.actionID}</td>
      <td className="job-status">{task.status}</td>
      <td>{task.service}</td>
      <td>
        {isActive(task.status) && !task.cancelRequested && (
          <Button size="sm" variant="danger" onClick={cancel}>
            <FormattedMessage id="actions.stop" />
          </Button>
        )}
        {task.error && <span className="job-error">{task.error}</span>}
      </td>
    </tr>
  );
};

export const AITaskTable: React.FC<AITaskTableProps> = ({
  tasks,
  history,
}) => {
  const rows = [...tasks, ...history];

  if (rows.length === 0) {
    return (
      <div className="empty-queue-message">
        <FormattedMessage id="config.ai_server.no_tasks" />
      </div>
    );
  }

  return (
    <table className="job-table">
      <thead>
        <tr>
          <th>
            <FormattedMessage id="config.ai_server.task_action" />
          </th>
          <th>
            <FormattedMessage id="config.ai_server.task_status" />
          </th>
          <th>
            <FormattedMessage id="config.ai_server.task_service" />
          </th>
          <th />
        </tr>
      </thead>
      <tbody>
        {rows.map((task) => (
          <TaskRow key={task.id} task={task} />
        ))}
      </tbody>
    </table>
  );
};
