package stashbox

import (
	"context"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/stashbox/graphql"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

func (c Client) resolveStudio(ctx context.Context, s *graphql.StudioFragment) (*models.ScrapedStudio, error) {
	scraped := studioFragmentToScrapedStudio(*s)

	if s.Parent != nil {
		parentStudio, err := c.client.FindStudio(ctx, &s.Parent.ID, nil)
		if err != nil {
			return nil, err
		}

		if parentStudio.FindStudio == nil {
			return scraped, nil
		}

		scraped.Parent, err = c.resolveStudio(ctx, parentStudio.FindStudio)
		if err != nil {
			return nil, err
		}
	}

	return scraped, nil
}

func (c Client) queryStudioByURL(ctx context.Context, rawURL string) (*models.ScrapedStudio, error) {
	result, err := c.client.QueryStudios(ctx, graphql.StudioQueryInput{
		URL:       &rawURL,
		Page:      1,
		PerPage:   2,
		Direction: graphql.SortDirectionEnumAsc,
		Sort:      graphql.StudioSortEnumName,
	})
	if err != nil {
		return nil, err
	}
	studios := result.QueryStudios.Studios
	if len(studios) != 1 {
		return nil, nil
	}
	return c.resolveStudio(ctx, studios[0])
}

// FindStudioByURL resolves an exact Stash-box studio URL. Stash-box stores
// URLs literally, so callers must try the URL variants they consider
// equivalent.
func (c Client) FindStudioByURL(ctx context.Context, rawURL string) (*models.ScrapedStudio, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, nil
	}
	return c.queryStudioByURL(ctx, rawURL)
}

func studioNameKey(value string) string {
	value = cases.Fold().String(norm.NFKC.String(value))
	var ret strings.Builder
	ret.Grow(len(value))
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			ret.WriteRune(r)
		}
	}
	return ret.String()
}

func studioNameQueryPattern(value string) string {
	value = strings.TrimSpace(norm.NFKC.String(value))
	var ret strings.Builder
	ret.Grow(len(value) * 2)
	for _, current := range value {
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) {
			continue
		}
		if ret.Len() > 0 {
			ret.WriteByte('%')
		}
		ret.WriteRune(current)
	}
	return ret.String()
}

func studioFragmentMatchesName(studio *graphql.StudioFragment, query string) bool {
	if studio == nil {
		return false
	}
	key := studioNameKey(query)
	if key == "" {
		return false
	}
	if studioNameKey(studio.Name) == key {
		return true
	}
	for _, alias := range studio.Aliases {
		if studioNameKey(alias) == key {
			return true
		}
	}
	return false
}

func (c Client) findStudioByFlexibleName(ctx context.Context, query string) (*models.ScrapedStudio, error) {
	pattern := studioNameQueryPattern(query)
	if pattern == "" {
		return nil, nil
	}
	result, err := c.client.QueryStudios(ctx, graphql.StudioQueryInput{
		Names:     &pattern,
		Page:      1,
		PerPage:   50,
		Direction: graphql.SortDirectionEnumAsc,
		Sort:      graphql.StudioSortEnumName,
	})
	if err != nil {
		return nil, err
	}
	var match *graphql.StudioFragment
	for _, studio := range result.QueryStudios.Studios {
		if !studioFragmentMatchesName(studio, query) {
			continue
		}
		if match != nil {
			return nil, nil
		}
		match = studio
	}
	if match == nil {
		return nil, nil
	}
	return c.resolveStudio(ctx, match)
}

func (c Client) FindStudio(ctx context.Context, query string) (*models.ScrapedStudio, error) {
	if _, err := uuid.Parse(query); err == nil {
		studio, err := c.client.FindStudio(ctx, &query, nil)
		if err != nil || studio.FindStudio == nil {
			return nil, err
		}
		return c.resolveStudio(ctx, studio.FindStudio)
	}

	studio, err := c.client.FindStudio(ctx, nil, &query)
	if err != nil {
		return nil, err
	}
	if studio.FindStudio != nil {
		return c.resolveStudio(ctx, studio.FindStudio)
	}
	return c.findStudioByFlexibleName(ctx, query)
}

func studioFragmentToScrapedStudio(s graphql.StudioFragment) *models.ScrapedStudio {
	images := []string{}
	for _, image := range s.Images {
		images = append(images, image.URL)
	}

	aliases := strings.Join(s.Aliases, ", ")

	st := &models.ScrapedStudio{
		Name:         s.Name,
		Aliases:      &aliases,
		Images:       images,
		RemoteSiteID: &s.ID,
	}

	for _, u := range s.Urls {
		st.URLs = append(st.URLs, u.URL)
	}

	if len(st.Images) > 0 {
		st.Image = &st.Images[0]
	}

	return st
}
