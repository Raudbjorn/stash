package markersync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// timestampTradeBaseURL is the REST API base for timestamp.trade.
const timestampTradeBaseURL = "https://timestamp.trade"

// timestampTradeName is the stable source name.
const timestampTradeName = "timestamp.trade"

// millisPerSecond is the conversion factor between timestamp.trade's
// millisecond timestamps and the canonical internal second unit.
const millisPerSecond = 1000.0

// TimestampTradeSource fetches (and, in a later stage, submits) scene markers
// from timestamp.trade. It requires no authentication.
//
// CRITICAL: timestamp.trade expresses marker times in MILLISECONDS. The
// millisecond<->second conversion is confined to this adapter; every value that
// leaves this type is in seconds.
type TimestampTradeSource struct {
	client  *restClient
	baseURL string
	enabled bool
}

// TimestampTradeOptions configures a TimestampTradeSource.
type TimestampTradeOptions struct {
	// Enabled toggles the source on or off.
	Enabled bool
	// RequestsPerMinute optionally rate limits requests. <= 0 disables it.
	RequestsPerMinute int
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// BaseURL overrides the REST API base URL. When empty the production base
	// (timestampTradeBaseURL) is used. This exists so that tests and future
	// self-hosted mirrors can point the source at an alternative host.
	BaseURL string
}

// NewTimestampTradeSource constructs a TimestampTradeSource.
func NewTimestampTradeSource(opts TimestampTradeOptions) *TimestampTradeSource {
	baseURL := timestampTradeBaseURL
	if opts.BaseURL != "" {
		baseURL = opts.BaseURL
	}
	return &TimestampTradeSource{
		client:  newRestClient(opts.UserAgent, opts.RequestsPerMinute),
		baseURL: baseURL,
		enabled: opts.Enabled,
	}
}

// Name implements Source.
func (s *TimestampTradeSource) Name() string { return timestampTradeName }

// Enabled implements Source.
func (s *TimestampTradeSource) Enabled() bool { return s.enabled }

// ttGetMarkersResponse is the first-step response from /get-markers/<stash_id>.
type ttGetMarkersResponse struct {
	// SceneID is timestamp.trade's internal scene id. It is decoded as a
	// json.Number so that both numeric and string encodings resolve cleanly.
	SceneID json.Number `json:"scene_id"`
}

// ttSceneResponse is the second-step response from /json-scene/<ttSceneId>. It
// carries everything the marker path and the extras providers (URLs, galleries,
// groups) need, all decoded from a single fetch.
type ttSceneResponse struct {
	// SceneID is timestamp.trade's own id for this scene, echoed back in the
	// body. It is used to locate this scene's index within each movie's scenes
	// list. Decoded as json.Number to tolerate numeric or string encodings.
	SceneID json.Number `json:"scene_id"`

	Markers []struct {
		Name      string  `json:"name"`
		TagName   string  `json:"tag_name"`
		StartTime float64 `json:"start_time"` // MILLISECONDS
	} `json:"markers"`

	// URLs are extra scene URLs.
	URLs []string `json:"urls"`

	// Funscripts are funscripts associated with the scene. Each is matched to a
	// locally-indexed funscript by its md5 checksum.
	Funscripts []struct {
		MD5 string `json:"md5"`
	} `json:"funscripts"`

	// Galleries are galleries associated with the scene. Each is matched locally
	// by its files' md5 checksums.
	Galleries []struct {
		Files []struct {
			MD5 string `json:"md5"`
		} `json:"files"`
		URLs []struct {
			URL string `json:"url"`
		} `json:"urls"`
	} `json:"galleries"`

	// Movies are groups (schema >= 64 the community plugin maps tt "movies" to
	// stash groups).
	Movies []struct {
		ID          json.Number `json:"id"`
		Title       string      `json:"title"`
		Description string      `json:"description"`
		Synopsis    string      `json:"synopsis"`
		ReleaseDate string      `json:"release_date"`
		Date        string      `json:"date"`
		URLs        []struct {
			URL string `json:"url"`
		} `json:"urls"`
		Scenes []struct {
			SceneID    json.Number `json:"scene_id"`
			SceneIndex *int        `json:"scene_index"`
		} `json:"scenes"`
	} `json:"movies"`
}

