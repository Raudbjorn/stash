package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/performer"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

func parseSceneMetadataActionIdentifier(value, runID string, sceneID int) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid scene metadata action identifier")
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil || id < 1 || sceneMetadataActionIdentifier(runID, sceneID, id) != value {
		return 0, fmt.Errorf("invalid scene metadata action identifier")
	}
	return id, nil
}

func (s *Manager) setSceneMetadataProposalActionState(ctx context.Context, runID string, sceneID int, actionIDs []string, toState string) (bool, error) {
	ids := make([]int, 0, len(actionIDs))
	for _, identifier := range actionIDs {
		id, err := parseSceneMetadataActionIdentifier(identifier, runID, sceneID)
		if err != nil {
			return false, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return true, nil
	}
	if s.Repository.SceneMetadataPlan == nil {
		return false, fmt.Errorf("scene metadata plan store is unavailable")
	}
	err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		actions, err := s.Repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		available := make(map[int]struct{}, len(actions))
		for _, action := range actions {
			available[action.ID] = struct{}{}
		}
		for _, id := range ids {
			if _, exists := available[id]; !exists {
				return fmt.Errorf("scene metadata action %d does not belong to plan %s/%d", id, runID, sceneID)
			}
		}
		updated, err := s.Repository.SceneMetadataPlan.SetSceneMetadataPlanActionStates(
			ctx, runID, sceneID, ids, string(SceneMetadataPlanProposed), toState,
		)
		if err != nil {
			return err
		}
		if updated != len(ids) {
			return fmt.Errorf("one or more scene metadata actions are not proposed")
		}
		return nil
	})
	return err == nil, err
}

func (s *Manager) AcceptSceneMetadataProposalActions(ctx context.Context, runID string, sceneID int, actionIDs []string) (bool, error) {
	return s.setSceneMetadataProposalActionState(ctx, runID, sceneID, actionIDs, string(SceneMetadataPlanAccepted))
}

func (s *Manager) RejectSceneMetadataProposalActions(ctx context.Context, runID string, sceneID int, actionIDs []string) (bool, error) {
	return s.setSceneMetadataProposalActionState(ctx, runID, sceneID, actionIDs, string(SceneMetadataPlanRejected))
}

