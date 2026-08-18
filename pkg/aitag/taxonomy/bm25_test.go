package taxonomy

import (
	"context"
	"reflect"
	"testing"
)

func TestBM25TopRanksSpecificMultiTermEntry(t *testing.T) {
	index := &BM25{}
	index.Rebuild([]Entry{
		{StashID: "footjob", Canonical: "Dildo Footjob", Aliases: []string{"dildo feet"}, Category: "Acts"},
		{StashID: "dildo", Canonical: "Dildo", Category: "Accessories"},
		{StashID: "feet", Canonical: "Foot Fetish", Aliases: []string{"feet"}, Category: "Themes"},
		{StashID: "couch", Canonical: "Couch", Category: "Surfaces"},
	})

	hits := index.Top("dildo feet", 3)
	got := make([]string, len(hits))
	for i := range hits {
		got[i] = hits[i].Entry.Canonical
	}
	want := []string{"Dildo Footjob", "Dildo", "Foot Fetish"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ranking = %q, want %q", got, want)
	}
}

func TestBM25IDFRewardsDiscriminativeTerms(t *testing.T) {
	index := &BM25{}
	index.Rebuild([]Entry{
		{StashID: "rare", Canonical: "Dildo Zephyric", Category: "Acts"},
		{StashID: "one", Canonical: "Dildo Play", Category: "Acts"},
		{StashID: "two", Canonical: "Dildo Sex", Category: "Acts"},
		{StashID: "three", Canonical: "Sex Position", Category: "Acts"},
	})

	rare := index.Top("zephyric", 1)
	generic := index.Top("dildo", 1)
	if len(rare) != 1 || len(generic) != 1 {
		t.Fatalf("missing hits: rare=%#v generic=%#v", rare, generic)
	}
	if rare[0].Score <= generic[0].Score {
		t.Fatalf("rare score %.4f must exceed generic score %.4f", rare[0].Score, generic[0].Score)
	}
}

func TestBM25RebuildDropsOldDocuments(t *testing.T) {
	index := &BM25{}
	index.Rebuild([]Entry{{StashID: "old", Canonical: "Old Dildo"}})
	index.Rebuild([]Entry{{StashID: "new", Canonical: "New Couch"}})
	if hits := index.Top("dildo", 3); len(hits) != 0 {
		t.Fatalf("old index leaked after rebuild: %#v", hits)
	}
	if hits := index.Top("couch", 1); len(hits) != 1 || hits[0].Entry.StashID != "new" {
		t.Fatalf("new index missing: %#v", hits)
	}
}

func TestClientBM25TracksCacheAliasCount(t *testing.T) {
	client := &Client{Cache: &Cache{Entries: map[string]Entry{
		"one": {StashID: "one", Canonical: "Alpha", Aliases: []string{"dildo"}},
	}}}
	if hits, err := client.BM25Top(context.Background(), "dildo", 1); err != nil || len(hits) != 1 {
		t.Fatalf("initial BM25Top = %#v, %v", hits, err)
	}
	client.Cache.Entries["one"] = Entry{StashID: "one", Canonical: "Alpha"}
	if hits, err := client.BM25Top(context.Background(), "dildo", 1); err != nil || len(hits) != 0 {
		t.Fatalf("rebuilt BM25Top = %#v, %v", hits, err)
	}
}
