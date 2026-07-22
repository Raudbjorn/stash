import React from "react";
import { Route, Switch } from "react-router-dom";
import { useIntl } from "react-intl";
import { Helmet } from "react-helmet";
import { PlaylistList } from "./PlaylistList";
import { Playlist } from "./PlaylistDetails/Playlist";

const Playlists: React.FC = () => {
  const intl = useIntl();

  const title_template = `${intl.formatMessage({
    id: "playlists",
  })} | Stash`;

  return (
    <>
      <Helmet
        defaultTitle={title_template}
        titleTemplate={`%s | ${title_template}`}
      />
      <Switch>
        <Route exact path="/playlists" component={PlaylistList} />
        <Route path="/playlists/:id" component={Playlist} />
      </Switch>
    </>
  );
};

export default Playlists;
