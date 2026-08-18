package taxonomy

import (
	"math"
	"sort"
	"strings"
	"sync"
)

const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

type bm25Doc struct {
	entry  Entry
	length int
	terms  map[string]int
}

type bm25Entry struct {
	docFrequency int
	idf          float64
}

// BM25Hit is one ranked taxonomy document.
type BM25Hit struct {
	Entry Entry
	Score float64
}

// BM25 is a concurrency-safe BM25Okapi index over taxonomy entries.
type BM25 struct {
	mu    sync.RWMutex
	index map[string]bm25Entry
	docs  []bm25Doc
	avg   float64
}

// Rebuild atomically replaces the index with entries.
func (b *BM25) Rebuild(entries []Entry) {
	docs := make([]bm25Doc, 0, len(entries))
	documentFrequency := make(map[string]int)
	totalTerms := 0

	for _, entry := range entries {
		text := strings.ToLower(strings.Join(append(
			[]string{entry.Canonical},
			append(append([]string(nil), entry.Aliases...), entry.Category, entry.Description)...,
		), " "))
		terms := make(map[string]int)
		for _, term := range tokenize(text) {
			terms[term]++
			totalTerms++
		}
		for term := range terms {
			documentFrequency[term]++
		}
		docs = append(docs, bm25Doc{entry: entry, length: sumTermFrequency(terms), terms: terms})
	}

	index := make(map[string]bm25Entry, len(documentFrequency))
	n := float64(len(docs))
	for term, frequency := range documentFrequency {
		df := float64(frequency)
		index[term] = bm25Entry{
			docFrequency: frequency,
			idf:          math.Log(1 + (n-df+0.5)/(df+0.5)),
		}
	}
	average := 0.0
	if len(docs) > 0 {
		average = float64(totalTerms) / float64(len(docs))
	}

	b.mu.Lock()
	b.index = index
	b.docs = docs
	b.avg = average
	b.mu.Unlock()
}

// Top returns the k highest-scoring entries for query. Entries with no matching
// query term are omitted.
func (b *BM25) Top(query string, k int) []BM25Hit {
	if b == nil || k <= 0 {
		return nil
	}
	queryTerms := tokenize(strings.ToLower(query))
	if len(queryTerms) == 0 {
		return nil
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if len(b.docs) == 0 || b.avg == 0 {
		return nil
	}

	hits := make([]BM25Hit, 0, min(k, len(b.docs)))
	for _, doc := range b.docs {
		score := 0.0
		for _, term := range queryTerms {
			frequency := doc.terms[term]
			termStats, ok := b.index[term]
			if !ok || frequency == 0 {
				continue
			}
			tf := float64(frequency)
			normalization := 1 - bm25B + bm25B*float64(doc.length)/b.avg
			score += termStats.idf * (tf * (bm25K1 + 1)) / (tf + bm25K1*normalization)
		}
		if score > 0 {
			hits = append(hits, BM25Hit{Entry: doc.entry, Score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			if hits[i].Entry.Canonical == hits[j].Entry.Canonical {
				return hits[i].Entry.StashID < hits[j].Entry.StashID
			}
			return hits[i].Entry.Canonical < hits[j].Entry.Canonical
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	return append([]BM25Hit(nil), hits...)
}

func sumTermFrequency(terms map[string]int) int {
	total := 0
	for _, frequency := range terms {
		total += frequency
	}
	return total
}
