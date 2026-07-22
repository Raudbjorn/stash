import React from "react";
import { useHistory } from "react-router-dom";
import { Button } from "react-bootstrap";
import { Helmet } from "react-helmet";
import { FormattedMessage, useIntl } from "react-intl";
import { Icon } from "../Shared/Icon";
import { faPlay, faArrowLeft } from "@fortawesome/free-solid-svg-icons";
import { PersistentQueueViewer } from "../Scenes/SceneDetails/PersistentQueueViewer";
import { usePersistentQueue } from "src/hooks/usePersistentQueue";

const QueuePage: React.FC = () => {
  const intl = useIntl();
  const history = useHistory();
  const { queue } = usePersistentQueue();

  const handlePlayQueue = () => {
    if (queue.length > 0) {
      history.push(`/scenes/${queue[0].id}?queueType=persistent&continue=true`);
    }
  };

  const handleSceneClicked = (sceneId: string) => {
    history.push(`/scenes/${sceneId}?queueType=persistent&continue=true`);
  };

  const handleBack = () => {
    history.goBack();
  };

  return (
    <>
      <Helmet>
        <title>{intl.formatMessage({ id: "persistent_queue.title" })}</title>
      </Helmet>
      <div className="queue-page">
        <div className="queue-page-header">
          <Button variant="secondary" onClick={handleBack}>
            <Icon icon={faArrowLeft} />
            <span className="ml-2">
              <FormattedMessage id="actions.back" />
            </span>
          </Button>
          {queue.length > 0 && (
            <Button variant="primary" onClick={handlePlayQueue}>
              <Icon icon={faPlay} />
              <span className="ml-2">
                <FormattedMessage id="actions.play_queue" />
              </span>
            </Button>
          )}
        </div>
        <PersistentQueueViewer
          onSceneClicked={handleSceneClicked}
          embedded={false}
        />
      </div>
    </>
  );
};

export default QueuePage;
