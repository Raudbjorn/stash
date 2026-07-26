package sqlite

import (
	"context"

	"github.com/stashapp/stash/pkg/models"
)

type clipFilterHandler struct {
	clipFilter *models.ClipFilterType
}

func (qb *clipFilterHandler) validate() error {
	clipFilter := qb.clipFilter
	if clipFilter == nil {
		return nil
	}

	if err := validateFilterCombination(clipFilter.OperatorFilter); err != nil {
		return err
	}

	if subFilter := clipFilter.SubFilter(); subFilter != nil {
		sqb := &clipFilterHandler{clipFilter: subFilter}
		if err := sqb.validate(); err != nil {
			return err
		}
	}

	return nil
}

func (qb *clipFilterHandler) handle(ctx context.Context, f *filterBuilder) {
	clipFilter := qb.clipFilter
	if clipFilter == nil {
		return
	}

	if err := qb.validate(); err != nil {
		f.setError(err)
		return
	}

	f.handleCriterion(ctx, qb.criterionHandler())

	sf := clipFilter.SubFilter()
	if sf != nil {
		sub := &clipFilterHandler{sf}
		handleSubFilter(ctx, sub, f, clipFilter.OperatorFilter)
	}
}

func (qb *clipFilterHandler) criterionHandler() criterionHandler {
	clipFilter := qb.clipFilter
	return compoundHandler{
		stringCriterionHandler(clipFilter.Title, "clips.title"),
		intCriterionHandler(clipFilter.Rating100, "clips.rating", nil),
		qb.scenesCriterionHandler(clipFilter.Scenes),
		qb.tagsCriterionHandler(clipFilter.Tags),
		&timestampCriterionHandler{clipFilter.CreatedAt, "clips.created_at", nil},
		&timestampCriterionHandler{clipFilter.UpdatedAt, "clips.updated_at", nil},
	}
}

func (qb *clipFilterHandler) scenesCriterionHandler(scenes *models.MultiCriterionInput) criterionHandlerFunc {
	addJoinsFunc := func(f *filterBuilder, joinType joinType) {
		f.addJoin(joinType, sceneTable, "clips_scenes", "clips_scenes.id = clips.scene_id")
	}
	h := multiCriterionHandlerBuilder{
		primaryTable: clipTable,
		foreignTable: "clips_scenes",
		joinTable:    "",
		primaryFK:    sceneIDColumn,
		foreignFK:    sceneIDColumn,
		addJoinsFunc: addJoinsFunc,
	}
	return h.handler(scenes)
}

func (qb *clipFilterHandler) tagsCriterionHandler(tags *models.HierarchicalMultiCriterionInput) criterionHandlerFunc {
	h := joinedHierarchicalMultiCriterionHandlerBuilder{
		primaryTable: clipTable,
		foreignTable: tagTable,
		foreignFK:    "tag_id",

		relationsTable: "tags_relations",
		joinAs:         "clip_tag",
		joinTable:      clipsTagsTable,
		primaryFK:      clipIDColumn,
	}

	return h.handler(tags)
}
