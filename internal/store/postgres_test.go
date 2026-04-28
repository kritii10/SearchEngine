package store

import (
	"fmt"
	"testing"
	"time"

	"atlas-search/internal/model"
)

func TestScanDocumentDecodesTermsJSON(t *testing.T) {
	now := time.Now().UTC()
	doc, err := scanDocument(func(dest ...any) error {
		*(dest[0].(*string)) = "doc-1"
		*(dest[1].(*string)) = "https://example.com"
		*(dest[2].(*string)) = "example.com"
		*(dest[3].(*string)) = "Example"
		*(dest[4].(*string)) = "Description"
		*(dest[5].(*string)) = "Content"
		*(dest[6].(*[]byte)) = []byte(`["search","engine"]`)
		*(dest[7].(*[]byte)) = []byte(`["https://example.com/docs"]`)
		*(dest[8].(*[]byte)) = []byte(`["Search Overview"]`)
		*(dest[9].(*string)) = "fp-123"
		*(dest[10].(*time.Time)) = now
		*(dest[11].(*time.Time)) = now.Add(24 * time.Hour)
		return nil
	})
	if err != nil {
		t.Fatalf("scan document: %v", err)
	}

	if doc.ID != "doc-1" {
		t.Fatalf("expected doc id doc-1, got %s", doc.ID)
	}
	if len(doc.Terms) != 2 || doc.Terms[0] != "search" {
		t.Fatalf("expected decoded terms, got %#v", doc.Terms)
	}
	if doc.Domain != "example.com" {
		t.Fatalf("expected domain, got %q", doc.Domain)
	}
	if doc.ContentFingerprint != "fp-123" {
		t.Fatalf("expected content fingerprint, got %q", doc.ContentFingerprint)
	}
	if len(doc.Links) != 1 || doc.Links[0] != "https://example.com/docs" {
		t.Fatalf("expected decoded links, got %#v", doc.Links)
	}
	if len(doc.Headings) != 1 || doc.Headings[0] != "Search Overview" {
		t.Fatalf("expected decoded headings, got %#v", doc.Headings)
	}
}

func TestScanDocumentPropagatesScanError(t *testing.T) {
	_, err := scanDocument(func(dest ...any) error {
		return fmt.Errorf("scan failed")
	})
	if err == nil {
		t.Fatal("expected scan error")
	}
}

func TestMemoryStoreStillImplementsDocumentStore(t *testing.T) {
	var _ DocumentStore = NewMemoryStore()
	var _ DocumentStore = &PostgresStore{}
	_ = model.Document{}
}
