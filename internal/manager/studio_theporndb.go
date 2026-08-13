package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
)

const (
	thePornDBStashBoxEndpoint = "https://theporndb.net/graphql"
	thePornDBSitesBaseURL     = "https://api.theporndb.net"
	maxThePornDBResponseBytes = 2 << 20
)

type thePornDBStudioClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type thePornDBSite struct {
	UUID      string         `json:"uuid"`
	Name      string         `json:"name"`
	ShortName string         `json:"short_name"`
	URL       string         `json:"url"`
	Logo      string         `json:"logo"`
	Poster    string         `json:"poster"`
	Parent    *thePornDBSite `json:"parent"`
}

type thePornDBSitesResponse struct {
	Data []thePornDBSite `json:"data"`
}

func newThePornDBStudioClient(box *models.StashBox) *thePornDBStudioClient {
	if box == nil || normalizeStashBoxEndpoint(box.Endpoint) != thePornDBStashBoxEndpoint || strings.TrimSpace(box.APIKey) == "" {
		return nil
	}
	return &thePornDBStudioClient{
		baseURL:    thePornDBSitesBaseURL,
		apiKey:     strings.TrimSpace(box.APIKey),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func studioLookupKey(value string) string {
	value = metadata.NormalizeKey(value)
	var ret strings.Builder
	ret.Grow(len(value))
	for _, current := range value {
		if unicode.IsLetter(current) || unicode.IsDigit(current) {
			ret.WriteRune(current)
		}
	}
	return ret.String()
}

func thePornDBSiteMatches(site thePornDBSite, query string) bool {
	key := studioLookupKey(query)
	return key != "" && (studioLookupKey(site.Name) == key || studioLookupKey(site.ShortName) == key)
}

func thePornDBSiteToScrapedStudio(site thePornDBSite) *models.ScrapedStudio {
	aliases := site.ShortName
	ret := &models.ScrapedStudio{
		Name:         site.Name,
		Aliases:      &aliases,
		RemoteSiteID: &site.UUID,
	}
	if strings.TrimSpace(site.URL) != "" {
		ret.URLs = []string{site.URL}
	}
	for _, image := range []string{site.Logo, site.Poster} {
		if strings.TrimSpace(image) != "" {
			ret.Images = append(ret.Images, image)
		}
	}
	if len(ret.Images) > 0 {
		ret.Image = &ret.Images[0]
	}
	if site.Parent != nil {
		ret.Parent = thePornDBSiteToScrapedStudio(*site.Parent)
	}
	return ret
}

func (c *thePornDBStudioClient) FindStudio(ctx context.Context, query string) (*models.ScrapedStudio, error) {
	if c == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	endpoint, err := url.Parse(strings.TrimRight(c.baseURL, "/") + "/sites")
	if err != nil {
		return nil, fmt.Errorf("building ThePornDB sites URL: %w", err)
	}
	params := endpoint.Query()
	params.Set("orderBy", "most_relevant")
	params.Set("page", "1")
	params.Set("q", query)
	endpoint.RawQuery = params.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("creating ThePornDB sites request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("querying ThePornDB sites: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return nil, fmt.Errorf("querying ThePornDB sites: status %d: %s", response.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var result thePornDBSitesResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxThePornDBResponseBytes))
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding ThePornDB sites: %w", err)
	}

	var match *thePornDBSite
	for index := range result.Data {
		if !thePornDBSiteMatches(result.Data[index], query) {
			continue
		}
		if match != nil {
			return nil, nil
		}
		match = &result.Data[index]
	}
	if match == nil {
		return nil, nil
	}
	return thePornDBSiteToScrapedStudio(*match), nil
}
