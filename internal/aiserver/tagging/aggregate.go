package tagging

import (
	"context"
	"sort"

	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// LabelSupportWithParents reports direct/ancestor relationships and support
// contributed by descendant labels without mutating the user's tag graph.
type LabelSupportWithParents struct {
	llamaprov.LabelSupport
	ParentNames []string `json:"parent_names"`
	ParentIDs   []int    `json:"parent_ids"`
	ParentBoost int      `json:"parent_boost"`
}

func (s *Service) aggregateSupportParents(ctx context.Context, supports []llamaprov.LabelSupport, endpoint string) ([]LabelSupportWithParents, error) {
	rows := make([]LabelSupportWithParents, len(supports))
	for i, support := range supports {
		rows[i] = LabelSupportWithParents{LabelSupport: support, ParentNames: []string{}, ParentIDs: []int{}}
	}
	if len(supports) == 0 || endpoint == "" || s.repo.Tag == nil || s.repo.TxnManager == nil {
		return rows, nil
	}

	localIDs := make([]int, len(supports))
	boosts := make(map[int]int)
	parentTags := make(map[int]*models.Tag)
	err := txn.WithReadTxn(ctx, s.repo.TxnManager, func(ctx context.Context) error {
		for i, support := range supports {
			if support.StashID == "" {
				continue
			}
			matches, err := s.repo.Tag.FindByStashID(ctx, models.StashID{StashID: support.StashID, Endpoint: endpoint})
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				continue
			}
			sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
			localIDs[i] = matches[0].ID

			seen := map[int]bool{matches[0].ID: true}
			queue := []*models.Tag{matches[0]}
			for len(queue) > 0 {
				current := queue[0]
				queue = queue[1:]
				if err := current.LoadParentIDs(ctx, s.repo.Tag); err != nil {
					return err
				}
				for _, parentID := range current.ParentIDs.List() {
					if seen[parentID] {
						continue
					}
					seen[parentID] = true
					parent, err := s.repo.Tag.Find(ctx, parentID)
					if err != nil {
						return err
					}
					if parent == nil {
						continue
					}
					parentTags[parentID] = parent
					rows[i].ParentIDs = append(rows[i].ParentIDs, parentID)
					rows[i].ParentNames = append(rows[i].ParentNames, parent.Name)
					boosts[parentID] += support.Frames
					queue = append(queue, parent)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	present := make(map[int]bool, len(localIDs))
	for i, localID := range localIDs {
		if localID == 0 {
			continue
		}
		present[localID] = true
		rows[i].ParentBoost = boosts[localID]
		sortParents(&rows[i])
	}
	for parentID, boost := range boosts {
		if present[parentID] {
			continue
		}
		parent := parentTags[parentID]
		if parent == nil {
			continue
		}
		rows = append(rows, LabelSupportWithParents{
			LabelSupport: llamaprov.LabelSupport{Tag: parent.Name},
			ParentNames:  []string{},
			ParentIDs:    []int{},
			ParentBoost:  boost,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Tag == rows[j].Tag {
			return rows[i].StashID < rows[j].StashID
		}
		return rows[i].Tag < rows[j].Tag
	})
	return rows, nil
}

func sortParents(row *LabelSupportWithParents) {
	order := make([]int, len(row.ParentIDs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return row.ParentIDs[order[i]] < row.ParentIDs[order[j]] })
	ids := make([]int, len(order))
	names := make([]string, len(order))
	for i, index := range order {
		ids[i] = row.ParentIDs[index]
		names[i] = row.ParentNames[index]
	}
	row.ParentIDs = ids
	row.ParentNames = names
}
