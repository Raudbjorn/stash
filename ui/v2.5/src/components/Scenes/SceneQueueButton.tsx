import React, { useMemo, useCallback } from "react";
import { Button, OverlayTrigger, Tooltip } from "react-bootstrap";
import { faPlus, faCheck, faListOl } from "@fortawesome/free-solid-svg-icons";
import cx from "classnames";
import { Icon } from "../Shared/Icon";
import { usePersistentQueue } from "src/hooks/usePersistentQueue";
import { useIntl } from "react-intl";

interface ISceneQueueButtonProps {
  sceneID: string;
  compact?: boolean;
}

export const SceneQueueButton: React.FC<ISceneQueueButtonProps> = ({
  sceneID,
  compact = false,
}) => {
  const intl = useIntl();
  const { addToQueue, removeFromQueue, isInQueue } = usePersistentQueue();

  const inQueue = useMemo(() => isInQueue(sceneID), [sceneID, isInQueue]);

  const handleToggleQueue = useCallback(
    async (e: React.MouseEvent) => {
      e.preventDefault();
      e.stopPropagation();
      if (inQueue) {
        await removeFromQueue(sceneID);
      } else {
        await addToQueue(sceneID);
      }
    },
    [inQueue, sceneID, addToQueue, removeFromQueue]
  );

  const tooltipText = inQueue
    ? intl.formatMessage({ id: "actions.remove_from_queue" })
    : intl.formatMessage({ id: "actions.add_to_queue" });

  const icon = inQueue ? faCheck : faPlus;

  if (compact) {
    return (
      <Button
        className={cx("minimal", "queue-button", { "in-queue": inQueue })}
        onClick={handleToggleQueue}
        variant={inQueue ? "success" : "secondary"}
        size="sm"
      >
        <Icon icon={icon} />
      </Button>
    );
  }

  return (
    <OverlayTrigger
      overlay={<Tooltip id="queue-tooltip">{tooltipText}</Tooltip>}
      placement="top"
    >
      <Button
        className={cx("minimal", "queue-button", { "in-queue": inQueue })}
        onClick={handleToggleQueue}
        variant={inQueue ? "success" : "secondary"}
      >
        <Icon icon={faListOl} />
        <Icon icon={icon} className="ml-1" />
      </Button>
    </OverlayTrigger>
  );
};

export default SceneQueueButton;
