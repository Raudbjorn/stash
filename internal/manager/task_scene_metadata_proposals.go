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
	"github.com/stashapp/stash/pkg/scene/metadata"
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
		record, err := s.Repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		if record == nil {
			return fmt.Errorf("scene metadata plan %s/%d not found", runID, sceneID)
		}
		switch SceneMetadataPlanState(record.State) {
		case SceneMetadataPlanApplied, SceneMetadataPlanStale:
			return fmt.Errorf("scene metadata plan %s/%d is already %s", runID, sceneID, record.State)
		}
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
		actions, err = s.Repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		state, update := reviewedSceneMetadataPlanState(actions)
		if !update {
			return nil
		}
		return s.Repository.SceneMetadataPlan.SetSceneMetadataPlanState(ctx, runID, sceneID, string(state), nil)
	})
	return err == nil, err
}

func (s *Manager) AcceptSceneMetadataProposalActions(ctx context.Context, runID string, sceneID int, actionIDs []string) (bool, error) {
	return s.setSceneMetadataProposalActionState(ctx, runID, sceneID, actionIDs, string(SceneMetadataPlanAccepted))
}

func (s *Manager) RejectSceneMetadataProposalActions(ctx context.Context, runID string, sceneID int, actionIDs []string) (bool, error) {
	return s.setSceneMetadataProposalActionState(ctx, runID, sceneID, actionIDs, string(SceneMetadataPlanRejected))
}
func (s *Manager) SelectSceneMetadataRemoteCandidate(
	ctx context.Context,
	runID string,
	sceneID int,
	endpoint string,
	remoteID string,
) (bool, error) {
	if s.Repository.SceneMetadataPlan == nil {
		return false, fmt.Errorf("scene metadata plan store is unavailable")
	}
	err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		record, err := s.Repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		if record == nil {
			return fmt.Errorf("scene metadata plan %s/%d not found", runID, sceneID)
		}
		switch SceneMetadataPlanState(record.State) {
		case SceneMetadataPlanApplied, SceneMetadataPlanStale:
			return fmt.Errorf("scene metadata plan %s/%d is already %s", runID, sceneID, record.State)
		}
		var plan AnalysisPlan
		if err := json.Unmarshal([]byte(record.ProposalJSON), &plan); err != nil {
			return fmt.Errorf("decode scene metadata plan: %w", err)
		}
		var selected *RemoteSceneCandidate
		normalizedEndpoint := normalizeStashBoxEndpoint(endpoint)
		for index := range plan.RemoteCandidates {
			candidate := &plan.RemoteCandidates[index]
			if normalizeStashBoxEndpoint(candidate.Endpoint) == normalizedEndpoint &&
				candidate.RemoteID == remoteID && candidate.Decision == metadata.SceneCandidateReview {
				selected = candidate
				break
			}
		}
		if selected == nil {
			return fmt.Errorf("remote scene candidate does not belong to plan %s/%d", runID, sceneID)
		}
		actions, err := s.Repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
		if err != nil {
			return err
		}
		var existingIDs []int
		existingSame := false
		for _, action := range actions {
			if action.Kind != sceneMetadataActionRemoteScene {
				continue
			}
			var payload sceneMetadataActionPayload
			if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode scene metadata action %d: %w", action.ID, err)
			}
			sameCandidate := normalizeStashBoxEndpoint(payload.Endpoint) == normalizedEndpoint &&
				payload.RemoteID == remoteID
			if sameCandidate {
				existingSame = true
				if action.State == string(SceneMetadataPlanAccepted) {
					return nil
				}
				continue
			}
			if action.State == string(SceneMetadataPlanProposed) ||
				action.State == string(SceneMetadataPlanAccepted) {
				existingIDs = append(existingIDs, action.ID)
			}
		}
		if len(existingIDs) > 0 {
			if _, err := s.Repository.SceneMetadataPlan.SetSceneMetadataPlanActionStates(
				ctx, runID, sceneID, existingIDs,
				string(SceneMetadataPlanAccepted), string(SceneMetadataPlanRejected),
			); err != nil {
				return err
			}
		}
		if existingSame {
			if len(existingIDs) > 0 {
				actions, err = s.Repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
				if err != nil {
					return err
				}
				state, update := reviewedSceneMetadataPlanState(actions)
				if !update {
					return nil
				}
				return s.Repository.SceneMetadataPlan.SetSceneMetadataPlanState(
					ctx, runID, sceneID, string(state), nil,
				)
			}
			return nil
		}
		reasons, _ := json.Marshal([]string{"review_selected_remote_scene"})
		action := models.SceneMetadataPlanActionRecord{
			RunID: runID, SceneID: sceneID, Kind: sceneMetadataActionRemoteScene,
			PayloadJSON: actionPayload(sceneMetadataActionPayload{
				Endpoint: selected.Endpoint,
				RemoteID: selected.RemoteID,
			}),
			State: string(SceneMetadataPlanAccepted), ReasonCodes: string(reasons),
		}
		if err := s.Repository.SceneMetadataPlan.CreateSceneMetadataPlanAction(ctx, &action); err != nil {
			return err
		}
		actions = append(actions, action)
		state, update := reviewedSceneMetadataPlanState(actions)
		if !update {
			return nil
		}
		return s.Repository.SceneMetadataPlan.SetSceneMetadataPlanState(
			ctx, runID, sceneID, string(state), nil,
		)
	})
	return err == nil, err
}

