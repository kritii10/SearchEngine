package search

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type Service struct {
	store   store.DocumentStore
	index   *index.Index
	fetcher Fetcher
	summary Summarizer

	jobQueue    chan string
	jobsMu      sync.RWMutex
	jobs        map[string]CrawlJob
	jobSequence uint64
}

type Fetcher interface {
	Fetch(url string) (model.Document, error)
}

type CrawlIssue struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

type CrawlResponse struct {
	Documents []model.Document `json:"documents"`
	Issues    []CrawlIssue     `json:"issues"`
}

type CrawlJobStatus string

const (
	CrawlJobQueued    CrawlJobStatus = "queued"
	CrawlJobRunning   CrawlJobStatus = "running"
	CrawlJobCompleted CrawlJobStatus = "completed"
	CrawlJobFailed    CrawlJobStatus = "failed"
)

type CrawlJob struct {
	ID          string         `json:"id"`
	Status      CrawlJobStatus `json:"status"`
	URLs        []string       `json:"urls"`
	Response    CrawlResponse  `json:"response"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	StartedAt   *time.Time     `json:"started_at,omitempty"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
}

func NewService(store store.DocumentStore, idx *index.Index, fetcher Fetcher) *Service {
	return NewServiceWithSummarizer(store, idx, fetcher, nil)
}

func NewServiceWithSummarizer(store store.DocumentStore, idx *index.Index, fetcher Fetcher, summarizer Summarizer) *Service {
	service := &Service{
		store:    store,
		index:    idx,
		fetcher:  fetcher,
		summary:  summarizer,
		jobQueue: make(chan string, 32),
		jobs:     make(map[string]CrawlJob),
	}
	go service.runCrawlWorker()
	return service
}

func (s *Service) Crawl(urls []string) (CrawlResponse, error) {
	return s.executeCrawl(urls)
}

func (s *Service) EnqueueCrawl(urls []string) (CrawlJob, error) {
	normalized := normalizeURLs(urls)
	if len(normalized) == 0 {
		return CrawlJob{}, nil
	}

	jobID := fmt.Sprintf("crawl-%d", atomic.AddUint64(&s.jobSequence, 1))
	job := CrawlJob{
		ID:        jobID,
		Status:    CrawlJobQueued,
		URLs:      normalized,
		Response:  CrawlResponse{Documents: []model.Document{}, Issues: []CrawlIssue{}},
		CreatedAt: time.Now().UTC(),
	}

	s.jobsMu.Lock()
	s.jobs[jobID] = job
	s.jobsMu.Unlock()

	s.jobQueue <- jobID

	return job, nil
}

func (s *Service) GetCrawlJob(id string) (CrawlJob, bool) {
	s.jobsMu.RLock()
	defer s.jobsMu.RUnlock()

	job, ok := s.jobs[id]
	return job, ok
}

