import React from "react";
import { FormattedMessage } from "react-intl";
import { SceneCard } from "../../Scenes/SceneCard";
import * as GQL from "src/core/generated-graphql";

interface IProps {
  playlistId: string;
  entries: { position: number; scene: GQL.SlimSceneDataFragment }[];
}

export const PlaylistScenesPanel: React.FC<IProps> = ({ entries }) => {
  if (entries.length === 0) {
    return (
      <div className="text-center text-muted py-5">
        <FormattedMessage id="playlists_page.empty" />
      </div>
    );
  }

  return (
    <div className="playlist-scenes-panel">
      <div className="row justify-content-center">
        {entries.map(({ scene }, index) => (
          <div key={scene.id} className="col-auto mb-4">
            <SceneCard scene={scene} index={index} />
          </div>
        ))}
      </div>
    </div>
  );
};

export default PlaylistScenesPanel;
