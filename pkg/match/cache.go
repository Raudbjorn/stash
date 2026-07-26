package match

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/stashapp/stash/pkg/models"
)

const singleFirstCharacterRegex = `^[\p{L}][.\-_ ]`

var singleFirstCharacterRE = regexp.MustCompile(singleFirstCharacterRegex)

// firstTwoRunesLower returns the first two runes of s, lowercased. Returns
// "" if s has fewer than two runes. Mirrors what getPathWords produces for
// path words, so the two can be compared as index keys.
func firstTwoRunesLower(s string) string {
	lower := strings.ToLower(s)
	runes := []rune(lower)
	if len(runes) < 2 {
		return ""
	}
	return string(runes[0:2])
}

// Cache is used to cache queries that should not change across an autotag
// process. Safe for concurrent use by multiple goroutines once preloaded.
type Cache struct {
	singleCharPerformers []*models.Performer
	singleCharStudios    []*models.Studio
	singleCharTags       []*models.Tag

	// Preloaded candidate sets. When populated (via PreloadX), the
	// PathTo* functions skip the per-path QueryForAutoTag DB roundtrip
	// and consult the in-memory prefix index instead. Nil means
	// "not preloaded, fall back to the per-call SQL-prefilter path".
	allPerformers []*models.Performer
	allStudios    []cachedStudio
	allTags       []cachedTag

	// Prefix indexes built at preload time. Map key is the first two
	// lowercased runes of name (or alias, for studios/tags). The
	// alwaysCheck slice holds entries whose first "word" is a single
	// letter — they wouldn't be reached by 2-rune path word lookup, so
	// they must always be checked (mirroring the single-letter query).
	performerByPrefix    map[string][]*models.Performer
	performerAlwaysCheck []*models.Performer
	studioByPrefix       map[string][]cachedStudio
	studioAlwaysCheck    []cachedStudio
	tagByPrefix          map[string][]cachedTag
	tagAlwaysCheck       []cachedTag

	// regexpCache memoises compiled name regexps across the autotag run. The
	// same performer/studio/tag name is otherwise recompiled for every path
	// examined. Keyed by name + useUnicode; safe for concurrent use.
	regexpCache sync.Map
}

// cachedStudio bundles a studio with its aliases so PathToStudio can match
// against both without an N+1 GetAliases query.
type cachedStudio struct {
	Studio  *models.Studio
	Aliases []string
}

// cachedTag bundles a tag with its aliases so PathToTags can match against
// both without an N+1 GetAliases query.
type cachedTag struct {
	Tag     *models.Tag
	Aliases []string
}

type regexpCacheKey struct {
	name       string
	useUnicode bool
}

// nameRegexp returns a compiled regexp for the given name, caching the result.
// It is safe for concurrent use. A nil receiver compiles without caching.
func (c *Cache) nameRegexp(name string, useUnicode bool) *regexp.Regexp {
	if c == nil {
		return nameToRegexp(name, useUnicode)
	}

	key := regexpCacheKey{name: name, useUnicode: useUnicode}
	if v, ok := c.regexpCache.Load(key); ok {
		return v.(*regexp.Regexp)
	}

	re := nameToRegexp(name, useUnicode)
	actual, _ := c.regexpCache.LoadOrStore(key, re)
	return actual.(*regexp.Regexp)
}

// performerCandidates returns the preloaded performers that should be
// regex-checked for the given path words: those sharing a 2-rune prefix
// with a path word, plus the always-check (single-letter-name) set.
func (c *Cache) performerCandidates(pathWords []string) []*models.Performer {
	if len(c.performerByPrefix) == 0 && len(c.performerAlwaysCheck) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(pathWords)*2)
	out := make([]*models.Performer, 0, len(pathWords)*2)
	for _, w := range pathWords {
		key := strings.ToLower(w)
		for _, p := range c.performerByPrefix[key] {
			if !seen[p.ID] {
				seen[p.ID] = true
				out = append(out, p)
			}
		}
	}
	for _, p := range c.performerAlwaysCheck {
		if !seen[p.ID] {
			seen[p.ID] = true
			out = append(out, p)
		}
	}
	return out
}

