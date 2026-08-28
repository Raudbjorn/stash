import { faMapMarkerAlt, faTag } from "@fortawesome/free-solid-svg-icons";
import React, { useState } from "react";
import { Modal } from "react-bootstrap";
import { FormattedMessage } from "react-intl";
import { Link } from "react-router-dom";
import * as GQL from "src/core/generated-graphql";
import TextUtils from "src/utils/text";
import { Icon } from "../Shared/Icon";

type DialogProps = {
  show: boolean;
  scene: GQL.SlimSceneDataFragment;
  onHide: () => void;
};

type SceneCardDialogProps = Pick<DialogProps, "show" | "onHide"> & {
  titleId: "tags" | "markers";
};

const SceneCardDialog: React.FC<SceneCardDialogProps> = ({
  show,
  onHide,
  titleId,
  children,
}) => (
  <Modal
    show={show}
    onHide={onHide}
    centered
    scrollable
    size="lg"
    className="scene-card-dialog"
  >
    <Modal.Header closeButton>
      <Modal.Title>
        <FormattedMessage id={titleId} />
      </Modal.Title>
    </Modal.Header>
    <Modal.Body>
      <div className="scene-card-dialog-grid">{children}</div>
    </Modal.Body>
  </Modal>
);

type Tag = GQL.SlimSceneDataFragment["tags"][number];

const TagThumbnail: React.FC<{ tag: Tag }> = ({ tag }) => {
  const [failed, setFailed] = useState(false);

  if (!tag.image_path || failed) {
    return (
      <span className="scene-card-dialog-placeholder">
        <Icon icon={faTag} />
      </span>
    );
  }

  return (
    <img
      className="scene-card-dialog-thumbnail"
      src={tag.image_path}
      alt=""
      loading="lazy"
      onError={() => setFailed(true)}
    />
  );
};

export const SceneCardTagDialog: React.FC<DialogProps> = ({
  show,
  scene,
  onHide,
}) => (
  <SceneCardDialog show={show} onHide={onHide} titleId="tags">
    {scene.tags.map((tag) => (
      <Link
        key={tag.id}
        to={`/tags/${tag.id}`}
        className="scene-card-dialog-tile"
      >
        <TagThumbnail key={tag.image_path ?? ""} tag={tag} />
        <span className="scene-card-dialog-title">{tag.name}</span>
      </Link>
    ))}
  </SceneCardDialog>
);

type Marker = GQL.SlimSceneDataFragment["scene_markers"][number];

const MarkerThumbnail: React.FC<{
  marker: Marker;
  sceneScreenshot?: string | null;
}> = ({ marker, sceneScreenshot }) => {
  const primarySource = marker.screenshot || null;
  const fallbackSource = sceneScreenshot || null;
  const [failedPrimary, setFailedPrimary] = useState(false);
  const [failedFallback, setFailedFallback] = useState(false);

  const source =
    (!failedPrimary && primarySource) ||
    (!failedFallback && fallbackSource !== primarySource && fallbackSource) ||
    null;

  if (!source) {
    return (
      <span className="scene-card-dialog-placeholder">
        <Icon icon={faMapMarkerAlt} />
      </span>
    );
  }

  return (
    <img
      className="scene-card-dialog-thumbnail"
      src={source}
      alt=""
      loading="lazy"
      onError={() => {
        if (source === primarySource) setFailedPrimary(true);
        else setFailedFallback(true);
      }}
    />
  );
};

export const SceneCardMarkerDialog: React.FC<DialogProps> = ({
  show,
  scene,
  onHide,
}) => (
  <SceneCardDialog show={show} onHide={onHide} titleId="markers">
    {scene.scene_markers.map((marker) => (
      <Link
        key={marker.id}
        to={`/scenes/${scene.id}?t=${marker.seconds}`}
        className="scene-card-dialog-tile"
      >
        <MarkerThumbnail
          key={`${marker.screenshot ?? ""}:${scene.paths.screenshot ?? ""}`}
          marker={marker}
          sceneScreenshot={scene.paths.screenshot}
        />
        <span className="scene-card-dialog-title">
          {marker.title || marker.primary_tag.name}
        </span>
        <span className="scene-card-dialog-timestamp">
          {TextUtils.secondsToTimestamp(marker.seconds)}
        </span>
      </Link>
    ))}
  </SceneCardDialog>
);
