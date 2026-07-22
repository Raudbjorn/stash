import React, { useState, useMemo } from "react";
import { FormattedMessage, useIntl } from "react-intl";
import { Button, Dropdown, Form } from "react-bootstrap";
import { faList, faStar } from "@fortawesome/free-solid-svg-icons";
import { Icon } from "../Shared/Icon";
import {
  usePlaylists,
  usePlaylistsWithScene,
  usePlaylistMutations,
} from "src/hooks/usePlaylist";

interface IProps {
  sceneId: string;
  compact?: boolean;
}

export const PlaylistButton: React.FC<IProps> = ({ sceneId, compact }) => {
  const intl = useIntl();
  const { playlists, refetch: refetchPlaylists } = usePlaylists();
  const { playlists: scenePlaylists, refetch: refetchScenePlaylists } =
    usePlaylistsWithScene(sceneId);
  const { addScenesToPlaylist, removeSceneFromPlaylist } =
    usePlaylistMutations();

  const [isOpen, setIsOpen] = useState(false);
  const [filter, setFilter] = useState("");

  const inPlaylistCount = scenePlaylists.length;

  const handleTogglePlaylist = async (
    playlistId: string,
    isInPlaylist: boolean
  ) => {
    try {
      if (isInPlaylist) {
        await removeSceneFromPlaylist(playlistId, sceneId);
      } else {
        await addScenesToPlaylist(playlistId, [sceneId]);
      }
      refetchScenePlaylists();
      refetchPlaylists();
    } catch (err) {
      // Error handled in hook
    }
  };

  // Sort: default playlist first, then alphabetical. Add membership status.
  const playlistsWithStatus = useMemo(() => {
    const mapped = playlists.map((p) => ({
      ...p,
      isInPlaylist: scenePlaylists.some((sp) => sp.id === p.id),
    }));
    mapped.sort((a, b) => {
      if (a.is_default && !b.is_default) return -1;
      if (!a.is_default && b.is_default) return 1;
      return a.name.localeCompare(b.name);
    });
    return mapped;
  }, [playlists, scenePlaylists]);

  const filteredPlaylists = useMemo(() => {
    if (!filter.trim()) return playlistsWithStatus;
    const lowerFilter = filter.toLowerCase();
    return playlistsWithStatus.filter((p) =>
      p.name.toLowerCase().includes(lowerFilter)
    );
  }, [playlistsWithStatus, filter]);

  const showFilter = playlists.length >= 5;

  const renderDropdownMenu = () => (
    <Dropdown.Menu>
      <Dropdown.Header>
        <FormattedMessage id="playlists" />
      </Dropdown.Header>
      {showFilter && (
        <div className="px-3 pb-2">
          <Form.Control
            size="sm"
            type="text"
            placeholder={intl.formatMessage({ id: "actions.search" })}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            onClick={(e: React.MouseEvent) => e.stopPropagation()}
            autoFocus
          />
        </div>
      )}
      {filteredPlaylists.map((playlist) => (
        <Dropdown.Item
          key={playlist.id}
          onClick={(e: React.MouseEvent) => {
            e.preventDefault();
            e.stopPropagation();
            handleTogglePlaylist(playlist.id, playlist.isInPlaylist);
          }}
        >
          <Form.Check
            type="checkbox"
            checked={playlist.isInPlaylist}
            onChange={() => {}}
            label={
              <>
                {playlist.is_default && (
                  <Icon icon={faStar} className="mr-1 text-warning" />
                )}
                {playlist.name}
              </>
            }
          />
        </Dropdown.Item>
      ))}
      {filteredPlaylists.length === 0 && (
        <Dropdown.Item disabled>
          <FormattedMessage id="playlists_page.no_playlists" />
        </Dropdown.Item>
      )}
    </Dropdown.Menu>
  );

  if (compact) {
    return (
      <Dropdown
        className="scene-playlist-button"
        show={isOpen}
        onToggle={(nextOpen) => {
          setIsOpen(nextOpen);
          if (!nextOpen) setFilter("");
        }}
      >
        <Dropdown.Toggle
          as={Button}
          variant={inPlaylistCount > 0 ? "success" : "secondary"}
          size="sm"
          className="minimal"
          title={intl.formatMessage({ id: "actions.add_to_playlist" })}
        >
          <Icon icon={faList} />
        </Dropdown.Toggle>
        {renderDropdownMenu()}
      </Dropdown>
    );
  }

  return (
    <Dropdown
      className="scene-playlist-button"
      show={isOpen}
      onToggle={(nextOpen) => {
        setIsOpen(nextOpen);
        if (!nextOpen) setFilter("");
      }}
    >
      <Dropdown.Toggle
        as={Button}
        variant={inPlaylistCount > 0 ? "success" : "secondary"}
        size="sm"
        title={intl.formatMessage({ id: "playlists" })}
      >
        <Icon icon={faList} className="mr-1" />
        {inPlaylistCount > 0 ? (
          <>
            <FormattedMessage id="actions.in_playlist" />
            {` (${inPlaylistCount})`}
          </>
        ) : (
          <FormattedMessage id="actions.add_to_playlist" />
        )}
      </Dropdown.Toggle>
      {renderDropdownMenu()}
    </Dropdown>
  );
};

export default PlaylistButton;
