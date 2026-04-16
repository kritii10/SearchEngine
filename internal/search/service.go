package search

import (
	"fmt"
	"strings"
	"sync"

	"atlas-search/internal/crawler"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type Service struct {
	store   store.DocumentStore
	index   *index.Index
	fetcher *crawler.Fetcher
}

type CrawlIssue struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

type CrawlResponse struct {
	Documents []model.Document `json:"documents"`
	Issues    []CrawlIssue     `json:"issues"`
}

func NewService(store store.DocumentStore, idx *index.Index, fetcher *crawler.Fetcher) *Service {
	return &Service{
		store:   store,
		index:   idx,
		fetcher: fetcher,
	}
}

func (s *Service) Crawl(urls []string) (CrawlResponse, error) {
	const maxWorkers = 4

	type crawlResult struct {
		document model.Document
		issue    CrawlIssue
		err      error
	}

	normalized := make([]string, 0, len(urls))
	for _, url := range urls {
		if trimmed := strings.TrimSpace(url); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}

	if len(normalized) == 0 {
		return CrawlResponse{}, nil
	}

	jobs := make(chan string)
	results := make(chan crawlResult, len(normalized))

	workerCount := min(maxWorkers, len(normalized))
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for url := range jobs {
				doc, err := s.fetcher.Fetch(url)
				if err != nil {
					results <- crawlResult{
						issue: CrawlIssue{
							URL:   url,
							Error: err.Error(),
						},
					}
					continue
				}

				if err := s.store.Upsert(doc); err != nil {
					results <- crawlResult{
						err: fmt.Errorf("persist %s: %w", url, err),
					}
					continue
				}

				s.index.Add(doc)
				results <- crawlResult{document: doc}
			}
		}()
	}

	for _, url := range normalized {
		jobs <- url
	}
	close(jobs)

	wg.Wait()
	close(results)

	response := CrawlResponse{
		Documents: make([]model.Document, 0, len(normalized)),
		Issues:    []CrawlIssue{},
	}

	for result := range results {
		if result.err != nil {
			return CrawlResponse{}, result.err
		}
		if result.issue.URL != "" {
			response.Issues = append(response.Issues, result.issue)
			continue
		}
		response.Documents = append(response.Documents, result.document)
	}

	return response, nil
}

func (s *Service) Search(query string, limit int) ([]model.SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	scored := s.index.Search(query)
	if len(scored) > limit {
		scored = scored[:limit]
	}

	results := make([]model.SearchResult, 0, len(scored))
	for _, score := range scored {
		doc, err := s.store.Get(score.DocumentID)
		if err != nil {
			return nil, err
		}

		results = append(results, model.SearchResult{
			DocumentID:  doc.ID,
			URL:         doc.URL,
			Title:       doc.Title,
			Description: doc.Description,
			Snippet:     buildSnippet(doc.Content, query),
			Score:       score.Score,
		})
	}

	return results, nil
}

func (s *Service) Stats() map[string]int {
	return map[string]int{
		"documents": s.store.Count(),
	}
}

func buildSnippet(content, query string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}

	needle := strings.ToLower(strings.TrimSpace(query))
	haystack := strings.ToLower(content)

	if needle != "" {
		if idx := strings.Index(haystack, needle); idx >= 0 {
			start := max(0, idx-80)
			end := min(len(content), idx+len(needle)+120)
			return strings.TrimSpace(content[start:end])
		}
	}

	if len(content) <= 200 {
		return content
	}
	return strings.TrimSpace(content[:200]) + "..."
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
