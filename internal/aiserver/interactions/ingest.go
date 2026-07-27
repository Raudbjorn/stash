package interactions

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// Ingest accepts a batch of interaction events.
//
// The pipeline, in order:
//  1. sort by client timestamp, so replay order is deterministic regardless of
//     the order the browser flushed them in;
//  2. resolve every session id to its canonical form;
//  3. validate and normalise each event, collecting per-event errors;
//  4. write the surviving events in ONE transaction;
//  5. fold them into session state, then derive scene watches, segments and
//     view counters.
//
// Structural divergence from the original, deliberate and load-bearing: the
// Python wrapped each event in a SAVEPOINT so one bad row could not poison the
// batch. This engine returns SQLITE_BUSY for SAVEPOINT while a write is in
// flight, so instead every event is validated BEFORE the transaction opens and
// only pre-validated rows are written. The observable result is the same
// accepted/duplicates/errors triple; the mechanism is not.
func (s *Service) Ingest(ctx context.Context, events []EventIn, fallbackFingerprint string) (IngestResult, error) {
	result := IngestResult{Errors: []string{}}
	if len(events) == 0 {
		return result, nil
	}

	settings := LoadSettings(ctx, s.db)
	fingerprint := fingerprintForBatch(events, fallbackFingerprint)

	// Deterministic ordering: the segment replay depends on it.
	sorted := make([]EventIn, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS.Before(sorted[j].TS) })

	// One query answers "which of these have we already seen", rather than one
	// per event.
	clientIDs := make([]string, 0, len(sorted))
	for _, ev := range sorted {
		if ev.ID != "" {
			clientIDs = append(clientIDs, ev.ID)
		}
	}
	seen, err := s.db.ExistingClientEventIDs(ctx, clientIDs)
	if err != nil {
		return result, err
	}

	// Resolve each distinct incoming session id once.
	sessionIDs := map[string]string{}
	for _, ev := range sorted {
		if ev.SessionID == "" {
			continue
		}
		if _, done := sessionIDs[ev.SessionID]; done {
			continue
		}
		canonical, err := s.resolveSessionID(ctx, ev.SessionID, fingerprint, settings)
		if err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("session=%s err=%v", ev.SessionID, err))
			continue
		}
		sessionIDs[ev.SessionID] = canonical
	}

	// Validate everything before opening the transaction.
	normalized := make([]normalizedEvent, 0, len(sorted))
	for _, ev := range sorted {
		canonical, ok := sessionIDs[ev.SessionID]
		if !ok {
			result.Errors = append(result.Errors,
				fmt.Sprintf("event=%s session=%s type=%s err=unresolved session", ev.ID, ev.SessionID, ev.Type))
			continue
		}

		if ev.ID != "" && seen[ev.ID] {
			result.Duplicates++
			continue
		}

		normalized = append(normalized, normalizedEvent{
			in:        ev,
			sessionID: canonical,
			entityID:  sanitizeEntityID(ev.EntityID),
			clientTS:  ev.TS.UTC(),
			// Placeholder null strings would otherwise be stored and later
			// compared as real values.
			metadata: normalizeMetadata(ev.Metadata),
			// Progress events drive state but are never persisted; that is what
			// keeps the raw event table small under continuous playback.
			store: ev.Type != EventSceneWatchProgress,
		})

		if ev.ID != "" {
			// Guard against a duplicate inside this same batch.
			seen[ev.ID] = true
		}
	}

	if len(normalized) == 0 {
		return result, nil
	}

	// One transaction for the whole batch.
	if err := s.db.InTx(ctx, func(tx *sql.Tx) error {
		for _, ev := range normalized {
			if !ev.store {
				continue
			}
			var clientEventID *string
			if ev.in.ID != "" {
				id := ev.in.ID
				clientEventID = &id
			}
			if _, err := store.InsertEvent(ctx, tx, store.InteractionEvent{
				ClientEventID: clientEventID,
				SessionID:     ev.sessionID,
				EventType:     ev.in.Type,
				EntityType:    ev.in.EntityType,
				EntityID:      ev.entityID,
				ClientTS:      ev.clientTS,
				Metadata:      ev.metadata,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return result, err
	}
	result.Accepted = len(normalized)

	if err := s.applySessionState(ctx, normalized); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("session state: %v", err))
	}

	if err := s.processSceneSummaries(ctx, normalized, settings, &result); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("scene summaries: %v", err))
	}
	if err := s.processImageDerived(ctx, normalized); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("image derived: %v", err))
	}
	if err := s.persistLibrarySearches(ctx, normalized); err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("library search: %v", err))
	}

	return result, nil
}

// normalizeMetadata applies the null-string normalisation to event metadata.
func normalizeMetadata(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	normalized, _ := store.NormalizeNullStrings(meta).(map[string]any)
	return normalized
}

// processImageDerived updates view counters for images touched by a batch.
func (s *Service) processImageDerived(ctx context.Context, events []normalizedEvent) error {
	counts := map[int]int{}
	lastViewed := map[int]timeValue{}
	touchedSet := map[int]bool{}

	for _, ev := range events {
		if ev.in.EntityType != "image" {
			continue
		}
		touchedSet[ev.entityID] = true
		if ev.in.Type != EventImageView {
			continue
		}
		counts[ev.entityID]++
		if prev, ok := lastViewed[ev.entityID]; !ok || ev.clientTS.After(prev.t) {
			lastViewed[ev.entityID] = timeValue{t: ev.clientTS}
		}
	}

	if len(touchedSet) == 0 {
		return nil
	}

	touched := make([]int, 0, len(touchedSet))
	for id := range touchedSet {
		touched = append(touched, id)
	}
	sort.Ints(touched)

	return s.db.BumpImageViews(ctx, counts, flattenTimes(lastViewed), touched)
}

// persistLibrarySearches records search and filter activity for analytics.
func (s *Service) persistLibrarySearches(ctx context.Context, events []normalizedEvent) error {
	for _, ev := range events {
		if ev.in.Type != EventLibrarySearch && ev.in.EntityType != "library" {
			continue
		}

		// The library may be identified by the entity id or, failing that, by
		// a metadata field.
		library := ""
		if ev.entityID != 0 {
			library = fmt.Sprint(ev.entityID)
		} else if name, ok := metaString(ev.metadata, "library"); ok {
			library = name
		}
		if library == "" {
			continue
		}

		var query *string
		if q, ok := metaString(ev.metadata, "query"); ok && q != "" {
			query = &q
		}
		var filters any
		if ev.metadata != nil {
			filters = ev.metadata["filters"]
		}

		if err := s.db.InsertLibrarySearch(ctx, ev.sessionID, library, query, filters); err != nil {
			return err
		}
	}
	return nil
}
