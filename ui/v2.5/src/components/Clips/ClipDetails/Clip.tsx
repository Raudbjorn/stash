import React, { useEffect, useRef, useState } from "react";
import { Button, Form } from "react-bootstrap";
import { Link, useHistory, useParams } from "react-router-dom";
import * as GQL from "src/core/generated-graphql";
import {
  useFindClip,
  useClipUpdate,
  useClipDestroy,
  useClipGenerate,
  useClipAddO,
  useClipResetO,
  useClipAddPlay,
} from "src/core/StashService";
import TextUtils from "src/utils/text";
import { useToast } from "src/hooks/Toast";
import { LoadingIndicator } from "src/components/Shared/LoadingIndicator";
import { RatingSystem } from "src/components/Shared/Rating/RatingSystem";

const Clip: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const history = useHistory();
  const Toast = useToast();
  const videoRef = useRef<HTMLVideoElement>(null);

  const { data, loading, error } = useFindClip(id);
  const clip = data?.findClip ?? undefined;

  const [updateClip] = useClipUpdate();
  const [destroyClip] = useClipDestroy();
  const [generateClip] = useClipGenerate();
  const [addO] = useClipAddO();
  const [resetO] = useClipResetO();
  const [addPlay] = useClipAddPlay();

  // inline edit form state
  const [editTitle, setEditTitle] = useState("");
  const [editSeconds, setEditSeconds] = useState("");
  const [editEndSeconds, setEditEndSeconds] = useState("");

  useEffect(() => {
    if (clip) {
      setEditTitle(clip.title ?? "");
      setEditSeconds(String(clip.seconds));
      setEditEndSeconds(clip.end_seconds != null ? String(clip.end_seconds) : "");
    }
  }, [clip]);

  if (loading) {
    return <LoadingIndicator />;
  }

  if (error) {
    return <div className="text-danger p-3">{error.message}</div>;
  }

  if (!clip) {
    return <div className="p-3">Clip not found.</div>;
  }

  async function runUpdate(input: GQL.ClipUpdateInput, successMsg: string) {
    try {
      await updateClip({ variables: { input } });
      Toast.success(successMsg);
    } catch (e) {
      Toast.error(e);
    }
  }

  async function onSetRating(value: number | null) {
    await runUpdate({ id: clip!.id, rating100: value }, "Rating updated");
  }

  async function onSaveEdit() {
    const input: GQL.ClipUpdateInput = {
      id: clip!.id,
      title: editTitle.trim() !== "" ? editTitle.trim() : null,
      seconds:
        editSeconds !== "" && !Number.isNaN(Number(editSeconds))
          ? Number(editSeconds)
          : undefined,
      end_seconds:
        editEndSeconds !== "" && !Number.isNaN(Number(editEndSeconds))
          ? Number(editEndSeconds)
          : null,
    };
    await runUpdate(input, "Clip updated");
  }

  async function onAddO() {
    try {
      await addO({ variables: { id: clip!.id } });
    } catch (e) {
      Toast.error(e);
    }
  }

  async function onResetO() {
    try {
      await resetO({ variables: { id: clip!.id } });
    } catch (e) {
      Toast.error(e);
    }
  }

  async function onGenerate() {
    try {
      await generateClip({ variables: { id: clip!.id } });
      Toast.success("Generation queued");
    } catch (e) {
      Toast.error(e);
    }
  }

  async function onPlay() {
    try {
      await addPlay({ variables: { id: clip!.id } });
    } catch (e) {
      Toast.error(e);
    }
    videoRef.current?.play();
  }

  async function onDelete() {
    try {
      await destroyClip({ variables: { id: clip!.id } });
      Toast.success("Clip deleted");
      history.push("/clips");
    } catch (e) {
      Toast.error(e);
    }
  }

  const rangeLabel =
    clip.end_seconds != null
      ? `${TextUtils.secondsToTimestamp(
          clip.seconds
        )} – ${TextUtils.secondsToTimestamp(clip.end_seconds)}`
      : TextUtils.secondsToTimestamp(clip.seconds);

  return (
    <div className="clip-details">
      <video
        ref={videoRef}
        className="clip-details__video"
        controls
        src={clip.stream || undefined}
        poster={clip.scene?.paths?.screenshot ?? undefined}
      />

      <div className="clip-details__header">
        <h3 className="mb-0">{clip.title || "Untitled clip"}</h3>
        <RatingSystem value={clip.rating100} onSetRating={onSetRating} />
      </div>

      <dl className="clip-details__meta">
        <dt>Scene</dt>
        <dd>
          {clip.scene ? (
            <Link to={`/scenes/${clip.scene.id}`}>
              {clip.scene.title || `Scene ${clip.scene.id}`}
            </Link>
          ) : (
            "—"
          )}
        </dd>

        <dt>Range</dt>
        <dd>{rangeLabel}</dd>

        <dt>Tags</dt>
        <dd>
          {clip.tags.length > 0 ? (
            <div className="clip-details__tags">
              {clip.tags.map((tag) => (
                <Link
                  key={tag.id}
                  to={`/tags/${tag.id}`}
                  className="badge badge-secondary"
                >
                  {tag.name}
                </Link>
              ))}
            </div>
          ) : (
            "—"
          )}
        </dd>
      </dl>

      <div className="clip-details__actions">
        <Button variant="secondary" onClick={onPlay}>
          Play ({clip.play_count ?? 0})
        </Button>
        <Button variant="secondary" onClick={onAddO}>
          O-Counter +{" "}
          <span className="badge badge-pill badge-light">
            {clip.o_counter ?? 0}
          </span>
        </Button>
        <Button variant="secondary" onClick={onResetO}>
          Reset O
        </Button>
        <Button variant="secondary" onClick={onGenerate}>
          Generate
        </Button>
        <Button variant="danger" onClick={onDelete}>
          Delete
        </Button>
      </div>

      <div className="clip-edit-form">
        <h5>Edit</h5>
        <Form.Group className="mb-2">
          <Form.Label>Title</Form.Label>
          <Form.Control
            type="text"
            value={editTitle}
            onChange={(e) => setEditTitle(e.currentTarget.value)}
          />
        </Form.Group>
        <Form.Group className="mb-2">
          <Form.Label>Start (seconds)</Form.Label>
          <Form.Control
            type="number"
            step="0.001"
            value={editSeconds}
            onChange={(e) => setEditSeconds(e.currentTarget.value)}
          />
        </Form.Group>
        <Form.Group className="mb-2">
          <Form.Label>End (seconds)</Form.Label>
          <Form.Control
            type="number"
            step="0.001"
            value={editEndSeconds}
            onChange={(e) => setEditEndSeconds(e.currentTarget.value)}
          />
        </Form.Group>
        <Button variant="primary" onClick={onSaveEdit}>
          Save
        </Button>
      </div>
    </div>
  );
};

export default Clip;
