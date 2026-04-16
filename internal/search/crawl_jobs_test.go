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
	documents map[string]model.Document
	errs      map[string]error
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

func TestEnqueueCrawlProcessesJobAndStoresResults(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	fetcher := stubFetcher{
		documents: map[string]model.Document{
			"https://example.com": {
				ID:          "doc-1",
				URL:         "https://example.com",
				Title:       "Example Search",
				Description: "Example document",
				Content:     "Search engines crawl and rank documents.",
				Terms:       index.Tokenize("Example Search Example document Search engines crawl and rank documents"),
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
				ID:      "doc-2",
				URL:     "https://bad.example",
				Title:   "Bad Example",
				Content: "This will fail storage.",
				Terms:   index.Tokenize("Bad Example This will fail storage"),
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
func (failingStore) List() []model.Document { return nil }
func (failingStore) Count() int             { return 0 }

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
