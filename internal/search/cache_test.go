package search

import (
	"context"
	"testing"

	"atlas-search/internal/cache"
	"atlas-search/internal/crawler"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type countingSummarizer struct {
	count int
}

func (s *countingSummarizer) Summarize(context.Context, string, []string) (model.AnswerSummary, error) {
	s.count++
	return model.AnswerSummary{
		Query:     "search",
		Summary:   "cached summary",
		Generated: true,
	}, nil
}

func TestSearchWithAnswerUsesCacheForRepeatQueries(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	doc := model.Document{
		ID:          "doc-1",
		URL:         "https://example.com",
		Title:       "Example Search",
		Description: "Example description",
		Content:     "Search engines need retrieval and summaries.",
		Terms:       index.Tokenize("Example Search Example description Search engines need retrieval and summaries"),
	}
	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	summarizer := &countingSummarizer{}
	service := NewServiceWithDependencies(documentStore, searchIndex, &crawler.Fetcher{}, summarizer, cache.NewMemoryCache())

	first, err := service.SearchWithAnswer(context.Background(), "search", 10)
	if err != nil {
		t.Fatalf("first search failed: %v", err)
	}
	second, err := service.SearchWithAnswer(context.Background(), "search", 10)
	if err != nil {
		t.Fatalf("second search failed: %v", err)
	}

	if first.Count != 1 || second.Count != 1 {
		t.Fatalf("expected cached results count to stay stable, got %d and %d", first.Count, second.Count)
	}
	if summarizer.count != 1 {
		t.Fatalf("expected summarizer to be called once due to cache hit, got %d", summarizer.count)
	}

	analytics := service.Analytics()
	if analytics.TotalQueries != 2 {
		t.Fatalf("expected analytics to record 2 queries, got %+v", analytics)
	}
	if analytics.CacheHits != 1 {
		t.Fatalf("expected 1 cache hit, got %+v", analytics)
	}
	if analytics.CacheHitRate <= 0 {
		t.Fatalf("expected positive cache hit rate, got %+v", analytics)
	}
}