// FetchMarkers implements Source. It fetches the scene once and maps the decoded
// markers, converting milliseconds to seconds. It returns (nil, nil) when no
// stash id resolves to a timestamp.trade scene.
func (s *TimestampTradeSource) FetchMarkers(ctx context.Context, id SceneIdentity) ([]MarkerCandidate, error) {
	resp, err := s.fetchSceneData(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	markers := make([]MarkerCandidate, 0, len(resp.Markers))
	for _, m := range resp.Markers {
		primaryTag := m.TagName
		if primaryTag == "" {
			primaryTag = m.Name
		}

		markers = append(markers, MarkerCandidate{
			Title:      m.Name,
			PrimaryTag: primaryTag,
			Seconds:    m.StartTime / millisPerSecond, // ms -> seconds
		})
	}

	if len(markers) == 0 {
		return nil, nil
	}
	return markers, nil
}

// FetchExtraURLs implements ExtraURLProvider.
func (s *TimestampTradeSource) FetchExtraURLs(ctx context.Context, id SceneIdentity) ([]string, error) {
	resp, err := s.fetchSceneData(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.URLs, nil
}

// FetchGalleries implements GalleryProvider. It maps each decoded gallery to a
// GalleryRef carrying the file md5s (for local matching) and the gallery URLs.
func (s *TimestampTradeSource) FetchGalleries(ctx context.Context, id SceneIdentity) ([]GalleryRef, error) {
	resp, err := s.fetchSceneData(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	refs := make([]GalleryRef, 0, len(resp.Galleries))
	for _, g := range resp.Galleries {
		var md5s []string
		for _, f := range g.Files {
			if f.MD5 != "" {
				md5s = append(md5s, f.MD5)
			}
		}
		var urls []string
		for _, u := range g.URLs {
			if u.URL != "" {
				urls = append(urls, u.URL)
			}
		}
		if len(md5s) == 0 && len(urls) == 0 {
			continue
		}
		refs = append(refs, GalleryRef{MD5s: md5s, URLs: urls})
	}

	if len(refs) == 0 {
		return nil, nil
	}
	return refs, nil
}

// FetchGroups implements GroupProvider. It maps each decoded movie to a GroupRef,
// appending the canonical timestamp.trade movie page URL and resolving this
// scene's index from the movie's scenes list (matched by scene_id).
func (s *TimestampTradeSource) FetchGroups(ctx context.Context, id SceneIdentity) ([]GroupRef, error) {
	resp, err := s.fetchSceneData(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	refs := make([]GroupRef, 0, len(resp.Movies))
	for _, m := range resp.Movies {
		urls := make([]string, 0, len(m.URLs)+1)
		for _, u := range m.URLs {
			if u.URL != "" {
				urls = append(urls, u.URL)
			}
		}
		// Append the canonical movie page URL. This mirrors the community plugin,
		// which hardcodes the production host; it doubles as a stable identity key
		// used to match/dedupe the group on re-sync.
		urls = append(urls, fmt.Sprintf("%s/movie/%s", timestampTradeBaseURL, m.ID.String()))

		// Resolve this scene's index within the movie by matching scene ids.
		var sceneIndex *int
		for _, sc := range m.Scenes {
			if sc.SceneID.String() == resp.SceneID.String() {
				sceneIndex = sc.SceneIndex
			}
		}

		// Prefer description/release_date (plugin's keys), fall back to
		// synopsis/date.
		synopsis := m.Description
		if synopsis == "" {
			synopsis = m.Synopsis
		}
		date := m.ReleaseDate
		if date == "" {
			date = m.Date
		}

		refs = append(refs, GroupRef{
			ExternalID: m.ID.String(),
			Name:       m.Title,
			Synopsis:   synopsis,
			Date:       date,
			URLs:       urls,
			SceneIndex: sceneIndex,
		})
	}

	if len(refs) == 0 {
		return nil, nil
	}
	return refs, nil
}

// FetchFunscripts implements FunscriptProvider. It maps each decoded funscript
// to a FunscriptRef carrying the file md5 (for local matching).
func (s *TimestampTradeSource) FetchFunscripts(ctx context.Context, id SceneIdentity) ([]FunscriptRef, error) {
	resp, err := s.fetchSceneData(ctx, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}

	refs := make([]FunscriptRef, 0, len(resp.Funscripts))
	for _, f := range resp.Funscripts {
		if f.MD5 == "" {
			continue
		}
		refs = append(refs, FunscriptRef{MD5: f.MD5})
	}

	if len(refs) == 0 {
		return nil, nil
	}
	return refs, nil
}

// fetchSceneData resolves the scene's stash ids to a timestamp.trade scene and
// fetches its /json-scene record in a single round trip. It returns (nil, nil)
// when no stash id resolves to a timestamp.trade scene.
//
// NOTE: FetchMarkers and each extras provider call this independently, so a
// full-sync of one scene performs up to four resolve+GET round trips against
// timestamp.trade. This keeps each capability self-contained; a per-scene cache
// can be layered on later if the request volume warrants it.
func (s *TimestampTradeSource) fetchSceneData(ctx context.Context, id SceneIdentity) (*ttSceneResponse, error) {
	for _, sid := range id.StashIDs {
		ttSceneID, err := s.resolveSceneID(ctx, sid.StashID)
		if err != nil {
			if isNotFound(err) {
				// This stash id is genuinely absent from timestamp.trade; try
				// the next one.
				continue
			}
			// A real failure (network, timeout, 5xx, ...) must not be silently
			// reported as "scene has no markers".
			return nil, fmt.Errorf("timestamp.trade: resolving stash id %s: %w", sid.StashID, err)
		}
		if ttSceneID == "" {
			continue
		}

		resp, err := s.fetchScene(ctx, ttSceneID)
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		return resp, nil
	}

	// No stash id resolved to a timestamp.trade scene.
	return nil, nil
}

// isNotFound reports whether err is an HTTP 404 status error, i.e. an explicit
// "no such record" rather than a transport or server failure.
func isNotFound(err error) bool {
	var se *HTTPStatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}

// resolveSceneID performs step one: stash id -> timestamp.trade scene id.
func (s *TimestampTradeSource) resolveSceneID(ctx context.Context, stashID string) (string, error) {
	url := fmt.Sprintf("%s/get-markers/%s", s.baseURL, stashID)

	var resp ttGetMarkersResponse
	if err := s.client.getJSON(ctx, url, "", &resp); err != nil {
		return "", err
	}

	sceneID := resp.SceneID.String()
	if sceneID == "" || sceneID == "0" {
		return "", nil
	}
	return sceneID, nil
}

// fetchScene performs step two: timestamp.trade scene id -> decoded scene
// record. The single decoded response feeds both the marker path and every
// extras provider.
func (s *TimestampTradeSource) fetchScene(ctx context.Context, ttSceneID string) (*ttSceneResponse, error) {
	url := fmt.Sprintf("%s/json-scene/%s", s.baseURL, ttSceneID)

	var resp ttSceneResponse
	if err := s.client.getJSON(ctx, url, "", &resp); err != nil {
		return nil, fmt.Errorf("timestamp.trade: fetching scene %s: %w", ttSceneID, err)
	}
	return &resp, nil
}

// The wire structs below mirror the GraphQL scene fragment that the community
// timestamp.trade plugin posts to /submit-stash (schema >= 64). Field names and
// nesting must match EXACTLY. Marker times are stash-native SECONDS on submit
// (the plugin performs NO conversion), unlike the fetch path which receives
// milliseconds.

// ttWireStashID is a stash-box identifier as timestamp.trade expects it.
type ttWireStashID struct {
	Endpoint string `json:"endpoint"`
	StashID  string `json:"stash_id"`
}

// ttWireTag is a bare tag name wrapper (scene tags).
type ttWireTag struct {
	Name string `json:"name"`
}

// ttWireTagName is a primary-tag wrapper on a marker.
type ttWireTagName struct {
	Name string `json:"name"`
}

// ttWireNamed is a named entity with optional stash ids (performers, studio).
type ttWireNamed struct {
	Name     string          `json:"name"`
	StashIDs []ttWireStashID `json:"stash_ids"`
}

// ttWireMarker is a scene marker. Seconds is stash-native (NOT milliseconds).
type ttWireMarker struct {
	Title      string        `json:"title"`
	Seconds    float64       `json:"seconds"`
	PrimaryTag ttWireTagName `json:"primary_tag"`
}

// ttWireFP is a file fingerprint.
type ttWireFP struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ttWireFile is one of the scene's files.
type ttWireFile struct {
	Basename     string     `json:"basename"`
	Duration     float64    `json:"duration"`
	Size         int64      `json:"size"`
	Fingerprints []ttWireFP `json:"fingerprints"`
}

// ttWireFunscript is a submitted funscript hash. Metadata is the funscript's raw
// top-level metadata JSON; it is omitted when empty (null on the wire). The json
// tag MUST be exactly "funscriptHashes" on the enclosing field to match the
// community plugin's submit payload.
type ttWireFunscript struct {
	Filename string          `json:"filename"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
	MD5      string          `json:"md5"`
}

// ttWireScene is the full body posted to /submit-stash. It matches the plugin's
// GraphQL scene fragment. funscriptHashes is omitted for scenes with no indexed
// funscripts; the server tolerates its absence.
type ttWireScene struct {
	Title           string            `json:"title"`
	Details         string            `json:"details"`
	URLs            []string          `json:"urls"`
	Date            string            `json:"date"`
	Performers      []ttWireNamed     `json:"performers"`
	Tags            []ttWireTag       `json:"tags"`
	Studio          *ttWireNamed      `json:"studio,omitempty"`
	StashIDs        []ttWireStashID   `json:"stash_ids"`
	SceneMarkers    []ttWireMarker    `json:"scene_markers"`
	Files           []ttWireFile      `json:"files"`
	FunscriptHashes []ttWireFunscript `json:"funscriptHashes,omitempty"`
}

// toWireStashIDs maps neutral stash id refs to the wire representation.
func toWireStashIDs(refs []StashIDRef) []ttWireStashID {
	out := make([]ttWireStashID, 0, len(refs))
	for _, r := range refs {
		out = append(out, ttWireStashID{Endpoint: r.Endpoint, StashID: r.StashID})
	}
	return out
}

// buildSubmitScene maps the neutral SceneSubmission into the timestamp.trade
// wire scene. Marker seconds are passed through UNCHANGED (seconds -> seconds).
func buildSubmitScene(scene SceneSubmission) ttWireScene {
	wire := ttWireScene{
		Title:        scene.Title,
		Details:      scene.Details,
		URLs:         scene.URLs,
		Date:         scene.Date,
		Performers:   make([]ttWireNamed, 0, len(scene.Performers)),
		Tags:         make([]ttWireTag, 0, len(scene.Tags)),
		StashIDs:     toWireStashIDs(scene.StashIDs),
		SceneMarkers: make([]ttWireMarker, 0, len(scene.Markers)),
		Files:        make([]ttWireFile, 0, len(scene.Files)),
	}

	for _, p := range scene.Performers {
		wire.Performers = append(wire.Performers, ttWireNamed{
			Name:     p.Name,
			StashIDs: toWireStashIDs(p.StashIDs),
		})
	}

	for _, t := range scene.Tags {
		wire.Tags = append(wire.Tags, ttWireTag{Name: t})
	}

	if scene.Studio != nil {
		wire.Studio = &ttWireNamed{
			Name:     scene.Studio.Name,
			StashIDs: toWireStashIDs(scene.Studio.StashIDs),
		}
	}

	for _, m := range scene.Markers {
		wire.SceneMarkers = append(wire.SceneMarkers, ttWireMarker{
			Title:      m.Title,
			Seconds:    m.Seconds, // stash-native seconds, NO conversion
			PrimaryTag: ttWireTagName{Name: m.PrimaryTag},
		})
	}

	for _, f := range scene.Files {
		fps := make([]ttWireFP, 0, len(f.Fingerprints))
		for _, fp := range f.Fingerprints {
			fps = append(fps, ttWireFP{Type: fp.Type, Value: fp.Value})
		}
		wire.Files = append(wire.Files, ttWireFile{
			Basename:     f.Basename,
			Duration:     f.Duration,
			Size:         f.Size,
			Fingerprints: fps,
		})
	}

	// funscriptHashes: only populate when present so scenes without funscripts
	// omit the field entirely (omitempty on a nil slice). Empty metadata is left
	// as a nil RawMessage so it is omitted (null) on the wire; a non-empty
	// metadata string is passed through as raw JSON unchanged.
	if len(scene.FunscriptHashes) > 0 {
		wire.FunscriptHashes = make([]ttWireFunscript, 0, len(scene.FunscriptHashes))
		for _, fh := range scene.FunscriptHashes {
			var meta json.RawMessage
			if fh.Metadata != "" {
				meta = json.RawMessage(fh.Metadata)
			}
			wire.FunscriptHashes = append(wire.FunscriptHashes, ttWireFunscript{
				Filename: fh.Filename,
				Metadata: meta,
				MD5:      fh.MD5,
			})
		}
	}

	return wire
}

// SubmitScene implements Submitter. It POSTs the entire scene to
// /submit-stash. timestamp.trade requires no authentication (empty bearer) and
// returns a non-JSON/empty body on success, so the response is discarded.
func (s *TimestampTradeSource) SubmitScene(ctx context.Context, scene SceneSubmission) error {
	url := fmt.Sprintf("%s/submit-stash", s.baseURL)
	wire := buildSubmitScene(scene)

	// out == nil discards (and drains) the response body without attempting a
	// JSON decode, which is what /submit-stash needs.
	if err := s.client.postJSON(ctx, url, "", wire, nil); err != nil {
		return fmt.Errorf("timestamp.trade: submitting scene: %w", err)
	}
	return nil
}

// compile-time assertions.
var (
	_ Source            = (*TimestampTradeSource)(nil)
	_ Submitter         = (*TimestampTradeSource)(nil)
	_ ExtraURLProvider  = (*TimestampTradeSource)(nil)
	_ GalleryProvider   = (*TimestampTradeSource)(nil)
	_ GroupProvider     = (*TimestampTradeSource)(nil)
	_ FunscriptProvider = (*TimestampTradeSource)(nil)
)