// ApplySceneMetadataPlans applies every accepted action in the given run/scene
// pairs and returns true when at least one action was persisted. If every
// plan has zero accepted actions (all rejected, or none provided), the call
// is a no-op and returns (false, nil) so callers can surface a "nothing to
// apply" message.
func (s *Manager) ApplySceneMetadataPlans(ctx context.Context, runID string, sceneIDs []string) (bool, error) {
	ids, err := stringslice.StringSliceToIntSlice(sceneIDs)
	if err != nil {
		return false, fmt.Errorf("parse scene IDs: %w", err)
	}
	if s.Repository.SceneMetadataPlan == nil {
		return false, fmt.Errorf("scene metadata plan store is unavailable")
	}
	var applied bool
	err = s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		for _, sceneID := range ids {
			appliedScene, err := applySceneMetadataPlanTx(ctx, s.Repository, runID, sceneID)
			if err != nil {
				return err
			}
			applied = applied || appliedScene
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, nil
}

func (s *Manager) PurgeSceneMetadataPlans(ctx context.Context, olderThan time.Duration) (int, error) {
	if s.Repository.SceneMetadataPlan == nil {
		return 0, fmt.Errorf("scene metadata plan store is unavailable")
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	var purged int
	err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		count, err := s.Repository.SceneMetadataPlan.PurgeSceneMetadataPlansBefore(ctx, cutoff, nil)
		if err != nil {
			return err
		}
		purged = count
		return nil
	})
	return purged, err
}

func (j *analyzeSceneMetadataJob) ApplySceneMetadataPlan(ctx context.Context, runID string, sceneID int) error {
	return applySceneMetadataPlan(ctx, j.repository, runID, sceneID)
}

func applySceneMetadataPlan(ctx context.Context, repository models.Repository, runID string, sceneID int) error {
	if repository.SceneMetadataPlan == nil {
		return fmt.Errorf("scene metadata plan store is unavailable")
	}
	return repository.WithTxn(ctx, func(ctx context.Context) error {
		_, err := applySceneMetadataPlanTx(ctx, repository, runID, sceneID)
		return err
	})
}