func (c *Cache) studioCandidates(pathWords []string) []cachedStudio {
	if len(c.studioByPrefix) == 0 && len(c.studioAlwaysCheck) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(pathWords)*2)
	out := make([]cachedStudio, 0, len(pathWords)*2)
	for _, w := range pathWords {
		key := strings.ToLower(w)
		for _, s := range c.studioByPrefix[key] {
			if !seen[s.Studio.ID] {
				seen[s.Studio.ID] = true
				out = append(out, s)
			}
		}
	}
	for _, s := range c.studioAlwaysCheck {
		if !seen[s.Studio.ID] {
			seen[s.Studio.ID] = true
			out = append(out, s)
		}
	}
	return out
}

func (c *Cache) tagCandidates(pathWords []string) []cachedTag {
	if len(c.tagByPrefix) == 0 && len(c.tagAlwaysCheck) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(pathWords)*2)
	out := make([]cachedTag, 0, len(pathWords)*2)
	for _, w := range pathWords {
		key := strings.ToLower(w)
		for _, t := range c.tagByPrefix[key] {
			if !seen[t.Tag.ID] {
				seen[t.Tag.ID] = true
				out = append(out, t)
			}
		}
	}
	for _, t := range c.tagAlwaysCheck {
		if !seen[t.Tag.ID] {
			seen[t.Tag.ID] = true
			out = append(out, t)
		}
	}
	return out
}

// Preload builds the in-memory prefix index for each non-nil reader. Call it
// once, serially, before the parallel matching phase. Readers may be nil to
// skip that category (matching Warm's contract).
func (c *Cache) Preload(ctx context.Context,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer) error {

	if performerReader != nil {
		if err := c.PreloadPerformers(ctx, performerReader); err != nil {
			return err
		}
	}
	if studioReader != nil {
		if err := c.PreloadStudios(ctx, studioReader); err != nil {
			return err
		}
	}
	if tagReader != nil {
		if err := c.PreloadTags(ctx, tagReader); err != nil {
			return err
		}
	}
	return nil
}

// PreloadPerformers loads all non-ignored performers into the cache and
// builds a 2-rune prefix index so subsequent PathToPerformers calls can
// skip the per-path QueryForAutoTag.
func (c *Cache) PreloadPerformers(ctx context.Context, reader models.PerformerAutoTagQueryer) error {
	if c.allPerformers != nil {
		return nil
	}
	ignoreAutoTag := false
	perPage := -1
	perfs, _, err := reader.Query(ctx, &models.PerformerFilterType{
		IgnoreAutoTag: &ignoreAutoTag,
	}, &models.FindFilterType{PerPage: &perPage})
	if err != nil {
		return err
	}
	if perfs == nil {
		perfs = []*models.Performer{}
	}
	c.allPerformers = perfs

	c.performerByPrefix = make(map[string][]*models.Performer, len(perfs))
	for _, p := range perfs {
		if prefix := firstTwoRunesLower(p.Name); prefix != "" {
			c.performerByPrefix[prefix] = append(c.performerByPrefix[prefix], p)
		}
		if singleFirstCharacterRE.MatchString(p.Name) {
			c.performerAlwaysCheck = append(c.performerAlwaysCheck, p)
		}
	}
	return nil
}

