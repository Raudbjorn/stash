package main

import (
	"fmt"
	"math/rand"
	"strings"
)

// synthEntity holds a generated entity's identity (name + optional aliases)
// and its canonical path token (lowercase-underscore form), used later when
// building file basenames so path-matching has something to find.
type synthEntity struct {
	name    string
	aliases []string
	token   string
}

// nameToToken lowercases and underscore-separates a name so it embeds cleanly
// in a file path. "Alice Smith" -> "alice_smith", which the auto-tag regex
// will reliably match against.
func nameToToken(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "_"))
}

// generatePerformers returns n unique performer names. The word lists are
// natural-language (with expected overlap between first names and surnames),
// so a seen-map guards against collisions. Past saturation, a numeric
// suffix disambiguates.
func generatePerformers(n int, rng *rand.Rand) []synthEntity {
	nFirst := len(firstNames)
	nLast := len(surnames)
	seen := make(map[string]bool, n)
	out := make([]synthEntity, 0, n)
	for i := 0; len(out) < n; i++ {
		name := firstNames[rng.Intn(nFirst)] + " " + surnames[rng.Intn(nLast)]
		if seen[name] {
			if i < 10*n {
				continue
			}
			name = fmt.Sprintf("%s %d", name, i)
		}
		seen[name] = true
		out = append(out, synthEntity{name: name, token: nameToToken(name)})
	}
	return out
}

// generateStudios returns n studios; ~20% have 1-2 aliases. The validator
// rejects any studio whose name or alias already appears as a name or alias
// on another studio, so we globally dedupe both.
func generateStudios(n int, rng *rand.Rand) []synthEntity {
	vocabSize := len(studioVocab)
	taken := make(map[string]bool, n*2)
	out := make([]synthEntity, 0, n)
	uniqueStudioName := func(attempt int) string {
		name := studioVocab[rng.Intn(vocabSize)] + " " + studioVocab[rng.Intn(vocabSize)]
		if taken[name] {
			return fmt.Sprintf("%s %d", name, attempt)
		}
		return name
	}
	for len(out) < n {
		name := uniqueStudioName(len(out))
		for taken[name] {
			name = uniqueStudioName(len(out) + rng.Intn(1<<20))
		}
		taken[name] = true
		ent := synthEntity{name: name, token: nameToToken(name)}
		if rng.Intn(100) < 20 {
			nAliases := 1 + rng.Intn(2)
			for j := 0; j < nAliases; j++ {
				alias := studioVocab[rng.Intn(vocabSize)] + " " + studioVocab[rng.Intn(vocabSize)]
				for taken[alias] {
					alias = fmt.Sprintf("%s %d", alias, rng.Intn(1<<20))
				}
				taken[alias] = true
				ent.aliases = append(ent.aliases, alias)
			}
		}
		out = append(out, ent)
	}
	return out
}

// generateTags returns n tags; ~15% have 1 alias. Tags have the same
// name-vs-alias uniqueness validator as studios.
func generateTags(n int, rng *rand.Rand) []synthEntity {
	vocabSize := len(tagVocab)
	taken := make(map[string]bool, n*2)
	out := make([]synthEntity, 0, n)
	for len(out) < n {
		name := tagVocab[rng.Intn(vocabSize)]
		for taken[name] {
			name = fmt.Sprintf("%s_%d", tagVocab[rng.Intn(vocabSize)], rng.Intn(1<<20))
		}
		taken[name] = true
		ent := synthEntity{name: name, token: nameToToken(name)}
		if rng.Intn(100) < 15 {
			alias := tagVocab[rng.Intn(vocabSize)]
			for taken[alias] {
				alias = fmt.Sprintf("%s_alias_%d", tagVocab[rng.Intn(vocabSize)], rng.Intn(1<<20))
			}
			taken[alias] = true
			ent.aliases = append(ent.aliases, alias)
		}
		out = append(out, ent)
	}
	return out
}

// pathSpec describes one synthetic file path: the basename (embeds entity
// tokens so auto-tag has something to match) and its target kind.
type pathSpec struct {
	basename string
	kind     fileKind
}

type fileKind int

const (
	kindScene fileKind = iota
	kindImage
	kindGallery
)

// generatePaths emits n paths split 60/30/10 across scenes/images/galleries.
// Distribution of matches follows the hardcoded realistic mix:
//
//	30% multi-match (2-3 entities embedded)
//	50% single-match (1 entity embedded)
//	20% no-match (random word fragments, no entity tokens)
//
// "entities embedded" draws uniformly from the performer/studio/tag pools
// restricted to whichever types are enabled for the run.
func generatePaths(n int, performers, studios, tags []synthEntity, rng *rand.Rand) []pathSpec {
	paths := make([]pathSpec, n)
	pools := [][]synthEntity{}
	if len(performers) > 0 {
		pools = append(pools, performers)
	}
	if len(studios) > 0 {
		pools = append(pools, studios)
	}
	if len(tags) > 0 {
		pools = append(pools, tags)
	}

	sceneCount := n * 60 / 100
	imageCount := n * 30 / 100
	// remainder goes to galleries so totals match n exactly
	for i := 0; i < n; i++ {
		r := rng.Intn(100)
		var tokens []string
		switch {
		case r < 30 && len(pools) > 0:
			// multi-match: 2-3 entities drawn from pools
			count := 2 + rng.Intn(2)
			for k := 0; k < count; k++ {
				pool := pools[rng.Intn(len(pools))]
				tokens = append(tokens, pool[rng.Intn(len(pool))].token)
			}
		case r < 80 && len(pools) > 0:
			// single-match: one entity
			pool := pools[rng.Intn(len(pools))]
			tokens = append(tokens, pool[rng.Intn(len(pool))].token)
		default:
			// no-match: two random filler tokens
			tokens = []string{randFiller(rng), randFiller(rng)}
		}

		var ext string
		var kind fileKind
		switch {
		case i < sceneCount:
			ext = ".mp4"
			kind = kindScene
		case i < sceneCount+imageCount:
			ext = ".jpg"
			kind = kindImage
		default:
			ext = ".zip"
			kind = kindGallery
		}

		// unique suffix prevents basename collisions
		basename := strings.Join(tokens, "_") + fmt.Sprintf("_%06d%s", i, ext)
		paths[i] = pathSpec{basename: basename, kind: kind}
	}

	// shuffle so kinds are interleaved in insertion order (more realistic)
	rng.Shuffle(len(paths), func(i, j int) { paths[i], paths[j] = paths[j], paths[i] })
	return paths
}

// randFiller produces a short pseudo-word to pad no-match paths without
// tripping any entity regex.
func randFiller(rng *rand.Rand) string {
	const chars = "bcdfghjklmnpqrstvwxyz"
	n := 4 + rng.Intn(4)
	out := make([]byte, n)
	for i := range out {
		out[i] = chars[rng.Intn(len(chars))]
	}
	return string(out)
}

// preset captures one row of the benchmark matrix.
type preset struct {
	name       string
	performers int
	studios    int
	tags       int
	files      int
}

var presets = map[string]preset{
	"tiny":   {name: "tiny", performers: 100, studios: 20, tags: 50, files: 1000},
	"small":  {name: "small", performers: 1000, studios: 200, tags: 300, files: 10000},
	"medium": {name: "medium", performers: 10000, studios: 1300, tags: 1000, files: 50000},
	"large":  {name: "large", performers: 100000, studios: 13000, tags: 3000, files: 100000},
}
