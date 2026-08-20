package manager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

// TestStudioResolverWiringEndToEnd exercises the full read-only studio path:
// the conflict fixture forces the LLM, the LLM pick is honored, and the
// verified studio is returned as a proposal without mutating the library.
func TestStudioResolverWiringEndToEnd(t *testing.T) {
	job, q1, q2, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":"stashbox:https://one.example/graphql"}`

	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatalf("resolveStudioCandidate: %v", err)
	}
	if resolution.Status != studioResolutionProposed {
		t.Fatalf("status = %q, want %q", resolution.Status, studioResolutionProposed)
	}
	if resolution.ProviderID != "stashbox:https://one.example/graphql" {
		t.Fatalf("provider_id = %q, want stashbox:one", resolution.ProviderID)
	}
	if completer.callCount() != 1 {
		t.Fatalf("LLM calls = %d, want 1", completer.callCount())
	}
	if len(q1.calls) == 0 || len(q2.calls) == 0 {
		t.Fatalf("both Stash-box queriers should be exercised; q1=%d q2=%d", len(q1.calls), len(q2.calls))
	}

	if resolution.Proposed == nil || resolution.Proposed.Name == "" {
		t.Fatalf("proposed studio = %+v, want populated proposal", resolution.Proposed)
	}
	count := 0
	if err := job.repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		all, err := job.repository.Studio.All(ctx)
		if err != nil {
			return err
		}
		count = len(all)
		return nil
	}); err != nil {
		t.Fatalf("reading studios: %v", err)
	}
	if count != 0 {
		t.Fatalf("studio count = %d, want 0 before apply", count)
	}
}

// TestStudioProviderChoiceUnknownIDClassification locks in the
// isInvalidContextCompletionError path: an unknown id is treated as
// invalid (not unavailable), matching the performer context path.
func TestStudioProviderChoiceUnknownIDClassification(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":"made-up"}`
	if err := errors.Unwrap(nil); err != nil { // keep imports happy
		_ = err
	}
	_, err := job.extractStudioProviderChoice(context.Background(), conflictingCandidate, []studioMetadataProvider{
		{ID: "stashbox:https://one.example/graphql"},
	}, nil)
	if !errors.Is(err, errStudioProviderChoiceInvalid) {
		t.Fatalf("err = %v, want errStudioProviderChoiceInvalid", err)
	}
}

// Verify the studio JSON request body decodes the documented response
// shape: an empty provider_id is the documented "no provider matches"
// signal, which the resolver must not classify as invalid.
func TestStudioProviderChoiceAcceptsEmptyID(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":""}`
	choice, err := job.extractStudioProviderChoice(context.Background(), conflictingCandidate, []studioMetadataProvider{
		{ID: "stashbox:https://one.example/graphql"},
	}, nil)
	if err != nil {
		t.Fatalf("extractStudioProviderChoice: %v", err)
	}
	if choice != nil {
		t.Fatalf("choice = %+v, want nil (empty provider_id means no pick)", choice)
	}
}

// Ensure the payload that goes to the LLM is shaped as documented: a
// candidate, the conflicting provider list, and no extraneous fields.
func TestStudioProviderChoicePayloadShape(t *testing.T) {
	payload := studioProviderChoicePayload{
		Candidate: "Bravo Films",
		Providers: []studioProviderChoiceEntry{
			{ID: "stashbox:https://one.example/graphql", Name: "One"},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"candidate", "providers", "sources"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload missing key %q: %s", key, string(encoded))
		}
	}
	if _, ok := decoded["models"]; ok {
		t.Fatalf("payload leaked Go field name `models`")
	}
	_ = models.ScrapedStudio{} // keep models import used
}