// loadAllAliases loads aliases for the given ids. Uses the reader's bulk
// GetAllAliases method when available (avoiding the N+1 per-id roundtrip);
// otherwise falls back to per-id GetAliases.
func loadAllAliases(ctx context.Context, reader models.AliasLoader, ids []int) (map[int][]string, error) {
	if bulk, ok := reader.(models.AllAliasLoader); ok {
		return bulk.GetAllAliases(ctx)
	}
	ret := make(map[int][]string, len(ids))
	for _, id := range ids {
		a, err := reader.GetAliases(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(a) > 0 {
			ret[id] = a
		}
	}
	return ret, nil
}

// PreloadStudios loads all non-ignored studios plus their aliases into the
// cache and builds a 2-rune prefix index (over names AND aliases).
func (c *Cache) PreloadStudios(ctx context.Context, reader models.StudioAutoTagQueryer) error {
	if c.allStudios != nil {
		return nil
	}
	ignoreAutoTag := false
	perPage := -1
	studios, _, err := reader.Query(ctx, &models.StudioFilterType{
		IgnoreAutoTag: &ignoreAutoTag,
	}, &models.FindFilterType{PerPage: &perPage})
	if err != nil {
		return err
	}
	ids := make([]int, len(studios))
	for i, s := range studios {
		ids[i] = s.ID
	}
	aliasesByID, err := loadAllAliases(ctx, reader, ids)
	if err != nil {
		return err
	}
	out := make([]cachedStudio, len(studios))
	c.studioByPrefix = make(map[string][]cachedStudio, len(studios))
	seenPerPrefix := make(map[string]map[int]bool)
	for i, s := range studios {
		aliases := aliasesByID[s.ID]
		cs := cachedStudio{Studio: s, Aliases: aliases}
		out[i] = cs

		c.indexByPrefix(s.ID, s.Name, aliases, seenPerPrefix, func(prefix string) {
			c.studioByPrefix[prefix] = append(c.studioByPrefix[prefix], cs)
		})
		if hasSingleFirstChar(s.Name, aliases) {
			c.studioAlwaysCheck = append(c.studioAlwaysCheck, cs)
		}
	}
	c.allStudios = out
	return nil
}

// PreloadTags loads all non-ignored tags plus their aliases into the cache
// and builds a 2-rune prefix index (over names AND aliases).
func (c *Cache) PreloadTags(ctx context.Context, reader models.TagAutoTagQueryer) error {
	if c.allTags != nil {
		return nil
	}
	ignoreAutoTag := false
	perPage := -1
	tags, _, err := reader.Query(ctx, &models.TagFilterType{
		IgnoreAutoTag: &ignoreAutoTag,
	}, &models.FindFilterType{PerPage: &perPage})
	if err != nil {
		return err
	}
	ids := make([]int, len(tags))
	for i, t := range tags {
		ids[i] = t.ID
	}
	aliasesByID, err := loadAllAliases(ctx, reader, ids)
	if err != nil {
		return err
	}
	out := make([]cachedTag, len(tags))
	c.tagByPrefix = make(map[string][]cachedTag, len(tags))
	seenPerPrefix := make(map[string]map[int]bool)
	for i, t := range tags {
		aliases := aliasesByID[t.ID]
		ct := cachedTag{Tag: t, Aliases: aliases}
		out[i] = ct

		c.indexByPrefix(t.ID, t.Name, aliases, seenPerPrefix, func(prefix string) {
			c.tagByPrefix[prefix] = append(c.tagByPrefix[prefix], ct)
		})
		if hasSingleFirstChar(t.Name, aliases) {
			c.tagAlwaysCheck = append(c.tagAlwaysCheck, ct)
		}
	}
	c.allTags = out
	return nil
}

// indexByPrefix records the entity under every distinct 2-rune prefix of
// its name/aliases (deduping so a name+alias sharing a prefix bucket only
// add the entity once).
func (c *Cache) indexByPrefix(id int, name string, aliases []string, seen map[string]map[int]bool, add func(prefix string)) {
	emit := func(s string) {
		prefix := firstTwoRunesLower(s)
		if prefix == "" {
			return
		}
		if seen[prefix] == nil {
			seen[prefix] = make(map[int]bool)
		}
		if !seen[prefix][id] {
			seen[prefix][id] = true
			add(prefix)
		}
	}
	emit(name)
	for _, a := range aliases {
		emit(a)
	}
}

func hasSingleFirstChar(name string, aliases []string) bool {
	if singleFirstCharacterRE.MatchString(name) {
		return true
	}
	for _, a := range aliases {
		if singleFirstCharacterRE.MatchString(a) {
			return true
		}
	}
	return false
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
