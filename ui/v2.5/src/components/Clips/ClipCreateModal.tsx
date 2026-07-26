import React, { useState } from "react";
import { Modal, Button, Form } from "react-bootstrap";
import { useHistory } from "react-router-dom";
import * as GQL from "src/core/generated-graphql";
import { useClipCreate } from "src/core/StashService";
import { useToast } from "src/hooks/Toast";
import { Scene, SceneSelect } from "../Scenes/SceneSelect";

interface IClipCreateModalProps {
  onClose: (created?: boolean) => void;
}

export const ClipCreateModal: React.FC<IClipCreateModalProps> = ({
  onClose,
}) => {
  const history = useHistory();
  const Toast = useToast();
  const [createClip, { loading }] = useClipCreate();

  const [scenes, setScenes] = useState<Scene[]>([]);
  const [title, setTitle] = useState("");
  const [seconds, setSeconds] = useState<string>("");
  const [endSeconds, setEndSeconds] = useState<string>("");
  const [validationError, setValidationError] = useState<string>();

  const sceneId = scenes.length > 0 ? scenes[0].id : undefined;

  async function onSubmit() {
    if (!sceneId) {
      setValidationError("A scene is required.");
      return;
    }
    if (seconds === "" || Number.isNaN(Number(seconds))) {
      setValidationError("A valid start time (seconds) is required.");
      return;
    }

    const input: GQL.ClipCreateInput = {
      scene_id: sceneId,
      title: title.trim() !== "" ? title.trim() : undefined,
      seconds: Number(seconds),
      end_seconds:
        endSeconds !== "" && !Number.isNaN(Number(endSeconds))
          ? Number(endSeconds)
          : undefined,
    };

    try {
      const result = await createClip({ variables: { input } });
      const newId = result.data?.clipCreate?.id;
      Toast.success("Clip created");
      onClose(true);
      if (newId) {
        history.push(`/clips/${newId}`);
      }
    } catch (e) {
      Toast.error(e);
    }
  }

  return (
    <Modal show onHide={() => onClose(false)}>
      <Modal.Header closeButton>
        <Modal.Title>Create Clip</Modal.Title>
      </Modal.Header>
      <Modal.Body>
        {validationError && (
          <div className="text-danger mb-2">{validationError}</div>
        )}
        <Form.Group className="mb-3">
          <Form.Label>Scene</Form.Label>
          <SceneSelect
            values={scenes}
            onSelect={(items) => setScenes(items)}
          />
        </Form.Group>

        <Form.Group className="mb-3">
          <Form.Label>Title</Form.Label>
          <Form.Control
            type="text"
            value={title}
            placeholder="Optional title"
            onChange={(e) => setTitle(e.currentTarget.value)}
          />
        </Form.Group>

        <Form.Group className="mb-3">
          <Form.Label>Start (seconds)</Form.Label>
          <Form.Control
            type="number"
            step="0.001"
            value={seconds}
            onChange={(e) => setSeconds(e.currentTarget.value)}
          />
        </Form.Group>

        <Form.Group className="mb-3">
          <Form.Label>End (seconds)</Form.Label>
          <Form.Control
            type="number"
            step="0.001"
            value={endSeconds}
            placeholder="Optional"
            onChange={(e) => setEndSeconds(e.currentTarget.value)}
          />
        </Form.Group>
      </Modal.Body>
      <Modal.Footer>
        <Button variant="secondary" onClick={() => onClose(false)}>
          Cancel
        </Button>
        <Button variant="primary" disabled={loading} onClick={onSubmit}>
          Create
        </Button>
      </Modal.Footer>
    </Modal>
  );
};

export default ClipCreateModal;