func (s *Manager) ApplySceneMetadataPlans(ctx context.Context, runID string, sceneIDs []string) (bool, error) {
	ids, err := stringslice.StringSliceToIntSlice(sceneIDs)
	if err != nil {
		return false, fmt.Errorf("parse scene IDs: %w", err)
	}
	for _, sceneID := range ids {
		if err := applySceneMetadataPlan(ctx, s.Repository, runID, sceneID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (j *analyzeSceneMetadataJob) ApplySceneMetadataPlan(ctx context.Context, runID string, sceneID int) error {
	return applySceneMetadataPlan(ctx, j.repository, runID, sceneID)
}

func applySceneMetadataPlan(ctx context.Context, repository models.Repository, runID string, sceneID int) error {
	if repository.SceneMetadataPlan == nil {
		return fmt.Errorf("scene metadata plan store is unavailable")
	}
	return repository.WithTxn(ctx, func(ctx context.Context) error {
		record, err := repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		if record == nil {
			return fmt.Errorf("scene metadata plan %s/%d not found", runID, sceneID)
		}
		var plan AnalysisPlan
		if err := json.Unmarshal([]byte(record.ProposalJSON), &plan); err != nil {
			return fmt.Errorf("decode scene metadata plan: %w", err)
		}
		scene, err := repository.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		if scene == nil {
			return fmt.Errorf("scene %d not found", sceneID)
		}
		if err := scene.LoadPerformerIDs(ctx, repository.Scene); err != nil {
			return err
		}
		if err := scene.LoadGroups(ctx, repository.Scene); err != nil {
			return err
		}
		if err := scene.LoadFiles(ctx, repository.Scene); err != nil {
			return err
		}
		if err := scene.LoadStashIDs(ctx, repository.Scene); err != nil {
			return err
		}
		if staleSceneHash(scene, scene.PerformerIDs.List()) != plan.StaleSceneHash {
			actions, err := repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
			if err != nil {
				return err
			}
			accepted := actionIDsInState(actions, string(SceneMetadataPlanAccepted))
			if _, err := repository.SceneMetadataPlan.SetSceneMetadataPlanActionStates(
				ctx, runID, sceneID, accepted,
				string(SceneMetadataPlanAccepted), string(SceneMetadataPlanStale),
			); err != nil {
				return err
			}
			return repository.SceneMetadataPlan.SetSceneMetadataPlanState(
				ctx, runID, sceneID, string(SceneMetadataPlanStale), nil,
			)
		}

		actions, err := repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		accepted := make([]models.SceneMetadataPlanActionRecord, 0, len(actions))
		for _, action := range actions {
			if action.State == string(SceneMetadataPlanAccepted) {
				accepted = append(accepted, action)
			}
		}
		if len(accepted) == 0 {
			return nil
		}

		partial := models.NewScenePartial()
		performerIDs := append([]int(nil), scene.PerformerIDs.List()...)
		performerIDsSet := false
		var fileUpdates []*models.VideoFile
		for _, action := range accepted {
			var payload sceneMetadataActionPayload
			if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode scene metadata action %d: %w", action.ID, err)
			}
			switch action.Kind {
			case sceneMetadataActionPerformerIDs:
				performerIDs = append([]int(nil), payload.PerformerIDs...)
				performerIDsSet = true
			case sceneMetadataActionCreatePerformer:
				if payload.Performer == nil {
					return fmt.Errorf("performer action %d has no performer", action.ID)
				}
				id, err := createOrFindPlannedPerformer(ctx, repository, payload.Performer)
				if err != nil {
					return err
				}
				performerIDs = mergeIDs(performerIDs, []int{id})
				performerIDsSet = true
			case sceneMetadataActionStudioID:
				if payload.StudioID == nil {
					return fmt.Errorf("studio action %d has no studio ID", action.ID)
				}
				partial.StudioID = models.NewOptionalInt(*payload.StudioID)
			case sceneMetadataActionCreateStudio:
				if payload.Studio == nil {
					return fmt.Errorf("studio action %d has no studio", action.ID)
				}
				if err := repository.Studio.Create(ctx, payload.Studio); err != nil {
					return err
				}
				partial.StudioID = models.NewOptionalInt(payload.Studio.ID)
			case sceneMetadataActionDate:
				if payload.Date == nil {
					return fmt.Errorf("date action %d has no date", action.ID)
				}
				date, err := models.ParseDate(*payload.Date)
				if err != nil {
					return err
				}
				partial.Date = models.NewOptionalDate(date)
			case sceneMetadataActionTitle:
				if payload.Title == nil {
					return fmt.Errorf("title action %d has no title", action.ID)
				}
				partial.Title = models.NewOptionalString(*payload.Title)
			case sceneMetadataActionGroups:
				partial.GroupIDs = &models.UpdateGroupIDs{Groups: payload.Groups, Mode: models.RelationshipUpdateModeSet}
			case sceneMetadataActionFileMetadata:
				if payload.File != nil {
					fileUpdates = append(fileUpdates, payload.File)
				}
			case sceneMetadataActionRemoteScene:
				if payload.Endpoint == "" || payload.RemoteID == "" {
					return fmt.Errorf("remote scene action %d is incomplete", action.ID)
				}
				stashIDs := append([]models.StashID(nil), scene.StashIDs.List()...)
				stashIDs = append(stashIDs, models.StashID{Endpoint: payload.Endpoint, StashID: payload.RemoteID})
				partial.StashIDs = &models.UpdateStashIDs{StashIDs: stashIDs, Mode: models.RelationshipUpdateModeSet}
			default:
				return fmt.Errorf("unsupported scene metadata action kind %q", action.Kind)
			}
		}
		if performerIDsSet {
			partial.PerformerIDs = &models.UpdateIDs{IDs: performerIDs, Mode: models.RelationshipUpdateModeSet}
		}
		for _, file := range fileUpdates {
			if err := repository.File.Update(ctx, file); err != nil {
				return err
			}
		}
		if _, err := repository.Scene.UpdatePartial(ctx, sceneID, partial); err != nil {
			return err
		}
		acceptedIDs := make([]int, 0, len(accepted))
		for _, action := range accepted {
			acceptedIDs = append(acceptedIDs, action.ID)
		}
		updated, err := repository.SceneMetadataPlan.SetSceneMetadataPlanActionStates(
			ctx, runID, sceneID, acceptedIDs,
			string(SceneMetadataPlanAccepted), string(SceneMetadataPlanApplied),
		)
		if err != nil {
			return err
		}
		if updated != len(acceptedIDs) {
			return fmt.Errorf("scene metadata action state changed during apply")
		}
		now := time.Now().UTC()
		return repository.SceneMetadataPlan.SetSceneMetadataPlanState(
			ctx, runID, sceneID, string(SceneMetadataPlanApplied), &now,
		)
	})
}

func createOrFindPlannedPerformer(ctx context.Context, repository models.Repository, proposed *models.Performer) (int, error) {
	fresh, err := findExactPerformerIdentities(ctx, repository.Performer, proposed.Name)
	if err != nil {
		return 0, err
	}
	if len(fresh) == 1 {
		return fresh[0].ID, nil
	}
	if len(fresh) > 1 {
		return 0, fmt.Errorf("performer %q became ambiguous before apply", proposed.Name)
	}
	input := models.CreatePerformerInput{Performer: proposed}
	if err := performer.ValidateCreate(ctx, *proposed, repository.Performer); err != nil {
		return 0, err
	}
	if err := repository.Performer.Create(ctx, &input); err != nil {
		return 0, err
	}
	return proposed.ID, nil
}

func actionIDsInState(actions []models.SceneMetadataPlanActionRecord, state string) []int {
	ids := make([]int, 0, len(actions))
	for _, action := range actions {
		if action.State == state {
			ids = append(ids, action.ID)
		}
	}
	return ids
}
