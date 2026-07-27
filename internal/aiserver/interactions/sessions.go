package interactions

import (
	"context"
	"errors"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// Sessions group a client's events. The client generates a session id, but it
// cannot be trusted to be stable: a page reload, a new tab, or a navigation can
// all produce a fresh id for what a person would call one viewing session. So
// incoming ids are resolved to a canonical one, and short-lived ids are recorded
// as aliases of it.

// resolveSessionID maps an incoming session id to the canonical one to store
// under, creating a session when there is nothing to attach to.
//
// The four cases, in order:
//  1. The id already names a session - use it.
//  2. The id is a known alias - use what it points at.
//  3. The same client has a still-open session whose last event is recent -
//     merge into it and remember the alias.
//  4. Otherwise create a new session, first finalising any of this client's
//     sessions that have gone stale.
func (s *Service) resolveSessionID(ctx context.Context, incoming string, fingerprint *string, settings Settings) (string, error) {
	if incoming == "" {
		return "", errors.New("event has no session id")
	}

	if _, err := s.db.GetSession(ctx, incoming); err == nil {
		return incoming, nil
	} else if !errors.Is(err, store.ErrSessionNotFound) {
		return "", err
	}

	if canonical, found, err := s.db.ResolveSessionAlias(ctx, incoming); err != nil {
		return "", err
	} else if found {
		return canonical, nil
	}

	now := s.now()
	threshold := now.Add(-settings.MergeTTL)

	if fingerprint != nil && *fingerprint != "" {
		canonical, found, err := s.db.FindRecentSessionForFingerprint(ctx, *fingerprint, threshold)
		if err != nil {
			return "", err
		}
		if found {
			if canonical != incoming {
				// Remember the mapping so later batches skip the lookup. A
				// duplicate is harmless: two batches may race to merge.
				if err := s.db.AddSessionAlias(ctx, incoming, canonical); err != nil {
					return "", err
				}
			}
			return canonical, nil
		}

		// Nothing to merge into, so anything still open for this client has
		// genuinely ended. Finalising here is what credits derived counts.
		if err := s.finalizeStaleSessions(ctx, *fingerprint, threshold, settings); err != nil {
			return "", err
		}
	}

	session := store.InteractionSession{
		SessionID:         incoming,
		LastEventTS:       now,
		SessionStartTS:    now,
		ClientFingerprint: fingerprint,
	}
	if err := s.db.CreateSession(ctx, session); err != nil {
		// A concurrent batch may have created it first; that is a success.
		if _, getErr := s.db.GetSession(ctx, incoming); getErr == nil {
			return incoming, nil
		}
		return "", err
	}
	return incoming, nil
}

// finalizeStaleSessions closes a client's abandoned sessions and credits a
// derived count to whatever each was last engaged with.
//
// This is the ONLY place derived_o_count is incremented. The reasoning: a
// session that ran long enough and ended on a particular scene is evidence of
// real engagement with it, in a way that merely opening the page is not.
//
// Sessions shorter than the minimum are still closed, just without credit.
func (s *Service) finalizeStaleSessions(ctx context.Context, fingerprint string, threshold time.Time, settings Settings) error {
	stale, err := s.db.StaleSessionsForFingerprint(ctx, fingerprint, threshold)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		return nil
	}

	minDuration := time.Duration(settings.MinSessionMinutes * float64(time.Minute))

	sceneCounts := map[int]int{}
	imageCounts := map[int]int{}
	var finalize []int64

	for _, session := range stale {
		finalize = append(finalize, session.ID)

		if session.LastEventTS.Sub(session.SessionStartTS) < minDuration {
			continue // too short to count as engagement
		}
		if session.LastEntityType == nil || session.LastEntityID == nil {
			continue // ended nowhere in particular
		}

		switch *session.LastEntityType {
		case "scene":
			sceneCounts[*session.LastEntityID]++
		case "image":
			imageCounts[*session.LastEntityID]++
		}
	}

	if err := s.db.CreditDerivedOCount(ctx, sceneCounts, imageCounts); err != nil {
		return err
	}
	return s.db.FinalizeSessions(ctx, finalize)
}

// applySessionState folds a batch's events into each session's mutable state:
// how recently it saw anything, and what it was last engaged with.
//
// The last-entity fields are what finalisation later credits, so they matter
// beyond bookkeeping.
func (s *Service) applySessionState(ctx context.Context, events []normalizedEvent) error {
	// Collect per session first so each is written once, not once per event.
	updates := map[string]*store.InteractionSession{}

	for _, ev := range events {
		session, ok := updates[ev.sessionID]
		if !ok {
			loaded, err := s.db.GetSession(ctx, ev.sessionID)
			if err != nil {
				if errors.Is(err, store.ErrSessionNotFound) {
					// Resolution guarantees the session exists; if it has gone
					// missing, skip rather than fail the whole batch.
					continue
				}
				return err
			}
			session = &loaded
			updates[ev.sessionID] = session
		}

		if ev.clientTS.After(session.LastEventTS) {
			session.LastEventTS = ev.clientTS
		}

		switch ev.in.EntityType {
		case "scene", "image", "gallery":
			entityType := ev.in.EntityType
			entityID := ev.entityID
			ts := ev.clientTS
			session.LastEntityType = &entityType
			session.LastEntityID = &entityID
			session.LastEntityEventTS = &ts

		case "session":
			// A session-scoped event may carry an explicit final entity, which
			// is how the frontend reports what the user was looking at when
			// the tab closed - the most valuable signal for crediting, and one
			// no ordinary event can provide.
			applyLastEntityFromMetadata(session, ev)
		}
	}

	out := make([]store.InteractionSession, 0, len(updates))
	for _, session := range updates {
		out = append(out, *session)
	}
	return s.db.UpdateSessions(ctx, out)
}

// applyLastEntityFromMetadata reads metadata.last_entity from a session event.
func applyLastEntityFromMetadata(session *store.InteractionSession, ev normalizedEvent) {
	lastEntity, ok := metaMap(ev.metadata, "last_entity")
	if !ok {
		return
	}

	entityType, hasType := metaString(lastEntity, "type")
	if !hasType || entityType == "" {
		return
	}
	rawID, hasID := lastEntity["id"]
	if !hasID {
		return
	}
	entityID, ok := toInt(rawID)
	if !ok {
		return
	}

	session.LastEntityType = &entityType
	session.LastEntityID = &entityID

	// The timestamp has been emitted in several forms; fall back to the
	// event's own time rather than dropping the entity.
	ts := ev.clientTS
	if raw, present := lastEntity["ts"]; present {
		if parsed, ok := parseEventTimestamp(raw); ok {
			ts = parsed
		}
	}
	session.LastEntityEventTS = &ts
}

// fingerprintForBatch decides which client a batch belongs to.
//
// A client-supplied id is preferred; it is stable across reloads in a way a
// derived one is not.
func fingerprintForBatch(events []EventIn, fallback string) *string {
	for _, ev := range events {
		if ev.ClientID != nil && *ev.ClientID != "" {
			id := *ev.ClientID
			return &id
		}
	}
	if fallback == "" {
		return nil
	}
	return &fallback
}
