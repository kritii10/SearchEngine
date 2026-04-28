package index

import (
	"testing"

	"atlas-search/internal/model"
)

func TestAddReplacesExistingDocumentPostings(t *testing.T) {
	idx := New()

	doc := model.Document{
		ID:    "doc-1",
		Terms: Tokenize("go search engine"),
	}
	idx.Add(doc)
	idx.Add(doc)

	results := idx.Search("go")
	if len(results) != 1 {
		t.Fatalf("expected exactly one indexed document after recrawl, got %d", len(results))
	}

	if idx.docCount != 1 {
		t.Fatalf("expected document count to remain 1, got %d", idx.docCount)
	}

	postings := idx.postings["go"]
	if len(postings) != 1 {
		t.Fatalf("expected exactly one posting for term 'go', got %d", len(postings))
	}
}

func TestAddReplacesOldTermsForUpdatedDocument(t *testing.T) {
	idx := New()

	idx.Add(model.Document{
		ID:    "doc-1",
		Terms: Tokenize("go concurrency"),
	})
	idx.Add(model.Document{
		ID:    "doc-1",
		Terms: Tokenize("search ranking"),
	})

	if results := idx.Search("concurrency"); len(results) != 0 {
		t.Fatalf("expected old terms to be removed, got %d results", len(results))
	}
	if results := idx.Search("search"); len(results) != 1 {
		t.Fatalf("expected updated terms to be searchable, got %d results", len(results))
	}
}

func TestSuggestReturnsPrefixMatchesSortedByUsage(t *testing.T) {
	idx := New()

	idx.Add(model.Document{
		ID:          "doc-1",
		Title:       "Search engine guide",
		Description: "Search engine ranking",
		Content:     "Search engine ranking systems need strong snippets.",
		Terms:       Tokenize("search search engine guide search engine ranking"),
	})
	idx.Add(model.Document{
		ID:          "doc-2",
		Title:       "Search system design",
		Description: "Search system notes",
		Content:     "Search system tradeoffs and retrieval patterns.",
		Terms:       Tokenize("search system design search system notes"),
	})
	idx.Add(model.Document{
		ID:          "doc-3",
		Title:       "Semantic retrieval",
		Description: "Semantic search ideas",
		Content:     "Semantic retrieval and hybrid ranking.",
		Terms:       Tokenize("semantic retrieval semantic search ideas"),
	})

	suggestions := idx.Suggest("se", 5)
	if len(suggestions) < 2 {
		t.Fatalf("expected multiple suggestions, got %#v", suggestions)
	}
	if suggestions[0] != "search" {
		t.Fatalf("expected most frequent term first, got %#v", suggestions)
	}
}

func TestSuggestSupportsMultiWordPhrasePrefix(t *testing.T) {
	idx := New()

	idx.Add(model.Document{
		ID:          "doc-1",
		Title:       "Go search engine",
		Description: "Go search engine tutorial",
		Content:     "Go search engine ranking and snippets.",
		Terms:       Tokenize("go search engine tutorial go search engine ranking"),
	})
	idx.Add(model.Document{
		ID:          "doc-2",
		Title:       "Go search console",
		Description: "Go search dashboard",
		Content:     "Go search dashboard patterns for premium search.",
		Terms:       Tokenize("go search console go search dashboard"),
	})

	suggestions := idx.Suggest("go se", 5)
	if len(suggestions) == 0 {
		t.Fatal("expected phrase suggestions")
	}
	if suggestions[0] != "go search engine" {
		t.Fatalf("expected phrase suggestion first, got %#v", suggestions)
	}
}

func TestStatsReportsIndexedTermsAndPhrases(t *testing.T) {
	idx := New()
	idx.Add(model.Document{
		ID:          "doc-1",
		Title:       "Atlas Search",
		Description: "Atlas Search premium dashboard",
		Content:     "Atlas Search premium dashboard and ranking insights.",
		Terms:       Tokenize("Atlas Search premium dashboard and ranking insights"),
	})

	stats := idx.Stats()
	if stats["documents"] != 1 {
		t.Fatalf("expected 1 document, got %+v", stats)
	}
	if stats["terms"] == 0 || stats["phrases"] == 0 {
		t.Fatalf("expected term and phrase stats, got %+v", stats)
	}
}

func TestCorrectQueryFixesNearMissTerm(t *testing.T) {
	idx := New()
	idx.Add(model.Document{
		ID:          "doc-1",
		Title:       "Premium Search",
		Description: "Search quality",
		Content:     "Premium search intelligence and ranking.",
		Terms:       Tokenize("Premium Search Search quality Premium search intelligence and ranking"),
	})

	corrected := idx.CorrectQuery("premum searh")
	if corrected != "premium search" {
		t.Fatalf("expected corrected query, got %q", corrected)
	}
}
