package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"github.com/stashapp/stash/internal/aiserver/interactions"
	"github.com/stashapp/stash/pkg/session"
)

// handleInteractionsSync ingests a batch of interaction events.
//
// The body is a bare JSON array, not an object - that is what the shipped
// InteractionTracker posts.
func (s *Server) handleInteractionsSync(w http.ResponseWriter, r *http.Request) {
	var events []interactions.EventIn
	if !decodeJSON(w, r, &events) {
		return
	}

	svc := s.backend.Interactions()
	if svc == nil {
		writeError(w, http.StatusServiceUnavailable, "interaction ingest is unavailable")
		return
	}

	result, err := svc.Ingest(r.Context(), events, clientFingerprint(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// clientFingerprint identifies which client a batch came from, so that a page
// reload can be stitched back onto the session it interrupted.
//
// An event's own client_id wins where present (the ingest layer prefers it);
// this is the fallback. Running inside Stash there is a better answer than the
// Python server had: the authenticated user. Where that is unavailable - Stash
// with no credentials configured - it falls back to hashing the request's
// origin, as the original did.
func clientFingerprint(r *http.Request) string {
	if user := authenticatedUser(r); user != "" {
		return "user:" + user
	}

	ip := requestIP(r)
	ua := r.Header.Get("User-Agent")
	if ip == "" && ua == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(ip + "|" + ua))
	return hex.EncodeToString(sum[:])
}

// authenticatedUser returns the current user id, if Stash's session middleware
// recorded one.
func authenticatedUser(r *http.Request) string {
	if id := session.GetCurrentUserID(r.Context()); id != nil {
		return *id
	}
	return ""
}

// requestIP extracts the client address, honouring a proxy header when present.
func requestIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, found := strings.Cut(fwd, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
