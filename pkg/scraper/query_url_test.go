package scraper

import (
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func makeQueryURLScene(urls ...string) *models.Scene {
	return &models.Scene{
		Files: models.NewRelatedVideoFiles(nil),
		URLs:  models.NewRelatedStrings(urls),
	}
}

func TestConstructURLOrErrorReportsMissingFields(t *testing.T) {
	params := queryURLParameters{"title": "Example"}

	_, err := params.constructURLOrError("https://example.test/{title}/{url}/{checksum}/{url}")
	if err == nil {
		t.Fatal("expected missing fields error")
	}

	want := "missing fields for queryURL: checksum, url"
	if err.Error() != want {
		t.Fatalf("expected %q, got %q", want, err.Error())
	}
}

func TestQueryURLParameterCandidatesFromSceneFiltersMatchingURLs(t *testing.T) {
	scene := makeQueryURLScene("https://example.com/update/123", "https://studiox.com/update/456")
	definition := Definition{
		SceneByURL: []*ByURLDefinition{{URL: []string{"studiox.com/update"}}},
	}

	candidates, err := queryURLParameterCandidatesFromScene(scene, "{url}", ScrapeContentTypeScene, definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if got := candidates[0]["url"]; got != "https://studiox.com/update/456" {
		t.Fatalf("expected matching URL, got %q", got)
	}
}

func TestQueryURLParameterCandidatesFromSceneErrorsWhenNoURLsMatch(t *testing.T) {
	scene := makeQueryURLScene("https://example.com/update/123")
	definition := Definition{
		SceneByURL: []*ByURLDefinition{{URL: []string{"studiox.com/update"}}},
	}

	_, err := queryURLParameterCandidatesFromScene(scene, "{url}", ScrapeContentTypeScene, definition)
	if err == nil {
		t.Fatal("expected error")
	}

	want := "no scene URLs match this scraper"
	if err.Error() != want {
		t.Fatalf("expected %q, got %q", want, err.Error())
	}
}

func TestQueryURLParameterCandidatesFromSceneUsesAllURLsWithoutURLScrapers(t *testing.T) {
	scene := makeQueryURLScene("https://example.com/1", "https://example.com/2")

	candidates, err := queryURLParameterCandidatesFromScene(scene, "{url}", ScrapeContentTypeScene, Definition{})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, candidate := range candidates {
		got = append(got, candidate["url"])
	}
	want := []string{"https://example.com/1", "https://example.com/2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestQueryURLParameterCandidatesFromGalleryFiltersMatchingURLs(t *testing.T) {
	gallery := &models.Gallery{
		Files: models.NewRelatedFiles(nil),
		URLs:  models.NewRelatedStrings([]string{"https://example.com/1", "https://studiox.com/update/456"}),
	}
	definition := Definition{
		GalleryByURL: []*ByURLDefinition{{URL: []string{"studiox.com/update"}}},
	}

	candidates, err := queryURLParameterCandidatesFromGallery(gallery, "{url}", ScrapeContentTypeGallery, definition)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if got := candidates[0]["url"]; got != "https://studiox.com/update/456" {
		t.Fatalf("expected matching URL, got %q", got)
	}
}

func TestQueryURLParameterCandidatesFromImageReturnsAllURLs(t *testing.T) {
	image := &models.Image{
		Files: models.NewRelatedFiles(nil),
		URLs:  models.NewRelatedStrings([]string{"https://example.com/1", "https://example.com/2"}),
	}

	candidates, err := queryURLParameterCandidatesFromImage(image, "{url}", ScrapeContentTypeImage, Definition{})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, candidate := range candidates {
		got = append(got, candidate["url"])
	}
	want := []string{"https://example.com/1", "https://example.com/2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestQueryURLParametersFromSceneFilelessReportsMissingFields(t *testing.T) {
	scene := &models.Scene{
		Files: models.NewRelatedVideoFiles(nil),
		URLs:  models.NewRelatedStrings([]string{}),
	}
	params := queryURLParametersFromScene(scene)

	_, err := params.constructURLOrError("https://example.test/{checksum}/{filename}/{oshash}")
	if err == nil {
		t.Fatal("expected missing fields error for fileless scene")
	}

	want := "missing fields for queryURL: checksum, filename, oshash"
	if err.Error() != want {
		t.Fatalf("expected %q, got %q", want, err.Error())
	}
}

func TestQueryURLParametersFromGalleryFilelessReportsMissingFields(t *testing.T) {
	gallery := &models.Gallery{
		Files: models.NewRelatedFiles(nil),
		URLs:  models.NewRelatedStrings([]string{}),
	}
	params := queryURLParametersFromGallery(gallery)

	_, err := params.constructURLOrError("https://example.test/{checksum}")
	if err == nil {
		t.Fatal("expected missing fields error for fileless gallery")
	}
}

func TestMissingFieldsDeduplicates(t *testing.T) {
	params := queryURLParameters{"title": "Example"}

	missing := params.missingFields("https://example.test/{url}/{url}/{checksum}")
	want := []string{"checksum", "url"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("expected %v, got %v", want, missing)
	}
}

func TestMissingFieldsEmptyURL(t *testing.T) {
	params := queryURLParameters{}
	missing := params.missingFields("")
	if len(missing) != 0 {
		t.Fatalf("expected no missing fields for empty url, got %v", missing)
	}
}

// TestMissingFieldsReportsEmptyValueAsMissing verifies that a parameter
// present in the map but set to an empty string is treated as missing.
// This guards against malformed URLs when a value is emptied via
// applyReplacements or other map manipulation before validation.
func TestMissingFieldsReportsEmptyValueAsMissing(t *testing.T) {
	params := queryURLParameters{"checksum": ""}

	missing := params.missingFields("https://example.test/{checksum}")
	want := []string{"checksum"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("expected %v, got %v", want, missing)
	}

	// constructURLOrError must also reject the empty value
	_, err := params.constructURLOrError("https://example.test/{checksum}")
	if err == nil {
		t.Fatal("expected missing fields error for empty checksum value")
	}
	wantErr := "missing fields for queryURL: checksum"
	if err.Error() != wantErr {
		t.Fatalf("expected %q, got %q", wantErr, err.Error())
	}
}

// TestMissingFieldsAfterReplacementReportsEmptyValueAsMissing verifies
// that a non-empty parameter value reduced to "" by applyReplacements is
// caught as missing by missingFields and rejected by constructURLOrError.
func TestMissingFieldsAfterReplacementReportsEmptyValueAsMissing(t *testing.T) {
	params := queryURLParameters{"checksum": "abc123"}
	replacements := queryURLReplacements{
		"checksum": mappedRegexConfigs{{Regex: ".*", With: ""}},
	}
	params.applyReplacements(replacements)

	if got := params["checksum"]; got != "" {
		t.Fatalf("expected replacement to empty checksum, got %q", got)
	}

	missing := params.missingFields("https://example.test/{checksum}")
	want := []string{"checksum"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("expected %v, got %v", want, missing)
	}

	_, err := params.constructURLOrError("https://example.test/{checksum}")
	if err == nil {
		t.Fatal("expected missing fields error after replacement emptied checksum")
	}
	wantErr := "missing fields for queryURL: checksum"
	if err.Error() != wantErr {
		t.Fatalf("expected %q, got %q", wantErr, err.Error())
	}
}

func TestHasURLScrapers(t *testing.T) {
	tests := []struct {
		name     string
		def      Definition
		ty       ScrapeContentType
		expected bool
	}{
		{"scene with url scrapers", Definition{SceneByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeScene, true},
		{"scene without url scrapers", Definition{}, ScrapeContentTypeScene, false},
		{"gallery with url scrapers", Definition{GalleryByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeGallery, true},
		{"gallery without url scrapers", Definition{}, ScrapeContentTypeGallery, false},
		{"image with url scrapers", Definition{ImageByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeImage, true},
		{"image without url scrapers", Definition{}, ScrapeContentTypeImage, false},
		{"performer with url scrapers", Definition{PerformerByURL: []*ByURLDefinition{{}}}, ScrapeContentTypePerformer, true},
		{"performer without url scrapers", Definition{}, ScrapeContentTypePerformer, false},
		{"movie with url scrapers", Definition{MovieByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeMovie, true},
		{"group with url scrapers", Definition{GroupByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeGroup, true},
		{"group with movie scrapers", Definition{MovieByURL: []*ByURLDefinition{{}}}, ScrapeContentTypeGroup, true},
		{"group without url scrapers", Definition{}, ScrapeContentTypeGroup, false},
		{"unknown type", Definition{}, ScrapeContentType("unknown"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.def.hasURLScrapers(tt.ty); got != tt.expected {
				t.Fatalf("hasURLScrapers(%v) = %v, want %v", tt.ty, got, tt.expected)
			}
		})
	}
}

func TestQueryURLParametersFromImageFilelessReportsMissingFields(t *testing.T) {
	image := &models.Image{
		Files: models.NewRelatedFiles(nil),
		URLs:  models.NewRelatedStrings([]string{}),
	}
	params := queryURLParametersFromImage(image)

	_, err := params.constructURLOrError("https://example.test/{checksum}")
	if err == nil {
		t.Fatal("expected missing fields error for fileless image")
	}
}
