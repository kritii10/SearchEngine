package search

import (
	"strings"
	"testing"

	"atlas-search/internal/crawler"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

func TestSearchBoostsTitleAndPhraseMatches(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:          "doc-1",
			URL:         "https://example.com/go-search",
			Title:       "Go Search Engine",
			Description: "Build a search engine in Go",
			Content:     "This guide explains how to build a search engine in Go with ranking and indexing.",
			Terms:       index.Tokenize("Go Search Engine Build a search engine in Go This guide explains how to build a search engine in Go with ranking and indexing"),
		},
		{
			ID:          "doc-2",
			URL:         "https://example.com/distributed-systems",
			Title:       "Distributed Systems Notes",
			Description: "Search architecture notes",
			Content:     "These notes mention search once, but mostly discuss distributed systems tradeoffs.",
			Terms:       index.Tokenize("Distributed Systems Notes Search architecture notes These notes mention search once but mostly discuss distributed systems tradeoffs"),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, &crawler.Fetcher{})

	results, err := service.Search("go search engine", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	if results[0].DocumentID != "doc-1" {
		t.Fatalf("expected doc-1 to rank first, got %s", results[0].DocumentID)
	}

	if results[0].Signals.TitleMatchBoost <= 0 {
		t.Fatalf("expected title match boost, got %+v", results[0].Signals)
	}

	if results[0].Signals.ExactPhraseBoost <= 0 {
		t.Fatalf("expected exact phrase boost, got %+v", results[0].Signals)
	}
}

func TestBuildSnippetPrefersMatchingRegion(t *testing.T) {
	content := "Distributed indexing is useful. The best search engine needs ranking, snippets, and cache-aware query handling for good latency."

	snippet := buildSnippet(content, "search engine")
	if snippet == "" {
		t.Fatal("expected non-empty snippet")
	}

	if !strings.Contains(strings.ToLower(snippet), "search engine") {
		t.Fatalf("expected snippet to contain search engine, got %q", snippet)
	}
}
