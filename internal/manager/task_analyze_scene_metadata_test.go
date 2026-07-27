package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/stashapp/stash/pkg/scraper"
)

type cancelingPerformerScraperCache struct {
	calls  int
	cancel context.CancelFunc
}

func (c *cancelingPerformerScraperCache) ListScrapers(_ []scraper.ScrapeContentType) []*scraper.Scraper {
	return []*scraper.Scraper{{ID: "canceling-scraper"}}
}

func (c *cancelingPerformerScraperCache) ScrapeName(_ context.Context, _, _ string, _ scraper.ScrapeContentType) ([]scraper.ScrapedContent, error) {
	c.calls++
	c.cancel()
	return nil, context.Canceled
}

func TestVerifyAndCreatePerformersStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &cancelingPerformerScraperCache{cancel: cancel}
	j := &analyzeSceneMetadataJob{scraperCache: cache}

	_, err := j.verifyAndCreatePerformers(ctx, []string{"First Candidate", "Second Candidate"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyAndCreatePerformers() error = %v, want context.Canceled", err)
	}
	if cache.calls != 1 {
		t.Fatalf("scraper calls = %d, want 1", cache.calls)
	}
}
