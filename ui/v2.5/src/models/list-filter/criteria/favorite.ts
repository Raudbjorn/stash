import { BooleanCriterion, BooleanCriterionOption } from "./criterion";

export const FavoritePerformerCriterionOption = new BooleanCriterionOption(
  "favourite",
  "filter_favorites",
  () => new FavoritePerformerCriterion()
);

export class FavoritePerformerCriterion extends BooleanCriterion {
  constructor() {
    super(FavoritePerformerCriterionOption);
  }
}
export const FavoriteTagCriterionOption = new BooleanCriterionOption(
  "favourite",
  "favorite",
  () => new FavoriteTagCriterion()
);

export class FavoriteTagCriterion extends BooleanCriterion {
  constructor() {
    super(FavoriteTagCriterionOption);
  }
}

export const FavoriteStudioCriterionOption = new BooleanCriterionOption(
  "favourite",
  "favorite",
  () => new FavoriteStudioCriterion()
);

export class FavoriteStudioCriterion extends BooleanCriterion {
  constructor() {
    super(FavoriteStudioCriterionOption);
  }
}

export const FavoriteSceneCriterionOption = new BooleanCriterionOption(
  "favourite",
  "favorite",
  () => new FavoriteSceneCriterion()
);

export class FavoriteSceneCriterion extends BooleanCriterion {
  constructor() {
    super(FavoriteSceneCriterionOption);
  }
}

export const FavoriteImageCriterionOption = new BooleanCriterionOption(
  "favourite",
  "favorite",
  () => new FavoriteImageCriterion()
);

export class FavoriteImageCriterion extends BooleanCriterion {
  constructor() {
    super(FavoriteImageCriterionOption);
  }
}

export const FavoriteGalleryCriterionOption = new BooleanCriterionOption(
  "favourite",
  "favorite",
  () => new FavoriteGalleryCriterion()
);

export class FavoriteGalleryCriterion extends BooleanCriterion {
  constructor() {
    super(FavoriteGalleryCriterionOption);
  }
}

export const PerformerFavoriteCriterionOption = new BooleanCriterionOption(
  "performer_favorite",
  "performer_favorite",
  () => new PerformerFavoriteCriterion()
);

export class PerformerFavoriteCriterion extends BooleanCriterion {
  constructor() {
    super(PerformerFavoriteCriterionOption);
  }
}
