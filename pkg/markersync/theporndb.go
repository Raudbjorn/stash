package markersync

import (
	"context"
	"fmt"
)

// ThePornDBEndpoint is the stash-box endpoint that identifies a stash id as
// belonging to ThePornDB. A scene is matched against ThePornDB by the stash id
// whose endpoint equals this value.
const ThePornDBEndpoint = "https://theporndb.net/graphql"

// thePornDBBaseURL is the REST API base for ThePornDB.
const thePornDBBaseURL = "https://api.theporndb.net"

// thePornDBName is the stable source name.
const thePornDBName = "ThePornDB"

// ThePornDBSource fetches scene markers from ThePornDB. It is fetch-only.
// Marker times returned by the API are already in seconds and are passed
// through unchanged.
type ThePornDBSource struct {
	client      *restClient
	baseURL     string
	apiKey      string
	enabled     bool
	markerTag   string
	titlePrefix string
}

// ThePornDBOptions configures a ThePornDBSource.
type ThePornDBOptions struct {
	// APIKey is the bearer token used to authenticate. Fetching is skipped when
	// empty.
	APIKey string
	// Enabled toggles the source on or off independently of the API key.
	Enabled bool
	// MarkerTag, when set, is appended to every fetched marker's extra tags.
	MarkerTag string
	// TitlePrefix, when set, is prepended to every fetched marker's title.
	TitlePrefix string
	// RequestsPerMinute optionally rate limits requests. <= 0 disables it.
	RequestsPerMinute int
	// UserAgent overrides the default User-Agent.
	UserAgent string
	// BaseURL overrides the REST API base URL. When empty the production base
	// (thePornDBBaseURL) is used. This exists so that tests and future
	// self-hosted mirrors can point the source at an alternative host.
	BaseURL string
}

// NewThePornDBSource constructs a ThePornDBSource.
func NewThePornDBSource(opts ThePornDBOptions) *ThePornDBSource {
	baseURL := thePornDBBaseURL
	if opts.BaseURL != "" {
		baseURL = opts.BaseURL
	}
	return &ThePornDBSource{
		client:      newRestClient(opts.UserAgent, opts.RequestsPerMinute),
		baseURL:     baseURL,
		apiKey:      opts.APIKey,
		enabled:     opts.Enabled,
		markerTag:   opts.MarkerTag,
		titlePrefix: opts.TitlePrefix,
	}
}

// Name implements Source.
func (s *ThePornDBSource) Name() string { return thePornDBName }

// Enabled implements Source. It reports true only when the source is enabled
// and an API key is configured.
func (s *ThePornDBSource) Enabled() bool {
	return s.enabled && s.apiKey != ""
}

// DisabledReason returns a human readable explanation for why the source is
// disabled, for the caller to log. It returns "" when the source is enabled.
func (s *ThePornDBSource) DisabledReason() string {
	switch {
	case !s.enabled:
		return "TPDB skipped: disabled"
	case s.apiKey == "":
		return "TPDB skipped: no API key"
	default:
		return ""
	}
}

// tpdbResponse mirrors the subset of the ThePornDB scene response we consume.
type tpdbResponse struct {
	Data struct {
		Markers []struct {
			Title     string  `json:"title"`
			StartTime float64 `json:"start_time"` // seconds
		} `json:"markers"`
	} `json:"data"`
}

// FetchMarkers implements Source. It locates the scene's ThePornDB stash id,
// fetches the scene and maps its markers. It returns (nil, nil) when the scene
// has no ThePornDB stash id.
func (s *ThePornDBSource) FetchMarkers(ctx context.Context, id SceneIdentity) ([]MarkerCandidate, error) {
	stashID, ok := id.StashIDForEndpoint(ThePornDBEndpoint)
	if !ok {
		return nil, nil
	}

	url := fmt.Sprintf("%s/scenes/%s", s.baseURL, stashID)

	var resp tpdbResponse
	if err := s.client.getJSON(ctx, url, s.apiKey, &resp); err != nil {
		return nil, fmt.Errorf("theporndb: fetching scene %s: %w", stashID, err)
	}

	markers := make([]MarkerCandidate, 0, len(resp.Data.Markers))
	for _, m := range resp.Data.Markers {
		title := m.Title
		if s.titlePrefix != "" {
			title = s.titlePrefix + title
		}

		c := MarkerCandidate{
			Title:      title,
			PrimaryTag: m.Title,
			Seconds:    m.StartTime, // already seconds - pass through
		}
		if s.markerTag != "" {
			c.ExtraTags = append(c.ExtraTags, s.markerTag)
		}
		markers = append(markers, c)
	}

	if len(markers) == 0 {
		return nil, nil
	}
	return markers, nil
}

// compile-time assertion.
var _ Source = (*ThePornDBSource)(nil)
