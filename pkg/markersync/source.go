// Package markersync provides a native, plugin-independent implementation of
// scene marker synchronisation against external databases.
//
// It is a superset of two community plugins:
//   - ThePornDB (fetch-only, marker times expressed in SECONDS)
//   - timestamp.trade (bidirectional, marker times expressed in MILLISECONDS)
//
// The package is intentionally decoupled from the persistence layer and the
// GraphQL/manager layers via small interfaces (Source, Submitter, TagResolver,
// MarkerWriter) so that it can be unit tested with httptest stubs and fakes.
//
// The canonical internal time unit is ALWAYS seconds. Any unit conversion (for
// example timestamp.trade's milliseconds) is performed exclusively inside the
// relevant source adapter and never in shared code.
package markersync

import (
	"context"

	"github.com/stashapp/stash/pkg/models"
)

// StashID is an alias of models.StashID so that callers can pass scene stash
// identifiers straight from the models layer without an intermediate copy. Only
// the StashID and Endpoint fields are meaningful to this package.
type StashID = models.StashID

// MarkerCandidate is a source-agnostic representation of a scene marker fetched
// from (or to be submitted to) an external database. Seconds and EndSeconds are
// always expressed in seconds from the start of the scene.
type MarkerCandidate struct {
	// Title is the human readable marker title.
	Title string
	// PrimaryTag is the name of the marker's primary tag.
	PrimaryTag string
	// Seconds is the marker start time in seconds.
	Seconds float64
	// EndSeconds is the optional marker end time in seconds.
	EndSeconds *float64
	// ExtraTags is an optional list of additional tag names to attach to the
	// marker (excluding the primary tag).
	ExtraTags []string
}

// SceneIdentity carries the information required by sources to match a local
// scene against an external record. Additional match vectors (for example
// phash fingerprints) may be added here in later stages.
type SceneIdentity struct {
	// StashIDs is the set of stash-box identifiers associated with the scene.
	StashIDs []StashID
}

// StashIDForEndpoint returns the stash id associated with the given endpoint,
// if present.
func (s SceneIdentity) StashIDForEndpoint(endpoint string) (string, bool) {
	for _, sid := range s.StashIDs {
		if sid.Endpoint == endpoint {
			return sid.StashID, true
		}
	}
	return "", false
}

// Source is an external database from which scene markers can be fetched.
type Source interface {
	// Name returns a stable, human readable identifier for the source.
	Name() string
	// Enabled reports whether the source is configured and may be queried.
	Enabled() bool
	// FetchMarkers returns the markers that the source has for the given scene
	// identity. It returns (nil, nil) when the source has no record for the
	// scene (as opposed to an error).
	FetchMarkers(ctx context.Context, id SceneIdentity) ([]MarkerCandidate, error)
}

// The capability-provider interfaces below are OPTIONAL extensions of a Source.
// A Source advertises support for fetching a given class of "extras" (extra
// scene URLs, galleries, groups) purely by implementing the matching interface;
// callers type-assert against it. timestamp.trade implements all three; TPDB
// implements none. Keeping them separate from Source means adding an extra class
// never forces every source to grow a no-op method.
//
// Like MarkerCandidate, these are wire-format-free domain structs. All external
// JSON parsing lives in the source adapter.

// ExtraURLProvider is a Source that can supply additional scene URLs for a
// scene, to be merged into the scene's existing URLs.
type ExtraURLProvider interface {
	// FetchExtraURLs returns extra URLs for the given scene identity. It returns
	// (nil, nil) when the source has no record for the scene.
	FetchExtraURLs(ctx context.Context, id SceneIdentity) ([]string, error)
}

// GalleryRef is a source-agnostic reference to a gallery associated with a
// scene. MD5s are the md5 checksums of the gallery's files (used to match a
// local gallery); URLs are the gallery's external URLs.
type GalleryRef struct {
	// MD5s are the md5 file checksums used to locate the local gallery.
	MD5s []string
	// URLs are the gallery's external URLs.
	URLs []string
}

// GalleryProvider is a Source that can supply gallery references for a scene.
type GalleryProvider interface {
	// FetchGalleries returns gallery references for the given scene identity. It
	// returns (nil, nil) when the source has no record for the scene.
	FetchGalleries(ctx context.Context, id SceneIdentity) ([]GalleryRef, error)
}

