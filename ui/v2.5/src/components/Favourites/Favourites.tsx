import React from "react";
import { Tab } from "react-bootstrap";
import { useRouteMatch } from "react-router-dom";
import { Helmet } from "react-helmet";
import { useIntl } from "react-intl";
import { useTitleProps } from "src/hooks/title";
import { useTabKey, StashTabs } from "src/components/Shared/DetailsPage/Tabs";
import { lazyComponent } from "src/utils/lazyComponent";
import { View } from "src/components/List/views";
import { ListFilterModel } from "src/models/list-filter/filter";
import { BooleanCriterion } from "src/models/list-filter/criteria/criterion";
import {
  FavoriteSceneCriterion,
  FavoriteImageCriterion,
  FavoriteGalleryCriterion,
  FavoritePerformerCriterion,
  FavoriteSceneCriterionOption,
  FavoriteImageCriterionOption,
  FavoriteGalleryCriterionOption,
  FavoritePerformerCriterionOption,
} from "src/models/list-filter/criteria/favorite";
import "./styles.scss";

const FilteredSceneList = lazyComponent(() => import("../Scenes/SceneList"));
const FilteredImageList = lazyComponent(async () => {
  const m = await import("../Images/ImageList");
  return { default: m.FilteredImageList };
});
const FilteredGalleryList = lazyComponent(async () => {
  const m = await import("../Galleries/GalleryList");
  return { default: m.FilteredGalleryList };
});
const FilteredPerformerList = lazyComponent(async () => {
  const m = await import("../Performers/PerformerList");
  return { default: m.FilteredPerformerList };
});

const validTabs = ["scenes", "images", "galleries", "performers"] as const;

function makeFavoriteFilterHook(
  CriterionClass: new () => BooleanCriterion,
  optionObj: { type: string }
) {
  return (filter: ListFilterModel) => {
    if (
      !filter.criteria.find((c) => c.criterionOption.type === optionObj.type)
    ) {
      const c = new CriterionClass();
      c.value = "true";
      filter.criteria.push(c);
    }
    return filter;
  };
}

const sceneFavoriteHook = makeFavoriteFilterHook(
  FavoriteSceneCriterion,
  FavoriteSceneCriterionOption
);
const imageFavoriteHook = makeFavoriteFilterHook(
  FavoriteImageCriterion,
  FavoriteImageCriterionOption
);
const galleryFavoriteHook = makeFavoriteFilterHook(
  FavoriteGalleryCriterion,
  FavoriteGalleryCriterionOption
);
const performerFavoriteHook = makeFavoriteFilterHook(
  FavoritePerformerCriterion,
  FavoritePerformerCriterionOption
);

const FavouriteTabs: React.FC<{ tabKey?: string }> = ({ tabKey }) => {
  const intl = useIntl();

  const { tabsProps } = useTabKey({
    tabKey,
    validTabs,
    defaultTabKey: "images",
    baseURL: "/favourites",
  });

  return (
    <StashTabs id="favourites-tabs" mountOnEnter unmountOnExit {...tabsProps}>
      <Tab eventKey="scenes" title={intl.formatMessage({ id: "scenes" })}>
        <FilteredSceneList
          filterHook={sceneFavoriteHook}
          alterQuery={tabKey === "scenes"}
          view={View.FavouriteScenes}
        />
      </Tab>
      <Tab eventKey="images" title={intl.formatMessage({ id: "images" })}>
        <FilteredImageList
          filterHook={imageFavoriteHook}
          alterQuery={tabKey === "images"}
          view={View.FavouriteImages}
        />
      </Tab>
      <Tab eventKey="galleries" title={intl.formatMessage({ id: "galleries" })}>
        <FilteredGalleryList
          filterHook={galleryFavoriteHook}
          alterQuery={tabKey === "galleries"}
          view={View.FavouriteGalleries}
        />
      </Tab>
      <Tab
        eventKey="performers"
        title={intl.formatMessage({ id: "performers" })}
      >
        <FilteredPerformerList
          filterHook={performerFavoriteHook}
          alterQuery={tabKey === "performers"}
          view={View.FavouritePerformers}
        />
      </Tab>
    </StashTabs>
  );
};

const Favourites: React.FC = () => {
  const titleProps = useTitleProps({ id: "favourites" });
  const match = useRouteMatch<{ tab?: string }>("/favourites/:tab?");
  const tabKey = match?.params.tab;

  return (
    <div className="row">
      <div className="detail-body">
        <Helmet {...titleProps} />
        <FavouriteTabs tabKey={tabKey} />
      </div>
    </div>
  );
};

export default Favourites;
