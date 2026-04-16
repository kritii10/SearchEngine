package search

import (
	"context"
	"testing"

	"atlas-search/internal/crawler"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type stubSummarizer struct {
	summary model.AnswerSummary
	err     error
}

func (s stubSummarizer) Summarize(context.Context, string, []string) (model.AnswerSummary, error) {
	return s.summary, s.err
}

func TestSearchWithAnswerIncludesSummaryWhenAvailable(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:          "doc-1",
		URL:         "https://example.com/go-search",
		Title:       "Go Search Engine",
		Description: "Build a search engine in Go",
		Content:     "This guide explains how to build a search engine in Go with ranking and indexing.",
		Terms:       index.Tokenize("Go Search Engine Build a search engine in Go This guide explains how to build a search engine in Go with ranking and indexing"),
	}

	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewServiceWithSummarizer(documentStore, searchIndex, &crawler.Fetcher{}, stubSummarizer{
		summary: model.AnswerSummary{
			Query:          "go search engine",
			Summary:        "Atlas Search found a grounded answer.",
			GroundedPoints: []string{"build a search engine in Go"},
			Generated:      true,
		},
	})

	response, err := service.SearchWithAnswer(context.Background(), "go search engine", 10)
	if err != nil {
		t.Fatalf("search with answer failed: %v", err)
	}

	if response.Answer == nil {
		t.Fatal("expected answer summary to be present")
	}

	if response.Answer.Summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestSearchWithAnswerSkipsSummaryOnSummarizerFailure(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:      "doc-1",
		URL:     "https://example.com",
		Title:   "Example Search",
		Content: "Search engines need retrieval and summaries.",
		Terms:   index.Tokenize("Example Search Search engines need retrieval and summaries"),
	}

	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewServiceWithSummarizer(documentStore, searchIndex, &crawler.Fetcher{}, stubSummarizer{
		err: context.DeadlineExceeded,
	})

	response, err := service.SearchWithAnswer(context.Background(), "search", 10)
	if err != nil {
		t.Fatalf("search with answer failed: %v", err)
	}

	if response.Answer != nil {
		t.Fatal("expected answer summary to be omitted when summarizer fails")
	}
}
