import React, { useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Link, useHistory } from "react-router-dom";
import { Button, Card, Col, Form, Modal, Row } from "react-bootstrap";
import {
  faPlus,
  faTrash,
  faPlay,
  faStar,
} from "@fortawesome/free-solid-svg-icons";
import { faStar as farStar } from "@fortawesome/free-regular-svg-icons";
import { Icon } from "../Shared/Icon";
import { LoadingIndicator } from "../Shared/LoadingIndicator";
import { usePlaylists, usePlaylistMutations } from "src/hooks/usePlaylist";

export const PlaylistList: React.FC = () => {
  const intl = useIntl();
  const history = useHistory();
  const { playlists, loading, refetch } = usePlaylists();
  const { createPlaylist, deletePlaylist, setDefaultPlaylist } =
    usePlaylistMutations();

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [newPlaylistName, setNewPlaylistName] = useState("");
  const [newPlaylistDescription, setNewPlaylistDescription] = useState("");
  const [deleteId, setDeleteId] = useState<string | null>(null);

  const handleCreate = async () => {
    if (!newPlaylistName.trim()) return;
    try {
      const playlist = await createPlaylist(
        newPlaylistName.trim(),
        newPlaylistDescription.trim() || undefined
      );
      setShowCreateModal(false);
      setNewPlaylistName("");
      setNewPlaylistDescription("");
      refetch();
      if (playlist?.id) {
        history.push(`/playlists/${playlist.id}`);
      }
    } catch (e) {
      // Error handled in hook
    }
  };

  const handleDelete = async () => {
    if (!deleteId) return;
    try {
      await deletePlaylist(deleteId);
      setDeleteId(null);
      refetch();
    } catch (e) {
      // Error handled in hook
    }
  };

  const handleSetDefault = async (playlistId: string) => {
    try {
      await setDefaultPlaylist(playlistId);
      refetch();
    } catch (e) {
      // Error handled in hook
    }
  };

  if (loading) {
    return <LoadingIndicator />;
  }

  return (
    <div className="container">
      <div className="d-flex justify-content-between align-items-center mb-4">
        <h2>
          <FormattedMessage id="playlists" />
        </h2>
        <Button variant="primary" onClick={() => setShowCreateModal(true)}>
          <Icon icon={faPlus} className="mr-2" />
          <FormattedMessage id="actions.create_playlist" />
        </Button>
      </div>

      {playlists.length === 0 ? (
        <div className="text-center text-muted py-5">
          <p>
            <FormattedMessage id="playlists_page.no_playlists" />
          </p>
          <Button variant="primary" onClick={() => setShowCreateModal(true)}>
            <Icon icon={faPlus} className="mr-2" />
            <FormattedMessage id="actions.create_playlist" />
          </Button>
        </div>
      ) : (
        <Row>
          {playlists.map((playlist) => (
            <Col key={playlist.id} xs={12} sm={6} md={4} lg={3} className="mb-4">
              <Card className="playlist-card h-100">
                <Link to={`/playlists/${playlist.id}`}>
                  {playlist.front_image_path ? (
                    <Card.Img
                      variant="top"
                      src={playlist.front_image_path}
                      alt={playlist.name}
                      style={{ height: "150px", objectFit: "cover" }}
                    />
                  ) : (
                    <div
                      className="d-flex align-items-center justify-content-center bg-secondary"
                      style={{ height: "150px" }}
                    >
                      <Icon icon={faPlay} size="3x" className="text-muted" />
                    </div>
                  )}
                </Link>
                <Card.Body>
                  <Card.Title>
                    <Link to={`/playlists/${playlist.id}`}>
                      {playlist.name}
                      {playlist.is_default && (
                        <span className="ml-2 badge badge-info">
                          <FormattedMessage id="playlists_page.default" />
                        </span>
                      )}
                    </Link>
                  </Card.Title>
                  <Card.Text className="text-muted">
                    {intl.formatMessage(
                      { id: "countables.scenes" },
                      { count: playlist.scene_count }
                    )}
                  </Card.Text>
                  {playlist.description && (
                    <Card.Text className="small text-muted">
                      {playlist.description}
                    </Card.Text>
                  )}
                </Card.Body>
                <Card.Footer className="d-flex justify-content-between">
                  <Link
                    to={`/playlists/${playlist.id}`}
                    className="btn btn-sm btn-primary"
                  >
                    <Icon icon={faPlay} className="mr-1" />
                    <FormattedMessage id="actions.play" />
                  </Link>
                  <div>
                    <Button
                      variant={playlist.is_default ? "warning" : "secondary"}
                      size="sm"
                      className="mr-1"
                      onClick={() => handleSetDefault(playlist.id)}
                      title={intl.formatMessage({
                        id: playlist.is_default
                          ? "actions.unset_default_playlist"
                          : "actions.set_as_default_playlist",
                      })}
                    >
                      <Icon icon={playlist.is_default ? faStar : farStar} />
                    </Button>
                    {!playlist.is_default && (
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => setDeleteId(playlist.id)}
                      >
                        <Icon icon={faTrash} />
                      </Button>
                    )}
                  </div>
                </Card.Footer>
              </Card>
            </Col>
          ))}
        </Row>
      )}

      <Modal show={showCreateModal} onHide={() => setShowCreateModal(false)}>
        <Modal.Header closeButton>
          <Modal.Title>
            <FormattedMessage id="actions.create_playlist" />
          </Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <Form.Group>
            <Form.Label>
              <FormattedMessage id="name" />
            </Form.Label>
            <Form.Control
              type="text"
              value={newPlaylistName}
              onChange={(e) => setNewPlaylistName(e.target.value)}
              placeholder={intl.formatMessage({ id: "playlists_page.name_placeholder" })}
              autoFocus
            />
          </Form.Group>
          <Form.Group>
            <Form.Label>
              <FormattedMessage id="description" />
            </Form.Label>
            <Form.Control
              as="textarea"
              rows={3}
              value={newPlaylistDescription}
              onChange={(e) => setNewPlaylistDescription(e.target.value)}
              placeholder={intl.formatMessage({
                id: "playlists_page.description_placeholder",
              })}
            />
          </Form.Group>
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={() => setShowCreateModal(false)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <Button
            variant="primary"
            onClick={handleCreate}
            disabled={!newPlaylistName.trim()}
          >
            <FormattedMessage id="actions.create" />
          </Button>
        </Modal.Footer>
      </Modal>

      <Modal show={!!deleteId} onHide={() => setDeleteId(null)}>
        <Modal.Header closeButton>
          <Modal.Title>
            <FormattedMessage id="actions.delete_playlist" />
          </Modal.Title>
        </Modal.Header>
        <Modal.Body>
          <FormattedMessage id="playlists_page.delete_confirm" />
        </Modal.Body>
        <Modal.Footer>
          <Button variant="secondary" onClick={() => setDeleteId(null)}>
            <FormattedMessage id="actions.cancel" />
          </Button>
          <Button variant="danger" onClick={handleDelete}>
            <FormattedMessage id="actions.delete" />
          </Button>
        </Modal.Footer>
      </Modal>
    </div>
  );
};

export default PlaylistList;
