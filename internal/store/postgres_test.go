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
		*(dest[2].(*string)) = "Example"
		*(dest[3].(*string)) = "Description"
		*(dest[4].(*string)) = "Content"
		*(dest[5].(*[]byte)) = []byte(`["search","engine"]`)
		*(dest[6].(*time.Time)) = now
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
