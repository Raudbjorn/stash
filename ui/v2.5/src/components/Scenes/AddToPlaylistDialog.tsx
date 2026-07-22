import React, { useState } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Button, Form, Modal } from "react-bootstrap";
import { faPlusCircle } from "@fortawesome/free-solid-svg-icons";
import { Icon } from "../Shared/Icon";
import { PlaylistSelect } from "../Playlists/PlaylistSelect";
import { usePlaylists, usePlaylistMutations } from "src/hooks/usePlaylist";
import * as GQL from "src/core/generated-graphql";

interface IProps {
  selected: Pick<GQL.SlimSceneDataFragment, "id" | "title">[];
  onClose: (applied: boolean) => void;
}

export const AddToPlaylistDialog: React.FC<IProps> = ({
  selected,
  onClose,
}) => {
  const intl = useIntl();
  const { refetch: refetchPlaylists } = usePlaylists();
  const { addScenesToPlaylist, createPlaylist } = usePlaylistMutations();

  const [selectedPlaylistId, setSelectedPlaylistId] = useState<
    string | undefined
  >();
  const [showCreateNew, setShowCreateNew] = useState(false);
  const [newPlaylistName, setNewPlaylistName] = useState("");
  const [isLoading, setIsLoading] = useState(false);

  const handleAdd = async () => {
    if (!selectedPlaylistId || selected.length === 0) return;

    setIsLoading(true);
    try {
      const sceneIds = selected.map((s) => s.id);
      await addScenesToPlaylist(selectedPlaylistId, sceneIds);
      refetchPlaylists();
      onClose(true);
    } catch (err) {
      // Error handled in hook
    } finally {
      setIsLoading(false);
    }
  };

  const handleCreateAndAdd = async () => {
    if (!newPlaylistName.trim() || selected.length === 0) return;

    setIsLoading(true);
    try {
      const playlist = await createPlaylist(newPlaylistName.trim());
      if (playlist?.id) {
        const sceneIds = selected.map((s) => s.id);
        await addScenesToPlaylist(playlist.id, sceneIds);
      }
      refetchPlaylists();
      onClose(true);
    } catch (err) {
      // Error handled in hook
    } finally {
      setIsLoading(false);
    }
  };

  const count = selected.length;

  return (
    <Modal show onHide={() => onClose(false)}>
      <Modal.Header closeButton>
        <Modal.Title>
          <Icon icon={faPlusCircle} className="mr-2" />
          <FormattedMessage
            id="dialogs.add_to_playlist.title"
            values={{ count }}
          />
        </Modal.Title>
      </Modal.Header>
      <Modal.Body>
        {showCreateNew ? (
          <div>
            <Form.Group>
              <Form.Label>
                <FormattedMessage id="playlists_page.new_name" />
              </Form.Label>
              <Form.Control
                type="text"
                value={newPlaylistName}
                onChange={(e) => setNewPlaylistName(e.target.value)}
                placeholder={intl.formatMessage({
                  id: "playlists_page.name_placeholder",
                })}
                autoFocus
              />
            </Form.Group>
            <Button
              variant="link"
              className="p-0"
              onClick={() => setShowCreateNew(false)}
            >
              <FormattedMessage id="actions.select_existing_playlist" />
            </Button>
          </div>
        ) : (
          <div>
            <Form.Group>
              <Form.Label>
                <FormattedMessage id="playlists_page.select" />
              </Form.Label>
              <PlaylistSelect
                value={selectedPlaylistId}
                onChange={(id) => setSelectedPlaylistId(id as string | undefined)}
                isClearable
                placeholder={intl.formatMessage({
                  id: "playlists_page.select_placeholder",
                })}
              />
            </Form.Group>
            <Button
              variant="link"
              className="p-0"
              onClick={() => setShowCreateNew(true)}
            >
              <FormattedMessage id="actions.create_new_playlist" />
            </Button>
          </div>
        )}

        {count > 0 && (
          <div className="mt-3 text-muted small">
            <FormattedMessage
              id="dialogs.add_to_playlist.scene_count"
              values={{ count }}
            />
          </div>
        )}
      </Modal.Body>
      <Modal.Footer>
        <Button variant="secondary" onClick={() => onClose(false)}>
          <FormattedMessage id="actions.cancel" />
        </Button>
        {showCreateNew ? (
          <Button
            variant="primary"
            onClick={handleCreateAndAdd}
            disabled={!newPlaylistName.trim() || isLoading}
          >
            <FormattedMessage id="actions.create_and_add" />
          </Button>
        ) : (
          <Button
            variant="primary"
            onClick={handleAdd}
            disabled={!selectedPlaylistId || isLoading}
          >
            <FormattedMessage id="actions.add" />
          </Button>
        )}
      </Modal.Footer>
    </Modal>
  );
};

export default AddToPlaylistDialog;
