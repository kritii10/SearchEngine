package search

import (
	"fmt"
	"testing"
	"time"

	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type stubFetcher struct {
	documents  map[string]model.Document
	errs       map[string]error
	discovered map[string][]string
}

func (f stubFetcher) Fetch(url string) (model.Document, error) {
	if err, ok := f.errs[url]; ok {
		return model.Document{}, err
	}
	if doc, ok := f.documents[url]; ok {
		return doc, nil
	}
	return model.Document{}, fmt.Errorf("unexpected url: %s", url)
}

func (f stubFetcher) Discover(rawURL string) ([]string, error) {
	if urls, ok := f.discovered[rawURL]; ok {
		return urls, nil
	}
	return []string{rawURL}, nil
}

func TestEnqueueCrawlProcessesJobAndStoresResults(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	fetcher := stubFetcher{
		documents: map[string]model.Document{
			"https://example.com": {
				ID:                 "doc-1",
				URL:                "https://example.com",
				Title:              "Example Search",
				Description:        "Example document",
				Content:            "Search engines crawl and rank documents.",
				Terms:              index.Tokenize("Example Search Example document Search engines crawl and rank documents"),
				ContentFingerprint: "fp-1",
			},
		},
	}

	service := NewService(documentStore, searchIndex, fetcher)
	job, err := service.EnqueueCrawl([]string{"https://example.com"})
	if err != nil {
		t.Fatalf("enqueue crawl: %v", err)
	}

	completed := waitForJobStatus(t, service, job.ID, CrawlJobCompleted)
	if len(completed.Response.Documents) != 1 {
		t.Fatalf("expected 1 crawled document, got %d", len(completed.Response.Documents))
	}
	if completed.Response.Documents[0].ID != "doc-1" {
		t.Fatalf("expected doc-1, got %s", completed.Response.Documents[0].ID)
	}
}

func TestEnqueueCrawlMarksJobFailedOnPersistenceError(t *testing.T) {
	fetcher := stubFetcher{
		documents: map[string]model.Document{
			"https://bad.example": {
				ID:                 "doc-2",
				URL:                "https://bad.example",
				Title:              "Bad Example",
				Content:            "This will fail storage.",
				Terms:              index.Tokenize("Bad Example This will fail storage"),
				ContentFingerprint: "fp-2",
			},
		},
	}

	service := NewService(failingStore{}, index.New(), fetcher)
	job, err := service.EnqueueCrawl([]string{"https://bad.example"})
	if err != nil {
		t.Fatalf("enqueue crawl: %v", err)
	}

	failed := waitForJobStatus(t, service, job.ID, CrawlJobFailed)
	if failed.Error == "" {
		t.Fatal("expected failed job to record an error")
	}
}

type failingStore struct{}

func (failingStore) Upsert(model.Document) error { return fmt.Errorf("boom") }
func (failingStore) Get(string) (model.Document, error) {
	return model.Document{}, store.ErrDocumentNotFound
}
func (failingStore) FindByContentFingerprint(string) (model.Document, error) {
	return model.Document{}, store.ErrDocumentNotFound
}
func (failingStore) List() []model.Document { return nil }
func (failingStore) Count() int             { return 0 }
func (failingStore) StaleCount(time.Time) int {
	return 0
}

func TestCrawlDeduplicatesMatchingContentFingerprint(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	fetcher := stubFetcher{
		documents: map[string]model.Document{
			"https://example.com/primary": {
				ID:                 "doc-primary",
				URL:                "https://example.com/primary",
				Title:              "Primary Search Page",
				Description:        "Canonical page",
				Content:            "Premium search results and ranking signals.",
				Terms:              index.Tokenize("Primary Search Page Canonical page Premium search results and ranking signals"),
				ContentFingerprint: "shared-fp",
			},
			"https://mirror.example/primary": {
				ID:                 "doc-mirror",
				URL:                "https://mirror.example/primary",
				Title:              "Mirror Search Page",
				Description:        "Duplicate page",
				Content:            "Premium search results and ranking signals.",
				Terms:              index.Tokenize("Mirror Search Page Duplicate page Premium search results and ranking signals"),
				ContentFingerprint: "shared-fp",
			},
		},
	}

	service := NewService(documentStore, searchIndex, fetcher)
	response, err := service.Crawl([]string{"https://example.com/primary", "https://mirror.example/primary"})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}

	if len(response.Documents) != 1 {
		t.Fatalf("expected 1 stored document after dedupe, got %d", len(response.Documents))
	}
	if len(response.Duplicates) != 1 {
		t.Fatalf("expected 1 duplicate record, got %+v", response.Duplicates)
	}
	if response.Duplicates[0].CanonicalURL != "https://example.com/primary" {
		t.Fatalf("expected canonical url to be original page, got %+v", response.Duplicates[0])
	}
	if documentStore.Count() != 1 {
		t.Fatalf("expected store count to remain 1, got %d", documentStore.Count())
	}
}

func TestCrawlExpandsDiscoveredURLs(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	fetcher := stubFetcher{
		discovered: map[string][]string{
			"https://example.com/": {"https://example.com/", "https://example.com/guide", "https://example.com/docs"},
		},
		documents: map[string]model.Document{
			"https://example.com/": {
				ID: "doc-root", URL: "https://example.com/", Title: "Root", Content: "Root", Terms: index.Tokenize("Root"),
			},
			"https://example.com/guide": {
				ID: "doc-guide", URL: "https://example.com/guide", Title: "Guide", Content: "Guide", Terms: index.Tokenize("Guide"),
			},
			"https://example.com/docs": {
				ID: "doc-docs", URL: "https://example.com/docs", Title: "Docs", Content: "Docs", Terms: index.Tokenize("Docs"),
			},
		},
	}

	service := NewService(documentStore, searchIndex, fetcher)
	response, err := service.Crawl([]string{"https://example.com/"})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if len(response.Documents) != 3 {
		t.Fatalf("expected discovered docs to be crawled, got %+v", response.Documents)
	}
	if len(response.Discovered) != 2 {
		t.Fatalf("expected discovered url list, got %+v", response.Discovered)
	}
}

func waitForJobStatus(t *testing.T, service *Service, jobID string, want CrawlJobStatus) CrawlJob {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := service.GetCrawlJob(jobID)
		if ok && job.Status == want {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}

	job, _ := service.GetCrawlJob(jobID)
	t.Fatalf("job %s did not reach status %s, got %+v", jobID, want, job)
	return CrawlJob{}
}