func (s *Service) executeCrawl(urls []string) (CrawlResponse, error) {
	const maxWorkers = 4

	type crawlResult struct {
		document model.Document
		issue    CrawlIssue
		err      error
	}

	normalized := normalizeURLs(urls)

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
	response, err := s.SearchWithAnswer(context.Background(), query, limit)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

type SearchResponse struct {
	Query   string               `json:"query"`
	Count   int                  `json:"count"`
	Results []model.SearchResult `json:"results"`
	Answer  *model.AnswerSummary `json:"answer,omitempty"`
}

func (s *Service) SearchWithAnswer(ctx context.Context, query string, limit int) (SearchResponse, error) {
	if limit <= 0 {
		limit = 10
	}

	scored := s.index.Search(query)
	documents := make(map[string]model.Document, len(scored))
	for _, score := range scored {
		doc, err := s.store.Get(score.DocumentID)
		if err != nil {
			return SearchResponse{}, err
		}
		documents[score.DocumentID] = doc
	}

	ranked := rerank(query, scored, documents)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	results := make([]model.SearchResult, 0, len(ranked))
	for _, reranked := range ranked {
		doc := documents[reranked.documentID]

		results = append(results, model.SearchResult{
			DocumentID:  doc.ID,
			URL:         doc.URL,
			Title:       doc.Title,
			Description: doc.Description,
			Snippet:     buildSnippet(doc.Content, query),
			Score:       reranked.score,
			Signals:     reranked.signals,
		})
	}

	response := SearchResponse{
		Query:   query,
		Count:   len(results),
		Results: results,
	}

	if s.summary != nil && len(results) > 0 {
		summary, err := s.summary.Summarize(ctx, query, collectSnippets(results))
		if err == nil {
			response.Answer = &summary
		}
	}

	return response, nil
}

func (s *Service) runCrawlWorker() {
	for jobID := range s.jobQueue {
		s.setJobRunning(jobID)
		job, ok := s.GetCrawlJob(jobID)
		if !ok {
			continue
		}

		response, err := s.executeCrawl(job.URLs)
		if err != nil {
			s.setJobFailed(jobID, err)
			continue
		}

		s.setJobCompleted(jobID, response)
	}
}

func (s *Service) setJobRunning(jobID string) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	job.Status = CrawlJobRunning
	job.StartedAt = &now
	s.jobs[jobID] = job
}

func (s *Service) setJobFailed(jobID string, err error) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	job.Status = CrawlJobFailed
	job.Error = err.Error()
	job.CompletedAt = &now
	s.jobs[jobID] = job
}

func (s *Service) setJobCompleted(jobID string, response CrawlResponse) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()

	job, ok := s.jobs[jobID]
	if !ok {
		return
	}
	now := time.Now().UTC()
	job.Status = CrawlJobCompleted
	job.Response = response
	job.CompletedAt = &now
	s.jobs[jobID] = job
}

type rerankedScore struct {
	documentID string
	score      float64
	signals    model.RankingSignals
}

func rerank(query string, scored []index.ResultScore, documents map[string]model.Document) []rerankedScore {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return nil
	}

	queryTerms := index.Tokenize(query)
	reranked := make([]rerankedScore, 0, len(scored))
	for _, candidate := range scored {
		doc, ok := documents[candidate.DocumentID]
		if !ok {
			continue
		}

		signals := model.RankingSignals{
			BaseScore: candidate.Score,
		}

		lowerTitle := strings.ToLower(doc.Title)
		lowerDescription := strings.ToLower(doc.Description)
		lowerContent := strings.ToLower(doc.Content)

		for _, term := range queryTerms {
			if strings.Contains(lowerTitle, term) {
				signals.TitleMatchBoost += 0.35
			}
			if strings.Contains(lowerDescription, term) {
				signals.DescriptionBoost += 0.15
			}
		}

		if strings.Contains(lowerTitle, lowerQuery) {
			signals.ExactPhraseBoost += 1.25
		} else if strings.Contains(lowerContent, lowerQuery) {
			signals.ExactPhraseBoost += 0.65
		}

		total := candidate.Score + signals.TitleMatchBoost + signals.DescriptionBoost + signals.ExactPhraseBoost
		signals.CombinedScoreHint = total

		reranked = append(reranked, rerankedScore{
			documentID: candidate.DocumentID,
			score:      total,
			signals:    signals,
		})
	}

	sort.Slice(reranked, func(i, j int) bool {
		if reranked[i].score == reranked[j].score {
			return reranked[i].documentID < reranked[j].documentID
		}
		return reranked[i].score > reranked[j].score
	})

	return reranked
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

func collectSnippets(results []model.SearchResult) []string {
	snippets := make([]string, 0, len(results))
	for _, result := range results {
		if snippet := strings.TrimSpace(result.Snippet); snippet != "" {
			snippets = append(snippets, snippet)
		}
	}
	return snippets
}

func normalizeURLs(urls []string) []string {
	normalized := make([]string, 0, len(urls))
	for _, url := range urls {
		if trimmed := strings.TrimSpace(url); trimmed != "" {
			normalized = append(normalized, trimmed)
		}
	}
	return normalized
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
