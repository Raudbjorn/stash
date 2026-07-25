import React from "react";
import { Route, Switch } from "react-router-dom";
import { Helmet } from "react-helmet";
import { ClipList } from "./ClipList";
import Clip from "./ClipDetails/Clip";
import "./styles.scss";

const ClipRoutes: React.FC = () => {
  return (
    <>
      <Helmet>
        <title>Clips</title>
      </Helmet>
      <Switch>
        <Route exact path="/clips" component={ClipList} />
        <Route path="/clips/:id" component={Clip} />
      </Switch>
    </>
  );
};

export default ClipRoutes;
