import React, { useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { useParams, useHistory, Link } from "react-router-dom";
import { Button, Form, Modal } from "react-bootstrap";
import {
  faPlay,
  faTrash,
  faPencilAlt,
  faArrowLeft,
  faStar,
} from "@fortawesome/free-solid-svg-icons";
import { faStar as farStar } from "@fortawesome/free-regular-svg-icons";
import { Icon } from "../../Shared/Icon";
import { LoadingIndicator } from "../../Shared/LoadingIndicator";
import {
  usePlaylistEntries,
  usePlaylistMutations,
  usePlaylists,
} from "src/hooks/usePlaylist";
import { PlaylistScenesPanel } from "./PlaylistScenesPanel";

export const Playlist: React.FC = () => {
  const { id } = useParams<{ id: string }>();
  const history = useHistory();
  const intl = useIntl();

  const {
    playlists,
    loading: playlistsLoading,
    refetch: refetchPlaylists,
  } = usePlaylists();
  const {
    entries,
    loading: entriesLoading,
    refetch: refetchEntries,
  } = usePlaylistEntries(id);
  const { updatePlaylist, deletePlaylist, clearPlaylist, setDefaultPlaylist } =
    usePlaylistMutations();

  const playlist = playlists.find((p) => p.id === id);

  const [isEditing, setIsEditing] = useState(false);
  const [editName, setEditName] = useState("");
  const [editDescription, setEditDescription] = useState("");
  const [showDeleteModal, setShowDeleteModal] = useState(false);
  const [showClearModal, setShowClearModal] = useState(false);

  const handleEdit = () => {
    if (playlist) {
      setEditName(playlist.name);
      setEditDescription(playlist.description || "");
      setIsEditing(true);
    }
  };

  const handleSaveEdit = async () => {
    try {
      await updatePlaylist(id, editName, editDescription || undefined);
      setIsEditing(false);
      refetchPlaylists();
    } catch (e) {
      // Error handled in hook
    }
  };

  const handleDelete = async () => {
    try {
      await deletePlaylist(id);
      history.push("/playlists");
    } catch (e) {
      // Error handled in hook
    }
  };

  const handleClear = async () => {
    try {
      await clearPlaylist(id);
      setShowClearModal(false);
      refetchEntries();
    } catch (e) {
      // Error handled in hook
    }
  };

  const handleSetDefault = async () => {
    try {
      await setDefaultPlaylist(id);
      refetchPlaylists();
    } catch (e) {
      // Error handled in hook
    }
  };

  const handlePlayFirst = () => {
    if (entries.length > 0) {
      const firstSceneId = entries[0].scene.id;
      history.push(`/scenes/${firstSceneId}?playlist=${id}`);
    }
  };

  if (playlistsLoading || entriesLoading) {
    return <LoadingIndicator />;
  }

  if (!playlist) {
    return (
      <div className="container">
        <h2>
          <FormattedMessage id="playlists_page.not_found" />
        </h2>
        <Link to="/playlists">
          <FormattedMessage id="actions.back_to_playlists" />
        </Link>
      </div>
    );
  }

  return (
    <div className="container">
      <div className="d-flex justify-content-between align-items-start mb-4">
        <div>
          <Button
            variant="link"
            className="p-0 mb-2"
            onClick={() => history.push("/playlists")}
          >
            <Icon icon={faArrowLeft} className="mr-2" />
            <FormattedMessage id="actions.back_to_playlists" />
          </Button>

          {isEditing ? (
            <div>
              <Form.Group>
                <Form.Control
                  type="text"
                  value={editName}
                  onChange={(e) => setEditName(e.target.value)}
                  className="mb-2"
                />
                <Form.Control
                  as="textarea"
                  rows={2}
                  value={editDescription}
                  onChange={(e) => setEditDescription(e.target.value)}
                  placeholder={intl.formatMessage({
                    id: "playlists_page.description_placeholder",
                  })}
                />
              </Form.Group>
              <div className="mt-2">
                <Button
                  variant="primary"
                  size="sm"
                  onClick={handleSaveEdit}
                  disabled={!editName.trim()}
                  className="mr-2"
                >
                  <FormattedMessage id="actions.save" />
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => setIsEditing(false)}
                >
                  <FormattedMessage id="actions.cancel" />
                </Button>
              </div>
            </div>
          ) : (
            <div>
              <h2>
                {playlist.name}
                {playlist.is_default && (
                  <span className="ml-2 badge badge-info">
                    <FormattedMessage id="playlists_page.default" />
                  </span>
                )}
              </h2>
              {playlist.description && (
                <p className="text-muted">{playlist.description}</p>
              )}
              <p className="text-muted">
                {intl.formatMessage(
                  { id: "countables.scenes" },
                  { count: entries.length }
                )}
              </p>
            </div>
          )}
        </div>

        {!isEditing && (
          <div>
            <Button
              variant="primary"
              onClick={handlePlayFirst}
              disabled={entries.length === 0}
              className="mr-2"
            >
              <Icon icon={faPlay} className="mr-2" />
              <FormattedMessage id="actions.play" />
            </Button>
            <Button variant="secondary" onClick={handleEdit} className="mr-2">
              <Icon icon={faPencilAlt} className="mr-2" />
              <FormattedMessage id="actions.edit" />
            </Button>
            <Button
              variant={playlist.is_default ? "warning" : "secondary"}
              onClick={handleSetDefault}
              className="mr-2"
              title={intl.formatMessage({
                id: playlist.is_default
                  ? "actions.unset_default_playlist"
                  : "actions.set_as_default_playlist",
              })}
            >
              <Icon
                icon={playlist.is_default ? faStar : farStar}
                className="mr-2"
              />
              {playlist.is_default ? (
                <FormattedMessage id="playlists_page.default" />
              ) : (
                <FormattedMessage id="actions.set_as_default_playlist" />
              )}
            </Button>
            {entries.length > 0 && (
              <Button
                variant="warning"
                onClick={() => setShowClearModal(true)}
                className="mr-2"
              >
                <Icon icon={faTrash} className="mr-2" />
                <FormattedMessage id="actions.clear" />
              </Button>
            )}
            {!playlist.is_default && (
              <Button variant="danger" onClick={() => setShowDeleteModal(true)}>
                <Icon icon={faTrash} className="mr-2" />
                <FormattedMessage id="actions.delete" />
              </Button>
            )}
          </div>
        )}
      </div>

      <PlaylistScenesPanel playlistId={id} entries={entries} />

      <Modal show={showDeleteModal} onHide={() => setShowDeleteModal(false)}>
        <Modal.Header closeButton>
          <Modal.Title>
            <FormattedMessage id="actions.delete_playlist" />
          </Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <FormattedMessage id="playlists_page.delete_confirm" />
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={() => setShowDeleteModal(false)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <Button variant="danger" onClick={handleDelete}>
            <FormattedMessage id="actions.delete" />
          </Button>
        </Modal.Footer>
      </Modal>

      <Modal show={showClearModal} onHide={() => setShowClearModal(false)}>
        <Modal.Header closeButton>
          <Modal.Title>
            <FormattedMessage id="actions.clear_playlist" />
          </Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <FormattedMessage id="playlists_page.clear_confirm" />
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={() => setShowClearModal(false)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <Button variant="warning" onClick={handleClear}>
            <FormattedMessage id="actions.clear" />
          </Button>
        </Modal.Footer>
      </Modal>
    </div>
  );
};

export default Playlist;
