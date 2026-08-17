package tagging

import (
	"context"
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
)

type aggregateTagRepo struct {
	mocks.TagReaderWriter
	byStash map[string][]int
	tags    map[int]*models.Tag
	parents map[int][]int
}

func (r *aggregateTagRepo) FindByStashID(_ context.Context, stashID models.StashID) ([]*models.Tag, error) {
	ids := r.byStash[stashID.Endpoint+"\x00"+stashID.StashID]
	ret := make([]*models.Tag, 0, len(ids))
	for _, id := range ids {
		ret = append(ret, r.tags[id])
	}
	return ret, nil
}

func (r *aggregateTagRepo) Find(_ context.Context, id int) (*models.Tag, error) {
	return r.tags[id], nil
}

func (r *aggregateTagRepo) GetParentIDs(_ context.Context, id int) ([]int, error) {
	return append([]int(nil), r.parents[id]...), nil
}

func TestAggregateSupportParentsBoostsParentWithoutMutation(t *testing.T) {
	const endpoint = "https://stashdb.org/graphql"
	tags := &aggregateTagRepo{
		byStash: map[string][]int{
			endpoint + "\x00bdsm-stash":     {1},
			endpoint + "\x00dominant-stash": {2},
		},
		tags: map[int]*models.Tag{
			1: {ID: 1, Name: "BDSM"},
			2: {ID: 2, Name: "Dominant"},
		},
		parents: map[int][]int{2: {1}},
	}
	beforeTags := len(tags.tags)
	beforeRelations := cloneIntLists(tags.parents)
	service := &Service{repo: models.Repository{TxnManager: nullTxn{}, Tag: tags}}

	rows, err := service.aggregateSupportParents(context.Background(), []llamaprov.LabelSupport{
		{Tag: "BDSM", StashID: "bdsm-stash", Frames: 4, SpanCount: 1},
		{Tag: "Dominant", StashID: "dominant-stash", Frames: 1, SpanCount: 1},
	}, endpoint)
	if err != nil {
		t.Fatal(err)
	}

	var bdsm, dominant *LabelSupportWithParents
	for i := range rows {
		switch rows[i].Tag {
		case "BDSM":
			bdsm = &rows[i]
		case "Dominant":
			dominant = &rows[i]
		}
	}
	if bdsm == nil || bdsm.Frames+bdsm.ParentBoost != 5 {
		t.Fatalf("BDSM aggregate = %#v, want 4 own + 1 child frame", bdsm)
	}
	if dominant == nil || !reflect.DeepEqual(dominant.ParentIDs, []int{1}) || !reflect.DeepEqual(dominant.ParentNames, []string{"BDSM"}) {
		t.Fatalf("Dominant parents = %#v", dominant)
	}
	if len(tags.tags) != beforeTags || !reflect.DeepEqual(tags.parents, beforeRelations) {
		t.Fatalf("tag graph mutated: tags=%d parents=%v", len(tags.tags), tags.parents)
	}
}

func cloneIntLists(input map[int][]int) map[int][]int {
	ret := make(map[int][]int, len(input))
	for key, values := range input {
		ret[key] = append([]int(nil), values...)
	}
	return ret
}
