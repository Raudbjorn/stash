import React, { useState, useCallback, DragEvent } from "react";
import { Link } from "react-router-dom";
import cx from "classnames";
import { Button } from "react-bootstrap";
import { Icon } from "src/components/Shared/Icon";
import { useIntl } from "react-intl";
import {
  faGripVertical,
  faTimes,
  faTrash,
} from "@fortawesome/free-solid-svg-icons";
import { objectTitle } from "src/core/files";
import * as GQL from "src/core/generated-graphql";
import { usePersistentQueue } from "src/hooks/usePersistentQueue";

export interface IPersistentQueueViewerProps {
  currentID?: string;
  onSceneClicked?: (id: string) => void;
  embedded?: boolean;
}

export const PersistentQueueViewer: React.FC<IPersistentQueueViewerProps> = ({
  currentID,
  onSceneClicked,
  embedded = false,
}) => {
  const intl = useIntl();
  const { queue, removeFromQueue, reorderQueue, clearQueue } =
    usePersistentQueue();
  const [draggedIndex, setDraggedIndex] = useState<number | null>(null);
  const [dragOverIndex, setDragOverIndex] = useState<number | null>(null);

  const handleDragStart = useCallback(
    (e: DragEvent<HTMLLIElement>, index: number) => {
      setDraggedIndex(index);
      e.dataTransfer.effectAllowed = "move";
      e.dataTransfer.setData("text/html", e.currentTarget.innerHTML);
    },
    []
  );

  const handleDragOver = useCallback(
    (e: DragEvent<HTMLLIElement>, index: number) => {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
      setDragOverIndex(index);
    },
    []
  );

  const handleDragLeave = useCallback(() => {
    setDragOverIndex(null);
  }, []);

  const handleDrop = useCallback(
    async (e: DragEvent<HTMLLIElement>, dropIndex: number) => {
      e.preventDefault();
      if (draggedIndex !== null && draggedIndex !== dropIndex) {
        await reorderQueue(draggedIndex, dropIndex);
      }
      setDraggedIndex(null);
      setDragOverIndex(null);
    },
    [draggedIndex, reorderQueue]
  );

  const handleDragEnd = useCallback(() => {
    setDraggedIndex(null);
    setDragOverIndex(null);
  }, []);

  const handleSceneClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>, id: string) => {
      if (onSceneClicked) {
        onSceneClicked(id);
        event.preventDefault();
      }
    },
    [onSceneClicked]
  );

  const handleRemove = useCallback(
    async (sceneId: string) => {
      await removeFromQueue(sceneId);
    },
    [removeFromQueue]
  );

  const renderQueueEntry = (
    scene: GQL.SlimSceneDataFragment,
    index: number
  ) => {
    const isCurrentScene = scene.id === currentID;
    const isDragging = draggedIndex === index;
    const isDragOver = dragOverIndex === index;

    return (
      <li
        key={scene.id}
        className={cx("queue-item", {
          current: isCurrentScene,
          dragging: isDragging,
          "drag-over": isDragOver,
        })}
        draggable
        onDragStart={(e) => handleDragStart(e, index)}
        onDragOver={(e) => handleDragOver(e, index)}
        onDragLeave={handleDragLeave}
        onDrop={(e) => handleDrop(e, index)}
        onDragEnd={handleDragEnd}
      >
        <div className="queue-item-content">
          <span className="drag-handle">
            <Icon icon={faGripVertical} />
          </span>
          <span className="queue-index">{index + 1}.</span>
          <Link
            to={`/scenes/${scene.id}`}
            onClick={(e) => handleSceneClick(e, scene.id)}
            className="queue-item-link"
          >
            <div className="thumbnail-container">
              <img
                loading="lazy"
                alt={scene.title ?? ""}
                src={scene.paths.screenshot ?? ""}
              />
            </div>
            <div className="queue-scene-details">
              <span className="queue-scene-title">{objectTitle(scene)}</span>
              <span className="queue-scene-studio">{scene?.studio?.name}</span>
              <span className="queue-scene-performers">
                {scene?.performers?.map((performer) => performer.name).join(", ")}
              </span>
              <span className="queue-scene-date">{scene?.date}</span>
            </div>
          </Link>
          <Button
            className="minimal remove-button"
            variant="danger"
            size="sm"
            onClick={() => handleRemove(scene.id)}
            title={intl.formatMessage({ id: "actions.remove" })}
          >
            <Icon icon={faTimes} />
          </Button>
        </div>
      </li>
    );
  };

  if (!embedded && queue.length === 0) {
    return (
      <div className="empty-queue">
        <p>{intl.formatMessage({ id: "persistent_queue.empty" })}</p>
      </div>
    );
  }

  return (
    <div className="persistent-queue-viewer">
      {!embedded && (
        <div className="queue-header">
          <h3>{intl.formatMessage({ id: "persistent_queue.title" })}</h3>
          {queue.length > 0 && (
            <div className="queue-actions">
              <Button
                variant="danger"
                size="sm"
                onClick={clearQueue}
                title={intl.formatMessage({ id: "actions.clear_queue" })}
              >
                <Icon icon={faTrash} />
                <span className="ml-2">
                  {intl.formatMessage({ id: "actions.clear" })}
                </span>
              </Button>
            </div>
          )}
        </div>
      )}
      <div className="queue-content">
        {queue.length > 0 ? (
          <>
            <div className="queue-info">
              <span>
                {intl.formatMessage(
                  { id: "persistent_queue.scene_count" },
                  { count: queue.length }
                )}
              </span>
            </div>
            <ol className="queue-list">
              {queue.map((scene, index) => renderQueueEntry(scene, index))}
            </ol>
          </>
        ) : (
          embedded && (
            <div className="empty-queue-inline">
              <p>{intl.formatMessage({ id: "persistent_queue.no_scenes" })}</p>
            </div>
          )
        )}
      </div>
    </div>
  );
};

export default PersistentQueueViewer;
