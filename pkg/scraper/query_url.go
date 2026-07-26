package scraper

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/stashapp/stash/pkg/models"
)

type queryURLReplacements map[string]mappedRegexConfigs

type queryURLParameters map[string]string

var queryURLPlaceholderRE = regexp.MustCompile(`\{([^}]+)\}`)

func queryURLParametersFromScene(scene *models.Scene) queryURLParameters {
	ret := make(queryURLParameters)

	if scene.Checksum != "" {
		ret["checksum"] = scene.Checksum
	}
	if scene.OSHash != "" {
		ret["oshash"] = scene.OSHash
	}
	if scene.Path != "" {
		ret["filename"] = filepath.Base(scene.Path)
	}

	// pull phash from primary file
	if primaryFile := scene.Files.Primary(); primaryFile != nil {
		phashFingerprints := primaryFile.Base().Fingerprints.Filter(models.FingerprintTypePhash)
		if len(phashFingerprints) > 0 {
			ret["phash"] = phashFingerprints[0].Value()
		}
	}

	if scene.Title != "" {
		ret["title"] = scene.Title
	}
	if len(scene.URLs.List()) > 0 {
		ret["url"] = scene.URLs.List()[0]
	}
	return ret
}

func urlCandidateParams(base queryURLParameters, urls []string, queryURL string, ty ScrapeContentType, definition Definition) ([]queryURLParameters, error) {
	if !strings.Contains(queryURL, "{url}") {
		return []queryURLParameters{base}, nil
	}

	if len(urls) == 0 {
		return []queryURLParameters{base}, nil
	}

	var ret []queryURLParameters
	for _, u := range urls {
		if definition.matchesURL(u, ty) {
			candidate := base.clone()
			candidate["url"] = u
			ret = append(ret, candidate)
		}
	}

	if len(ret) == 0 && definition.hasURLScrapers(ty) {
		return nil, fmt.Errorf("no %s URLs match this scraper", strings.ToLower(string(ty)))
	}

	if len(ret) == 0 {
		for _, u := range urls {
			candidate := base.clone()
			candidate["url"] = u
			ret = append(ret, candidate)
		}
	}

	return ret, nil
}

func queryURLParameterCandidatesFromScene(scene *models.Scene, queryURL string, ty ScrapeContentType, definition Definition) ([]queryURLParameters, error) {
	base := queryURLParametersFromScene(scene)
	return urlCandidateParams(base, scene.URLs.List(), queryURL, ty, definition)
}

func queryURLParameterCandidatesFromGallery(gallery *models.Gallery, queryURL string, ty ScrapeContentType, definition Definition) ([]queryURLParameters, error) {
	base := queryURLParametersFromGallery(gallery)
	return urlCandidateParams(base, gallery.URLs.List(), queryURL, ty, definition)
}

func queryURLParameterCandidatesFromImage(image *models.Image, queryURL string, ty ScrapeContentType, definition Definition) ([]queryURLParameters, error) {
	base := queryURLParametersFromImage(image)
	return urlCandidateParams(base, image.URLs.List(), queryURL, ty, definition)
}

func queryURLParametersFromScrapedScene(scene models.ScrapedSceneInput) queryURLParameters {
	ret := make(queryURLParameters)

	setField := func(field string, value *string) {
		if value != nil {
			ret[field] = *value
		}
	}

	setField("title", scene.Title)
	setField("code", scene.Code)
	if len(scene.URLs) > 0 {
		setField("url", &scene.URLs[0])
	} else {
		setField("url", scene.URL)
	}
	setField("date", scene.Date)
	setField("details", scene.Details)
	setField("director", scene.Director)
	setField("remote_site_id", scene.RemoteSiteID)
	return ret
}

func queryURLParameterFromURL(url string) queryURLParameters {
	ret := make(queryURLParameters)
	ret["url"] = url
	return ret
}

func queryURLParametersFromGallery(gallery *models.Gallery) queryURLParameters {
	ret := make(queryURLParameters)

	if checksum := gallery.PrimaryChecksum(); checksum != "" {
		ret["checksum"] = checksum
	}

	if gallery.Path != "" {
		ret["filename"] = filepath.Base(gallery.Path)
	}
	if gallery.Title != "" {
		ret["title"] = gallery.Title
	}

	if len(gallery.URLs.List()) > 0 {
		ret["url"] = gallery.URLs.List()[0]
	}

	return ret
}

func queryURLParametersFromImage(image *models.Image) queryURLParameters {
	ret := make(queryURLParameters)

	if image.Checksum != "" {
		ret["checksum"] = image.Checksum
	}

	if image.Path != "" {
		ret["filename"] = filepath.Base(image.Path)
	}
	if image.Title != "" {
		ret["title"] = image.Title
	}

	if len(image.URLs.List()) > 0 {
		ret["url"] = image.URLs.List()[0]
	}

	return ret
}

func (p queryURLParameters) applyReplacements(r queryURLReplacements) {
	for k, v := range p {
		rpl, found := r[k]
		if found {
			p[k] = rpl.apply(v)
		}
	}
}

func (p queryURLParameters) clone() queryURLParameters {
	ret := make(queryURLParameters, len(p))
	for k, v := range p {
		ret[k] = v
	}
	return ret
}

func (p queryURLParameters) constructURL(url string) string {
	ret := url
	for k, v := range p {
		ret = strings.ReplaceAll(ret, "{"+k+"}", v)
	}

	return ret
}

func (p queryURLParameters) missingFields(url string) []string {
	matches := queryURLPlaceholderRE.FindAllStringSubmatch(url, -1)
	seen := make(map[string]struct{})
	for _, match := range matches {
		if v, ok := p[match[1]]; !ok || v == "" {
			seen[match[1]] = struct{}{}
		}
	}

	ret := make([]string, 0, len(seen))
	for field := range seen {
		ret = append(ret, field)
	}
	sort.Strings(ret)
	return ret
}

func (p queryURLParameters) constructURLOrError(url string) (string, error) {
	missingFields := p.missingFields(url)
	if len(missingFields) > 0 {
		return "", fmt.Errorf("missing fields for queryURL: %s", strings.Join(missingFields, ", "))
	}

	return p.constructURL(url), nil
}

// replaceURL does a partial URL Replace ( only url parameter is used)
func replaceURL(url string, scraperConfig ByURLDefinition) string {
	u := url
	queryURL := queryURLParameterFromURL(u)
	if scraperConfig.QueryURLReplacements != nil {
		queryURL.applyReplacements(scraperConfig.QueryURLReplacements)
		u = queryURL.constructURL(scraperConfig.QueryURL)
	}
	return u
}
