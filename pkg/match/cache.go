package match

import (
	"context"
	"regexp"
	"sync"

	"github.com/stashapp/stash/pkg/models"
)

const singleFirstCharacterRegex = `^[\p{L}][.\-_ ]`

// Cache is used to cache queries that should not change across an autotag process.
type Cache struct {
	singleCharPerformers []*models.Performer
	singleCharStudios    []*models.Studio
	singleCharTags       []*models.Tag

	// regexpCache memoises compiled name regexps across the autotag run. The
	// same performer/studio/tag name is otherwise recompiled for every path
	// examined. Keyed by name + useUnicode; safe for concurrent use.
	regexpCache sync.Map
}

// nameToRegexp returns a compiled regexp for the given name, caching the result.
// It is safe for concurrent use. A nil receiver compiles without caching.
func (c *Cache) nameToRegexp(name string, useUnicode bool) *regexp.Regexp {
	if c == nil {
		return nameToRegexp(name, useUnicode)
	}

	key := name
	if useUnicode {
		// distinguish the unicode and non-unicode compilations
		key = "\x00u\x00" + name
	}

	if v, ok := c.regexpCache.Load(key); ok {
		return v.(*regexp.Regexp)
	}

	re := nameToRegexp(name, useUnicode)
	c.regexpCache.Store(key, re)
	return re
}

// Warm pre-populates the single-letter query caches. These fields are guarded
// only by a nil check, so populating them lazily from multiple goroutines would
// race. Calling Warm once, serially, before a parallel autotag run ensures the
// concurrent matching phase only ever reads them. Readers may be nil to skip
// that category.
func (c *Cache) Warm(ctx context.Context,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer) error {

	if performerReader != nil {
		if _, err := getSingleLetterPerformers(ctx, c, performerReader); err != nil {
			return err
		}
	}
	if studioReader != nil {
		if _, err := getSingleLetterStudios(ctx, c, studioReader); err != nil {
			return err
		}
	}
	if tagReader != nil {
		if _, err := getSingleLetterTags(ctx, c, tagReader); err != nil {
			return err
		}
	}
	return nil
}

// getSingleLetterPerformers returns all performers with names that start with single character words.
// The autotag query splits the words into two-character words to query
// against. This means that performers with single-letter words in their names could potentially
// be missed.
// This query is expensive, so it's queried once and cached, if the cache if provided.
func getSingleLetterPerformers(ctx context.Context, c *Cache, reader models.PerformerAutoTagQueryer) ([]*models.Performer, error) {
	if c == nil {
		c = &Cache{}
	}

	if c.singleCharPerformers == nil {
		pp := -1
		performers, _, err := reader.Query(ctx, &models.PerformerFilterType{
			Name: &models.StringCriterionInput{
				Value:    singleFirstCharacterRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
		}, &models.FindFilterType{
			PerPage: &pp,
		})

		if err != nil {
			return nil, err
		}

		if len(performers) == 0 {
			// make singleWordPerformers not nil
			c.singleCharPerformers = make([]*models.Performer, 0)
		} else {
			c.singleCharPerformers = performers
		}
	}

	return c.singleCharPerformers, nil
}

// getSingleLetterStudios returns all studios with names that start with single character words.
// See getSingleLetterPerformers for details.
func getSingleLetterStudios(ctx context.Context, c *Cache, reader models.StudioAutoTagQueryer) ([]*models.Studio, error) {
	if c == nil {
		c = &Cache{}
	}

	if c.singleCharStudios == nil {
		pp := -1
		studios, _, err := reader.Query(ctx, &models.StudioFilterType{
			Name: &models.StringCriterionInput{
				Value:    singleFirstCharacterRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
		}, &models.FindFilterType{
			PerPage: &pp,
		})

		if err != nil {
			return nil, err
		}

		if len(studios) == 0 {
			// make singleWordStudios not nil
			c.singleCharStudios = make([]*models.Studio, 0)
		} else {
			c.singleCharStudios = studios
		}
	}

	return c.singleCharStudios, nil
}

// getSingleLetterTags returns all tags with names that start with single character words.
// See getSingleLetterPerformers for details.
func getSingleLetterTags(ctx context.Context, c *Cache, reader models.TagAutoTagQueryer) ([]*models.Tag, error) {
	if c == nil {
		c = &Cache{}
	}

	if c.singleCharTags == nil {
		pp := -1
		tags, _, err := reader.Query(ctx, &models.TagFilterType{
			Name: &models.StringCriterionInput{
				Value:    singleFirstCharacterRegex,
				Modifier: models.CriterionModifierMatchesRegex,
			},
			OperatorFilter: models.OperatorFilter[models.TagFilterType]{
				Or: &models.TagFilterType{
					Aliases: &models.StringCriterionInput{
						Value:    singleFirstCharacterRegex,
						Modifier: models.CriterionModifierMatchesRegex,
					},
				},
			},
		}, &models.FindFilterType{
			PerPage: &pp,
		})

		if err != nil {
			return nil, err
		}

		if len(tags) == 0 {
			// make singleWordTags not nil
			c.singleCharTags = make([]*models.Tag, 0)
		} else {
			c.singleCharTags = tags
		}
	}

	return c.singleCharTags, nil
}
