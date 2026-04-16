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
