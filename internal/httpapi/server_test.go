package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"atlas-search/internal/config"
	"atlas-search/internal/crawler"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/search"
	"atlas-search/internal/store"
)

func TestServerSetsSecurityHeaders(t *testing.T) {
	service := search.NewService(store.NewMemoryStore(), index.New(), &crawler.Fetcher{})
	server := NewServer(config.Config{Address: ":0"}, service)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("expected content security policy header")
	}
}

func TestReadyzAndStaleEndpoints(t *testing.T) {
	documentStore := store.NewMemoryStore()
	_ = documentStore.Upsert(model.Document{
		ID:                 "doc-1",
		URL:                "https://example.com/stale",
		Title:              "Stale",
		Content:            "stale body",
		Terms:              index.Tokenize("stale body"),
		ContentFingerprint: "fp-1",
		RecrawlAfter:       time.Now().Add(-time.Hour).UTC(),
	})
	service := search.NewService(documentStore, index.New(), &crawler.Fetcher{})
	server := NewServer(config.Config{Address: ":0"}, service)

	readyReq := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	readyRec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK {
		t.Fatalf("expected readyz 200, got %d", readyRec.Code)
	}

	staleReq := httptest.NewRequest(http.MethodGet, "/api/v1/crawl/stale?limit=5", nil)
	staleRec := httptest.NewRecorder()
	server.httpServer.Handler.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusOK {
		t.Fatalf("expected stale endpoint 200, got %d", staleRec.Code)
	}
}