func applySceneMetadataPlanTx(ctx context.Context, repository models.Repository, runID string, sceneID int) (bool, error) {
	record, err := repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, runID, sceneID)
	if err != nil {
		return false, err
	}
	if record == nil {
		return false, fmt.Errorf("scene metadata plan %s/%d not found", runID, sceneID)
	}
	var plan AnalysisPlan
	if err := json.Unmarshal([]byte(record.ProposalJSON), &plan); err != nil {
		return false, fmt.Errorf("decode scene metadata plan: %w", err)
	}
	actions, err := repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, runID, sceneID)
	if err != nil {
		return false, err
	}
	accepted := make([]models.SceneMetadataPlanActionRecord, 0, len(actions))
	for _, action := range actions {
		switch SceneMetadataPlanState(action.State) {
		case SceneMetadataPlanProposed:
			return false, fmt.Errorf("scene metadata plan %s/%d has unreviewed actions", runID, sceneID)
		case SceneMetadataPlanAccepted:
			accepted = append(accepted, action)
		}
	}
	if len(accepted) == 0 {
		return false, nil
	}
	scene, err := repository.Scene.Find(ctx, sceneID)
	if err != nil {
		return false, err
	}
	if scene == nil {
		return false, fmt.Errorf("scene %d not found", sceneID)
	}
	if err := scene.LoadPerformerIDs(ctx, repository.Scene); err != nil {
		return false, err
	}
	if err := scene.LoadGroups(ctx, repository.Scene); err != nil {
		return false, err
	}
	if err := scene.LoadFiles(ctx, repository.Scene); err != nil {
		return false, err
	}
	if err := scene.LoadStashIDs(ctx, repository.Scene); err != nil {
		return false, err
	}
	if staleSceneHash(scene, scene.PerformerIDs.List(), scene.Groups.List(), scene.StashIDs.List(), scene.Files.Primary()) != plan.StaleSceneHash {
		acceptedIDs := make([]int, 0, len(accepted))
		for _, action := range accepted {
			acceptedIDs = append(acceptedIDs, action.ID)
		}
		if _, err := repository.SceneMetadataPlan.SetSceneMetadataPlanActionStates(
			ctx, runID, sceneID, acceptedIDs,
			string(SceneMetadataPlanAccepted), string(SceneMetadataPlanStale),
		); err != nil {
			return false, err
		}
		if err := repository.SceneMetadataPlan.SetSceneMetadataPlanState(
			ctx, runID, sceneID, string(SceneMetadataPlanStale), nil,
		); err != nil {
			return false, err
		}
		return false, nil
	}

	partial := models.NewScenePartial()
	performerIDs := append([]int(nil), scene.PerformerIDs.List()...)
	var createdPerformerIDs []int
	performerIDsSet := false
	var fileUpdates []*sceneMetadataFileUpdate
	for _, action := range accepted {
		var payload sceneMetadataActionPayload
		if err := json.Unmarshal([]byte(action.PayloadJSON), &payload); err != nil {
			return false, fmt.Errorf("decode scene metadata action %d: %w", action.ID, err)
		}
		switch action.Kind {
		case sceneMetadataActionPerformerIDs:
			performerIDs = append([]int(nil), payload.PerformerIDs...)
			performerIDsSet = true
		case sceneMetadataActionCreatePerformer:
			if payload.Performer == nil {
				return false, fmt.Errorf("performer action %d has no performer", action.ID)
			}
			id, err := createOrFindPlannedPerformer(ctx, repository, payload.Performer)
			if err != nil {
				return false, err
			}
			createdPerformerIDs = append(createdPerformerIDs, id)
			performerIDsSet = true
		case sceneMetadataActionStudioID:
			if payload.StudioID == nil {
				return false, fmt.Errorf("studio action %d has no studio ID", action.ID)
			}
			partial.StudioID = models.NewOptionalInt(*payload.StudioID)
			performerIDsSet = true
		case sceneMetadataActionCreateStudio:
			if payload.Studio == nil {
				return false, fmt.Errorf("studio action %d has no studio", action.ID)
			}
			if err := repository.Studio.Create(ctx, payload.Studio); err != nil {
				return false, err
			}
			partial.StudioID = models.NewOptionalInt(payload.Studio.ID)
		case sceneMetadataActionDate:
			if payload.Date == nil {
				return false, fmt.Errorf("date action %d has no date", action.ID)
			}
			date, err := models.ParseDate(*payload.Date)
			if err != nil {
				return false, err
			}
			partial.Date = models.NewOptionalDate(date)
		case sceneMetadataActionTitle:
			if payload.Title == nil {
				return false, fmt.Errorf("title action %d has no title", action.ID)
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
				return false, fmt.Errorf("remote scene action %d is incomplete", action.ID)
			}
			stashIDs := append([]models.StashID(nil), scene.StashIDs.List()...)
			stashIDs = append(stashIDs, models.StashID{Endpoint: payload.Endpoint, StashID: payload.RemoteID})
			partial.StashIDs = &models.UpdateStashIDs{StashIDs: stashIDs, Mode: models.RelationshipUpdateModeSet}
		default:
			return false, fmt.Errorf("unsupported scene metadata action kind %q", action.Kind)
		}
	}
	if performerIDsSet {
		performerIDs = mergeIDs(performerIDs, createdPerformerIDs)
		partial.PerformerIDs = &models.UpdateIDs{IDs: performerIDs, Mode: models.RelationshipUpdateModeSet}
	}
	for _, update := range fileUpdates {
		files, err := repository.File.Find(ctx, update.ID)
		if err != nil {
			return false, err
		}
		if len(files) != 1 {
			return false, fmt.Errorf("video file %d not found", update.ID)
		}
		file, ok := files[0].(*models.VideoFile)
		if !ok {
			return false, fmt.Errorf("file %d is not a video", update.ID)
		}
		file.Title = update.Title
		file.Comment = update.Comment
		file.Encoder = update.Encoder
		file.Tags = make(map[string]string, len(update.Tags))
		for key, value := range update.Tags {
			file.Tags[key] = value
		}
		file.CreationTime = update.CreationTime
		file.MetadataProbed = update.MetadataProbed
		if err := repository.File.Update(ctx, file); err != nil {
			return false, err
		}
	}
	if _, err := repository.Scene.UpdatePartial(ctx, sceneID, partial); err != nil {
		return false, err
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
		return false, err
	}
	if updated != len(acceptedIDs) {
		return false, fmt.Errorf("scene metadata action state changed during apply")
	}
	now := time.Now().UTC()
	if err := repository.SceneMetadataPlan.SetSceneMetadataPlanState(
		ctx, runID, sceneID, string(SceneMetadataPlanApplied), &now,
	); err != nil {
		return false, err
	}
	return true, nil
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

func reviewedSceneMetadataPlanState(actions []models.SceneMetadataPlanActionRecord) (SceneMetadataPlanState, bool) {
	hasAccepted, hasProposed := false, false
	for _, action := range actions {
		switch SceneMetadataPlanState(action.State) {
		case SceneMetadataPlanApplied, SceneMetadataPlanStale:
			return "", false
		case SceneMetadataPlanAccepted:
			hasAccepted = true
		case SceneMetadataPlanProposed:
			hasProposed = true
		}
	}
	if hasProposed {
		return SceneMetadataPlanProposed, true
	}
	if hasAccepted {
		return SceneMetadataPlanAccepted, true
	}
	return SceneMetadataPlanRejected, true
}