// GroupRef is a source-agnostic reference to a group (stash "group", formerly
// "movie") associated with a scene.
type GroupRef struct {
	// ExternalID is the source's identifier for the group (e.g. the tt movie id).
	ExternalID string
	// Name is the group's title.
	Name string
	// Synopsis is the group's description, if any.
	Synopsis string
	// Date is the group's release date as "YYYY-MM-DD", or "" when unknown.
	Date string
	// URLs are the group's external URLs (including the source's own group page).
	URLs []string
	// SceneIndex is this scene's ordinal within the group, if known.
	SceneIndex *int
}

// GroupProvider is a Source that can supply group references for a scene.
type GroupProvider interface {
	// FetchGroups returns group references for the given scene identity. It
	// returns (nil, nil) when the source has no record for the scene.
	FetchGroups(ctx context.Context, id SceneIdentity) ([]GroupRef, error)
}

// FunscriptRef is a source-agnostic reference to a funscript associated with a
// scene. MD5 is the checksum of the funscript file's bytes, used to locate a
// matching locally-indexed funscript (see FunscriptIndex).
type FunscriptRef struct {
	// MD5 is the md5 checksum of the funscript file used to locate the local
	// funscript in the index.
	MD5 string
}

// FunscriptProvider is a Source that can supply funscript references for a
// scene, matched to locally-indexed funscripts by md5.
type FunscriptProvider interface {
	// FetchFunscripts returns funscript references for the given scene identity.
	// It returns (nil, nil) when the source has no record for the scene.
	FetchFunscripts(ctx context.Context, id SceneIdentity) ([]FunscriptRef, error)
}

// The submission domain model below mirrors the fetch side: it is a neutral,
// wire-format-free representation of a stash scene. JSON encoding (and any
// per-source field naming) lives exclusively in the source adapter, exactly as
// the fetch path maps an adapter's wire struct into MarkerCandidate.
//
// UNIT NOTE: SubmissionMarker.Seconds is stash-native SECONDS and is submitted
// verbatim. Unlike the fetch path (where timestamp.trade returns milliseconds
// and the adapter divides by 1000), /submit-stash consumes seconds directly, so
// no conversion is performed anywhere on the submit path.

// StashIDRef is a single stash-box identifier (endpoint + id).
type StashIDRef struct {
	Endpoint string
	StashID  string
}

// NamedEntity is a named record that may carry stash-box identifiers. It is used
// for performers and the studio.
type NamedEntity struct {
	Name     string
	StashIDs []StashIDRef
}

// SubmissionMarker is a scene marker to submit. Seconds is stash-native seconds.
type SubmissionMarker struct {
	Title      string
	Seconds    float64
	PrimaryTag string
}

// Fingerprint is a single file fingerprint (e.g. oshash, phash, md5).
type Fingerprint struct {
	Type  string
	Value string
}

// SubmissionFile describes one of the scene's files.
type SubmissionFile struct {
	Basename     string
	Duration     float64
	Size         int64
	Fingerprints []Fingerprint
}

// FunscriptHash is a submitted funscript's identity: its basename, its
// top-level metadata as raw JSON text (empty when the funscript had none), and
// its md5 checksum.
type FunscriptHash struct {
	// Filename is the funscript's basename (not the full local path).
	Filename string
	// Metadata is the funscript's top-level "metadata" object as raw JSON text,
	// or "" when the funscript had none (in which case it is omitted on the wire).
	Metadata string
	// MD5 is the md5 checksum of the funscript file's bytes.
	MD5 string
}

// SceneSubmission is the neutral representation of a stash scene submitted to a
// bidirectional source.
type SceneSubmission struct {
	Title    string
	Details  string
	Date     string
	URLs     []string
	StashIDs []StashIDRef

	Performers []NamedEntity
	Tags       []string // tag names
	Studio     *NamedEntity
	Markers    []SubmissionMarker
	Files      []SubmissionFile
	// FunscriptHashes are the scene's indexed funscripts (submitted so the source
	// can match funscripts across users by md5). Empty when submission is disabled
	// or the scene has no indexed funscripts.
	FunscriptHashes []FunscriptHash
}

// Submitter is an external database that additionally accepts scene submissions
// (bidirectional sync).
type Submitter interface {
	// Name returns a stable, human readable identifier for the submitter.
	Name() string
	// Enabled reports whether the submitter is configured and may be used.
	Enabled() bool
	// SubmitScene uploads the given scene to the external database.
	SubmitScene(ctx context.Context, scene SceneSubmission) error
}
