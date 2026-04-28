package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"atlas-search/internal/cache"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

type Service struct {
	store     store.DocumentStore
	index     *index.Index
	fetcher   Fetcher
	summary   Summarizer
	cache     cache.Cache
	startedAt time.Time

	jobQueue    chan string
	jobsMu      sync.RWMutex
	jobs        map[string]CrawlJob
	jobSequence uint64
	dedupCount  uint64
	metricsMu   sync.Mutex
	metrics     serviceMetrics
}

type serviceMetrics struct {
	totalQueries   uint64
	cacheHits      uint64
	totalLatencyMS uint64
	queryCounts    map[string]uint64
	lastLatencyMS  int64
	lastQueryAt    time.Time
	lastCacheHit   bool
}

type Fetcher interface {
	Fetch(url string) (model.Document, error)
}

type URLDiscoverer interface {
	Discover(rawURL string) ([]string, error)
}

type CrawlIssue struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

type CrawlDuplicate struct {
	URL          string `json:"url"`
	CanonicalURL string `json:"canonical_url"`
	DocumentID   string `json:"document_id"`
}

type CrawlResponse struct {
	Documents  []model.Document `json:"documents"`
	Issues     []CrawlIssue     `json:"issues"`
	Duplicates []CrawlDuplicate `json:"duplicates"`
	Discovered []string         `json:"discovered"`
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
	return NewServiceWithDependencies(store, idx, fetcher, nil, nil)
}

func NewServiceWithSummarizer(store store.DocumentStore, idx *index.Index, fetcher Fetcher, summarizer Summarizer) *Service {
	return NewServiceWithDependencies(store, idx, fetcher, summarizer, nil)
}

func NewServiceWithDependencies(store store.DocumentStore, idx *index.Index, fetcher Fetcher, summarizer Summarizer, queryCache cache.Cache) *Service {
	service := &Service{
		store:     store,
		index:     idx,
		fetcher:   fetcher,
		summary:   summarizer,
		cache:     queryCache,
		startedAt: time.Now().UTC(),
		jobQueue:  make(chan string, 32),
		jobs:      make(map[string]CrawlJob),
		metrics: serviceMetrics{
			queryCounts: make(map[string]uint64),
		},
	}
	go service.runCrawlWorker()
	return service
}

func (s *Service) Crawl(urls []string) (CrawlResponse, error) {
	response, err := s.executeCrawl(urls)
	if err != nil {
		return CrawlResponse{}, err
	}
	atomic.AddUint64(&s.dedupCount, uint64(len(response.Duplicates)))
	s.invalidateSearchCache()
	return response, nil
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
		Response:  CrawlResponse{Documents: []model.Document{}, Issues: []CrawlIssue{}, Duplicates: []CrawlDuplicate{}},
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

func (s *Service) ListStaleDocuments(limit int) []RecrawlCandidate {
	now := time.Now().UTC()
	documents := s.store.List()
	candidates := make([]RecrawlCandidate, 0, len(documents))
	for _, doc := range documents {
		if doc.RecrawlAfter.IsZero() || doc.RecrawlAfter.After(now) {
			continue
		}
		candidates = append(candidates, RecrawlCandidate{
			DocumentID:    doc.ID,
			URL:           doc.URL,
			Domain:        doc.Domain,
			Title:         doc.Title,
			RecrawlAfter:  doc.RecrawlAfter,
			StaleForHours: int64(now.Sub(doc.RecrawlAfter).Hours()),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].RecrawlAfter.Equal(candidates[j].RecrawlAfter) {
			return candidates[i].URL < candidates[j].URL
		}
		return candidates[i].RecrawlAfter.Before(candidates[j].RecrawlAfter)
	})
	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func (s *Service) EnqueueRecrawlDue(limit int) (CrawlJob, error) {
	candidates := s.ListStaleDocuments(limit)
	urls := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		urls = append(urls, candidate.URL)
	}
	return s.EnqueueCrawl(urls)
}

func (s *Service) executeCrawl(urls []string) (CrawlResponse, error) {
	const maxWorkers = 4

	type crawlResult struct {
		document  model.Document
		issue     CrawlIssue
		duplicate *CrawlDuplicate
		err       error
	}

	normalized, discovered := s.expandCrawlURLs(urls)

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

				if doc.ContentFingerprint != "" {
					if existing, err := s.store.FindByContentFingerprint(doc.ContentFingerprint); err == nil && existing.ID != doc.ID {
						results <- crawlResult{
							document: existing,
							duplicate: &CrawlDuplicate{
								URL:          url,
								CanonicalURL: existing.URL,
								DocumentID:   existing.ID,
							},
						}
						continue
					}
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
		Documents:  make([]model.Document, 0, len(normalized)),
		Issues:     []CrawlIssue{},
		Duplicates: []CrawlDuplicate{},
		Discovered: discovered,
	}

	for result := range results {
		if result.err != nil {
			return CrawlResponse{}, result.err
		}
		if result.issue.URL != "" {
			response.Issues = append(response.Issues, result.issue)
			continue
		}
		if result.duplicate != nil {
			response.Duplicates = append(response.Duplicates, *result.duplicate)
			continue
		}
		response.Documents = append(response.Documents, result.document)
	}

	return response, nil
}

func (s *Service) expandCrawlURLs(urls []string) ([]string, []string) {
	normalized := normalizeURLs(urls)
	discovered := make([]string, 0, len(normalized))
	discoverer, ok := s.fetcher.(URLDiscoverer)
	if !ok {
		return normalized, discovered
	}

	expanded := make([]string, 0, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for _, rawURL := range normalized {
		found, err := discoverer.Discover(rawURL)
		if err != nil || len(found) == 0 {
			if _, exists := seen[rawURL]; !exists {
				seen[rawURL] = struct{}{}
				expanded = append(expanded, rawURL)
			}
			continue
		}
		for _, item := range normalizeURLs(found) {
			if _, exists := seen[item]; exists {
				continue
			}
			seen[item] = struct{}{}
			expanded = append(expanded, item)
			if item != rawURL {
				discovered = append(discovered, item)
			}
		}
	}
	return expanded, discovered
}

func (s *Service) Search(query string, limit int) ([]model.SearchResult, error) {
	response, err := s.SearchWithAnswer(context.Background(), query, limit)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

func (s *Service) Suggest(prefix string, limit int) []string {
	return s.index.Suggest(prefix, limit)
}

type SearchResponse struct {
	Query           string               `json:"query"`
	NormalizedQuery string               `json:"normalized_query"`
	CorrectedQuery  string               `json:"corrected_query,omitempty"`
	Interpretation  QueryInterpretation  `json:"interpretation"`
	Count           int                  `json:"count"`
	Results         []model.SearchResult `json:"results"`
	Answer          *model.AnswerSummary `json:"answer,omitempty"`
	Quality         model.SearchQuality  `json:"quality"`
	RelatedQueries  []string             `json:"related_queries,omitempty"`
	Domains         []SearchDomainFacet  `json:"domains,omitempty"`
	Coverage        []SearchSourceDigest `json:"coverage,omitempty"`
	Timeline        []SearchTimelineItem `json:"timeline,omitempty"`
	Compare         *SearchCompareCard   `json:"compare,omitempty"`
	SortBy          string               `json:"sort_by"`
	MinTrust        int                  `json:"min_trust"`
	Domain          string               `json:"domain"`
	TrustedOnly     bool                 `json:"trusted_only"`
	Limit           int                  `json:"limit"`
	Offset          int                  `json:"offset"`
	HasMore         bool                 `json:"has_more"`
	Cached          bool                 `json:"cached"`
	TookMS          int64                `json:"took_ms"`
}

type SearchOptions struct {
	SortBy      string
	MinTrust    int
	Domain      string
	Offset      int
	TrustedOnly bool
}

type SearchDomainFacet struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

type SearchSourceDigest struct {
	Domain    string `json:"domain"`
	Count     int    `json:"count"`
	AvgTrust  int    `json:"avg_trust"`
	BestTitle string `json:"best_title"`
}

type SearchTimelineItem struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type SearchCompareCard struct {
	Summary string               `json:"summary"`
	Items   []SearchCompareEntry `json:"items"`
}

type SearchCompareEntry struct {
	Title     string `json:"title"`
	Domain    string `json:"domain"`
	Strength  string `json:"strength"`
	Trust     int    `json:"trust"`
	Freshness string `json:"freshness"`
}

type QueryInterpretation struct {
	Intent       string   `json:"intent"`
	Terms        []string `json:"terms,omitempty"`
	Phrases      []string `json:"phrases,omitempty"`
	ExcludeTerms []string `json:"exclude_terms,omitempty"`
	TitleTerms   []string `json:"title_terms,omitempty"`
	URLTerms     []string `json:"url_terms,omitempty"`
	FileTypes    []string `json:"file_types,omitempty"`
	After        string   `json:"after,omitempty"`
	Before       string   `json:"before,omitempty"`
	Site         string   `json:"site,omitempty"`
	Advanced     bool     `json:"advanced"`
}

type parsedQuery struct {
	cleaned        string
	terms          []string
	phrases        []string
	excludeTerms   []string
	titleTerms     []string
	urlTerms       []string
	fileTypes      []string
	after          *time.Time
	before         *time.Time
	site           string
	intent         string
	hasAdvancedOps bool
}

type AnalyticsResponse struct {
	TotalQueries  uint64          `json:"total_queries"`
	CacheHits     uint64          `json:"cache_hits"`
	CacheHitRate  float64         `json:"cache_hit_rate"`
	AverageMS     float64         `json:"average_ms"`
	LastLatencyMS int64           `json:"last_latency_ms"`
	LastCacheHit  bool            `json:"last_cache_hit"`
	LastQueryAt   *time.Time      `json:"last_query_at,omitempty"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	TopQueries    []AnalyticsItem `json:"top_queries"`
}

type AnalyticsItem struct {
	Query string `json:"query"`
	Count uint64 `json:"count"`
}

type RecrawlCandidate struct {
	DocumentID    string    `json:"document_id"`
	URL           string    `json:"url"`
	Domain        string    `json:"domain"`
	Title         string    `json:"title"`
	RecrawlAfter  time.Time `json:"recrawl_after"`
	StaleForHours int64     `json:"stale_for_hours"`
}

func (s *Service) SearchWithAnswer(ctx context.Context, query string, limit int) (SearchResponse, error) {
	return s.SearchWithOptions(ctx, query, limit, SearchOptions{})
}

func (s *Service) SearchWithOptions(ctx context.Context, query string, limit int, opts SearchOptions) (SearchResponse, error) {
	started := time.Now()
	if limit <= 0 {
		limit = 10
	}
	query = strings.TrimSpace(query)
	parsed := parseQuery(query)
	if opts.Domain == "" && parsed.site != "" {
		opts.Domain = parsed.site
	}
	opts = normalizeSearchOptions(opts)
	normalizedQuery := parsed.cleaned
	correctedQuery := s.index.CorrectQuery(parsed.cleaned)
	if correctedQuery != "" && correctedQuery != normalizedQuery {
		normalizedQuery = correctedQuery
	}
	if query == "" {
		return SearchResponse{
			Query:           query,
			NormalizedQuery: normalizedQuery,
			CorrectedQuery:  correctedQueryIfNeeded(parsed.cleaned, normalizedQuery),
			Interpretation:  buildQueryInterpretation(parsed, opts),
			Count:           0,
			Results:         []model.SearchResult{},
			Domains:         []SearchDomainFacet{},
			SortBy:          opts.SortBy,
			MinTrust:        opts.MinTrust,
			Domain:          opts.Domain,
			TrustedOnly:     opts.TrustedOnly,
			Limit:           limit,
			Offset:          opts.Offset,
			Cached:          false,
			TookMS:          time.Since(started).Milliseconds(),
		}, nil
	}

	cacheKey := fmt.Sprintf("search:%s:%d:%s:%d:%s:%d:%t", normalizedQuery, limit, opts.SortBy, opts.MinTrust, opts.Domain, opts.Offset, opts.TrustedOnly)
	if s.cache != nil {
		cached, ok, err := s.cache.Get(ctx, cacheKey)
		if err == nil && ok {
			var response SearchResponse
			if err := json.Unmarshal([]byte(cached), &response); err == nil {
				response.Cached = true
				s.recordSearchMetric(query, response.TookMS, true)
				return response, nil
			}
		}
	}

	scored := s.index.Search(normalizedQuery)
	documents := make(map[string]model.Document, len(scored))
	for _, score := range scored {
		doc, err := s.store.Get(score.DocumentID)
		if err != nil {
			return SearchResponse{}, err
		}
		documents[score.DocumentID] = doc
	}

	ranked := rerank(normalizedQuery, parsed, scored, documents)

	results := make([]model.SearchResult, 0, len(ranked))
	for _, reranked := range ranked {
		doc := documents[reranked.documentID]
		if !documentMatchesParsedQuery(doc, parsed) {
			continue
		}

		results = append(results, model.SearchResult{
			DocumentID:   doc.ID,
			URL:          doc.URL,
			Domain:       doc.Domain,
			FileType:     detectFileType(doc.URL),
			Title:        doc.Title,
			Description:  doc.Description,
			Snippet:      buildSnippet(doc.Content, normalizedQuery),
			SiteLinks:    buildSiteLinks(doc),
			MatchContext: buildMatchContext(doc, parsed),
			Score:        reranked.score,
			CrawledAt:    doc.CrawledAt,
			RecrawlAfter: doc.RecrawlAfter,
			Signals:      reranked.signals,
			Trust:        scoreTrust(doc, reranked.signals),
			Explanations: buildExplanations(doc, reranked.signals),
		})
	}
	results = filterAndSortResults(results, opts)
	domainFacets := buildDomainFacets(results)
	totalCount := len(results)
	pagedResults, hasMore := paginateResults(results, opts.Offset, limit)

	response := SearchResponse{
		Query:           query,
		NormalizedQuery: normalizedQuery,
		CorrectedQuery:  correctedQueryIfNeeded(parsed.cleaned, normalizedQuery),
		Interpretation:  buildQueryInterpretation(parsed, opts),
		Count:           totalCount,
		Results:         pagedResults,
		Quality:         summarizeQuality(pagedResults),
		RelatedQueries:  buildRelatedQueries(normalizedQuery, pagedResults, s.index.Suggest(normalizedQuery, max(limit, 6))),
		Domains:         domainFacets,
		Coverage:        buildSourceCoverage(results),
		Timeline:        buildTimeline(results),
		Compare:         buildCompareCard(normalizedQuery, pagedResults),
		SortBy:          opts.SortBy,
		MinTrust:        opts.MinTrust,
		Domain:          opts.Domain,
		TrustedOnly:     opts.TrustedOnly,
		Limit:           limit,
		Offset:          opts.Offset,
		HasMore:         hasMore,
		Cached:          false,
	}

	if s.summary != nil && len(results) > 0 {
		summary, err := s.summary.Summarize(ctx, normalizedQuery, collectSnippets(results))
		if err == nil {
			summary.Citations = buildCitations(pagedResults)
			summary.RelatedQuestions = buildPeopleAlsoAsk(normalizedQuery, pagedResults)
			response.Answer = &summary
		}
	}
	if response.Answer == nil && len(pagedResults) > 0 {
		response.Answer = &model.AnswerSummary{
			Query:            normalizedQuery,
			Summary:          fmt.Sprintf("Top results for '%s' come from grounded Atlas documents.", normalizedQuery),
			GroundedPoints:   collectSnippets(pagedResults[:min(3, len(pagedResults))]),
			Citations:        buildCitations(pagedResults),
			RelatedQuestions: buildPeopleAlsoAsk(normalizedQuery, pagedResults),
		}
	}

	response.TookMS = time.Since(started).Milliseconds()

	if s.cache != nil {
		if payload, err := json.Marshal(response); err == nil {
			_ = s.cache.Set(ctx, cacheKey, string(payload), 2*time.Minute)
		}
	}

	s.recordSearchMetric(normalizedQuery, response.TookMS, false)

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

		atomic.AddUint64(&s.dedupCount, uint64(len(response.Duplicates)))
		s.invalidateSearchCache()

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

func rerank(query string, parsed parsedQuery, scored []index.ResultScore, documents map[string]model.Document) []rerankedScore {
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	if lowerQuery == "" {
		return nil
	}

	queryTerms := index.Tokenize(query)
	authorityScores := computeAuthorityScores(documents)
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
			for _, heading := range doc.Headings {
				if strings.Contains(strings.ToLower(heading), term) {
					signals.HeadingMatchBoost += 0.22
				}
			}
			if strings.Contains(lowerDescription, term) {
				signals.DescriptionBoost += 0.15
			}
		}
		for _, term := range parsed.titleTerms {
			if strings.Contains(lowerTitle, term) {
				signals.TitleMatchBoost += 0.5
			}
		}
		for _, term := range parsed.urlTerms {
			if strings.Contains(strings.ToLower(doc.URL), term) {
				signals.DescriptionBoost += 0.25
			}
		}

		if strings.Contains(lowerTitle, lowerQuery) {
			signals.ExactPhraseBoost += 1.25
		} else if strings.Contains(lowerContent, lowerQuery) {
			signals.ExactPhraseBoost += 0.65
		}
		for _, phrase := range parsed.phrases {
			if strings.Contains(lowerTitle, phrase) {
				signals.ExactPhraseBoost += 0.55
			} else if strings.Contains(lowerContent, phrase) {
				signals.ExactPhraseBoost += 0.3
			}
		}

		signals.FreshnessBoost = freshnessBoost(doc.CrawledAt)
		signals.AuthorityBoost = authorityScores[doc.ID] * 0.5

		total := candidate.Score + signals.AuthorityBoost + signals.HeadingMatchBoost + signals.TitleMatchBoost + signals.DescriptionBoost + signals.ExactPhraseBoost + signals.FreshnessBoost
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

func computeAuthorityScores(documents map[string]model.Document) map[string]float64 {
	if len(documents) == 0 {
		return map[string]float64{}
	}

	urlToID := make(map[string]string, len(documents))
	scores := make(map[string]float64, len(documents))
	outbound := make(map[string][]string, len(documents))

	for id, doc := range documents {
		urlToID[doc.URL] = id
		scores[id] = 1.0 / float64(len(documents))
	}
	for id, doc := range documents {
		for _, link := range doc.Links {
			if targetID, ok := urlToID[link]; ok && targetID != id {
				outbound[id] = append(outbound[id], targetID)
			}
		}
	}

	for iteration := 0; iteration < 8; iteration++ {
		next := make(map[string]float64, len(documents))
		base := 0.15 / float64(len(documents))
		for id := range documents {
			next[id] = base
		}
		for id, score := range scores {
			targets := outbound[id]
			if len(targets) == 0 {
				share := 0.85 * score / float64(len(documents))
				for targetID := range documents {
					next[targetID] += share
				}
				continue
			}
			share := 0.85 * score / float64(len(targets))
			for _, targetID := range targets {
				next[targetID] += share
			}
		}
		scores = next
	}

	maxScore := 0.0
	for _, score := range scores {
		if score > maxScore {
			maxScore = score
		}
	}
	if maxScore > 0 {
		for id, score := range scores {
			scores[id] = score / maxScore
		}
	}
	return scores
}

func freshnessBoost(crawledAt time.Time) float64 {
	if crawledAt.IsZero() {
		return 0
	}
	age := time.Since(crawledAt)
	switch {
	case age <= 24*time.Hour:
		return 0.35
	case age <= 7*24*time.Hour:
		return 0.2
	case age <= 30*24*time.Hour:
		return 0.08
	default:
		return 0
	}
}

func normalizeSearchOptions(opts SearchOptions) SearchOptions {
	switch opts.SortBy {
	case "trust", "recent":
	default:
		opts.SortBy = "relevance"
	}
	if opts.MinTrust < 0 {
		opts.MinTrust = 0
	}
	if opts.MinTrust > 100 {
		opts.MinTrust = 100
	}
	opts.Domain = strings.ToLower(strings.TrimSpace(opts.Domain))
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if opts.TrustedOnly && opts.MinTrust < 80 {
		opts.MinTrust = 80
	}
	return opts
}

func parseQuery(query string) parsedQuery {
	trimmed := strings.TrimSpace(strings.ToLower(query))
	if trimmed == "" {
		return parsedQuery{}
	}

	rawTerms := strings.Fields(trimmed)
	phrases := extractQuotedPhrases(trimmed)
	excludeTerms := make([]string, 0)
	titleTerms := make([]string, 0)
	urlTerms := make([]string, 0)
	fileTypes := make([]string, 0)
	site := ""
	var after *time.Time
	var before *time.Time
	includeTokens := make([]string, 0, len(rawTerms))

	for _, term := range rawTerms {
		switch {
		case strings.HasPrefix(term, "site:") && len(term) > len("site:"):
			site = strings.TrimPrefix(term, "site:")
		case strings.HasPrefix(term, "intitle:") && len(term) > len("intitle:"):
			titleTerms = append(titleTerms, strings.TrimPrefix(term, "intitle:"))
		case strings.HasPrefix(term, "inurl:") && len(term) > len("inurl:"):
			urlTerms = append(urlTerms, strings.TrimPrefix(term, "inurl:"))
		case strings.HasPrefix(term, "filetype:") && len(term) > len("filetype:"):
			fileTypes = append(fileTypes, normalizeFileType(strings.TrimPrefix(term, "filetype:")))
		case strings.HasPrefix(term, "after:") && len(term) > len("after:"):
			after = parseDateOperator(strings.TrimPrefix(term, "after:"))
		case strings.HasPrefix(term, "before:") && len(term) > len("before:"):
			before = parseDateOperator(strings.TrimPrefix(term, "before:"))
		case strings.HasPrefix(term, "-") && len(term) > 1:
			excludeTerms = append(excludeTerms, strings.TrimPrefix(term, "-"))
		default:
			includeTokens = append(includeTokens, term)
		}
	}

	replacer := strings.NewReplacer(`"`, "", "'", "")
	for _, phrase := range phrases {
		trimmed = strings.ReplaceAll(trimmed, `"`+phrase+`"`, phrase)
	}

	cleanedTokens := make([]string, 0, len(includeTokens))
	for _, token := range includeTokens {
		if strings.HasPrefix(token, "site:") || strings.HasPrefix(token, "intitle:") || strings.HasPrefix(token, "inurl:") || strings.HasPrefix(token, "filetype:") || strings.HasPrefix(token, "after:") || strings.HasPrefix(token, "before:") || strings.HasPrefix(token, "-") {
			continue
		}
		cleanedTokens = append(cleanedTokens, replacer.Replace(token))
	}

	cleaned := strings.Join(strings.Fields(strings.Join(cleanedTokens, " ")), " ")
	terms := index.Tokenize(cleaned)

	return parsedQuery{
		cleaned:        cleaned,
		terms:          terms,
		phrases:        phrases,
		excludeTerms:   excludeTerms,
		titleTerms:     index.Tokenize(strings.Join(titleTerms, " ")),
		urlTerms:       index.Tokenize(strings.Join(urlTerms, " ")),
		fileTypes:      compactFileTypes(fileTypes),
		after:          after,
		before:         before,
		site:           strings.TrimSpace(site),
		intent:         classifyIntent(trimmed, phrases, site),
		hasAdvancedOps: len(phrases) > 0 || len(excludeTerms) > 0 || len(titleTerms) > 0 || len(urlTerms) > 0 || len(fileTypes) > 0 || after != nil || before != nil || site != "",
	}
}

func parseDateOperator(raw string) *time.Time {
	parsed, err := time.Parse("2006-01-02", strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	timestamp := parsed.UTC()
	return &timestamp
}

func normalizeFileType(raw string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

func compactFileTypes(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	compact := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		compact = append(compact, value)
	}
	return compact
}

func extractQuotedPhrases(query string) []string {
	phrases := make([]string, 0, 2)
	start := -1
	for i, r := range query {
		if r != '"' {
			continue
		}
		if start == -1 {
			start = i + 1
			continue
		}
		phrase := strings.TrimSpace(query[start:i])
		if phrase != "" {
			phrases = append(phrases, phrase)
		}
		start = -1
	}
	return phrases
}

func classifyIntent(query string, phrases []string, site string) string {
	lower := strings.TrimSpace(strings.ToLower(query))
	switch {
	case site != "":
		return "navigational"
	case strings.Contains(lower, " vs ") || strings.Contains(lower, "compare") || strings.Contains(lower, "best "):
		return "comparison"
	case strings.Contains(lower, "latest") || strings.Contains(lower, "today") || strings.Contains(lower, "new ") || strings.Contains(lower, "news"):
		return "freshness"
	case strings.HasPrefix(lower, "how ") || strings.HasPrefix(lower, "what ") || strings.HasPrefix(lower, "why ") || strings.HasPrefix(lower, "guide ") || strings.HasPrefix(lower, "tutorial "):
		return "informational"
	case len(phrases) > 0:
		return "precision"
	default:
		return "exploration"
	}
}

func buildQueryInterpretation(parsed parsedQuery, opts SearchOptions) QueryInterpretation {
	site := parsed.site
	if site == "" {
		site = opts.Domain
	}
	return QueryInterpretation{
		Intent:       parsed.intent,
		Terms:        parsed.terms,
		Phrases:      parsed.phrases,
		ExcludeTerms: parsed.excludeTerms,
		TitleTerms:   parsed.titleTerms,
		URLTerms:     parsed.urlTerms,
		FileTypes:    parsed.fileTypes,
		After:        formatQueryDate(parsed.after),
		Before:       formatQueryDate(parsed.before),
		Site:         site,
		Advanced:     parsed.hasAdvancedOps || opts.TrustedOnly || opts.MinTrust > 0 || opts.Domain != "",
	}
}

func formatQueryDate(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.Format("2006-01-02")
}

func documentMatchesParsedQuery(doc model.Document, parsed parsedQuery) bool {
	if parsed.site != "" && doc.Domain != parsed.site && !strings.HasSuffix(doc.Domain, "."+parsed.site) {
		return false
	}
	fullText := strings.ToLower(strings.Join([]string{doc.Title, doc.Description, doc.Content}, " "))
	lowerTitle := strings.ToLower(doc.Title)
	lowerURL := strings.ToLower(doc.URL)
	fileType := detectFileType(doc.URL)
	for _, phrase := range parsed.phrases {
		if !strings.Contains(fullText, phrase) {
			return false
		}
	}
	for _, term := range parsed.titleTerms {
		if !strings.Contains(lowerTitle, term) {
			return false
		}
	}
	for _, term := range parsed.urlTerms {
		if !strings.Contains(lowerURL, term) {
			return false
		}
	}
	if len(parsed.fileTypes) > 0 {
		matchedType := false
		for _, fileTypeQuery := range parsed.fileTypes {
			if fileType == fileTypeQuery {
				matchedType = true
				break
			}
		}
		if !matchedType {
			return false
		}
	}
	if parsed.after != nil && (doc.CrawledAt.IsZero() || doc.CrawledAt.Before(*parsed.after)) {
		return false
	}
	if parsed.before != nil && (doc.CrawledAt.IsZero() || !doc.CrawledAt.Before(parsed.before.Add(24*time.Hour))) {
		return false
	}
	for _, term := range parsed.excludeTerms {
		if strings.Contains(fullText, term) {
			return false
		}
	}
	return true
}

func filterAndSortResults(results []model.SearchResult, opts SearchOptions) []model.SearchResult {
	filtered := make([]model.SearchResult, 0, len(results))
	for _, result := range results {
		if result.Trust.Value < opts.MinTrust {
			continue
		}
		if opts.Domain != "" && result.Domain != opts.Domain && !strings.HasSuffix(result.Domain, "."+opts.Domain) {
			continue
		}
		if opts.TrustedOnly && !isTrustedSource(result.URL) {
			continue
		}
		filtered = append(filtered, result)
	}

	sort.Slice(filtered, func(i, j int) bool {
		switch opts.SortBy {
		case "trust":
			if filtered[i].Trust.Value == filtered[j].Trust.Value {
				if filtered[i].Score == filtered[j].Score {
					return filtered[i].DocumentID < filtered[j].DocumentID
				}
				return filtered[i].Score > filtered[j].Score
			}
			return filtered[i].Trust.Value > filtered[j].Trust.Value
		case "recent":
			if filtered[i].CrawledAt.Equal(filtered[j].CrawledAt) {
				if filtered[i].Score == filtered[j].Score {
					return filtered[i].DocumentID < filtered[j].DocumentID
				}
				return filtered[i].Score > filtered[j].Score
			}
			return filtered[i].CrawledAt.After(filtered[j].CrawledAt)
		default:
			if filtered[i].Score == filtered[j].Score {
				return filtered[i].DocumentID < filtered[j].DocumentID
			}
			return filtered[i].Score > filtered[j].Score
		}
	})

	if opts.SortBy == "relevance" && opts.Domain == "" {
		filtered = diversifyDomains(filtered, 2)
	}

	return filtered
}

func diversifyDomains(results []model.SearchResult, maxPerDomain int) []model.SearchResult {
	if maxPerDomain <= 0 {
		return results
	}
	counts := make(map[string]int)
	diversified := make([]model.SearchResult, 0, len(results))
	for _, result := range results {
		domain := result.Domain
		if domain == "" {
			diversified = append(diversified, result)
			continue
		}
		if counts[domain] >= maxPerDomain {
			continue
		}
		counts[domain]++
		diversified = append(diversified, result)
	}
	return diversified
}

func correctedQueryIfNeeded(original, normalized string) string {
	original = strings.TrimSpace(strings.ToLower(original))
	if normalized == "" || normalized == original {
		return ""
	}
	return normalized
}

func buildCitations(results []model.SearchResult) []model.Citation {
	citations := make([]model.Citation, 0, min(3, len(results)))
	for _, result := range results[:min(3, len(results))] {
		citations = append(citations, model.Citation{
			Title: result.Title,
			URL:   result.URL,
		})
	}
	return citations
}

func buildPeopleAlsoAsk(query string, results []model.SearchResult) []string {
	questions := make([]string, 0, 4)
	questions = append(questions,
		fmt.Sprintf("What is the best source for %s?", query),
		fmt.Sprintf("How fresh are the results for %s?", query),
	)
	if len(results) > 0 {
		questions = append(questions, fmt.Sprintf("Why did Atlas rank %s first for %s?", results[0].Title, query))
	}
	if len(results) > 1 {
		questions = append(questions, fmt.Sprintf("Which result best explains %s?", query))
	}
	return questions[:min(4, len(questions))]
}

func isTrustedSource(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	trusted := []string{"gov", "edu", "org", "nih.gov", "cdc.gov", "who.int"}
	for _, suffix := range trusted {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}

func buildDomainFacets(results []model.SearchResult) []SearchDomainFacet {
	counts := make(map[string]int)
	for _, result := range results {
		if result.Domain == "" {
			continue
		}
		counts[result.Domain]++
	}

	facets := make([]SearchDomainFacet, 0, len(counts))
	for domain, count := range counts {
		facets = append(facets, SearchDomainFacet{Domain: domain, Count: count})
	}
	sort.Slice(facets, func(i, j int) bool {
		if facets[i].Count == facets[j].Count {
			return facets[i].Domain < facets[j].Domain
		}
		return facets[i].Count > facets[j].Count
	})
	return facets
}

func buildSourceCoverage(results []model.SearchResult) []SearchSourceDigest {
	if len(results) == 0 {
		return nil
	}

	type aggregate struct {
		count      int
		totalTrust int
		bestTitle  string
		bestScore  float64
	}

	aggregates := make(map[string]aggregate)
	for _, result := range results {
		domain := result.Domain
		if domain == "" {
			domain = "(unknown)"
		}
		agg := aggregates[domain]
		agg.count++
		agg.totalTrust += result.Trust.Value
		if result.Score > agg.bestScore || agg.bestTitle == "" {
			agg.bestScore = result.Score
			agg.bestTitle = result.Title
		}
		aggregates[domain] = agg
	}

	coverage := make([]SearchSourceDigest, 0, len(aggregates))
	for domain, agg := range aggregates {
		coverage = append(coverage, SearchSourceDigest{
			Domain:    domain,
			Count:     agg.count,
			AvgTrust:  agg.totalTrust / agg.count,
			BestTitle: agg.bestTitle,
		})
	}
	sort.Slice(coverage, func(i, j int) bool {
		if coverage[i].Count == coverage[j].Count {
			if coverage[i].AvgTrust == coverage[j].AvgTrust {
				return coverage[i].Domain < coverage[j].Domain
			}
			return coverage[i].AvgTrust > coverage[j].AvgTrust
		}
		return coverage[i].Count > coverage[j].Count
	})
	if len(coverage) > 5 {
		coverage = coverage[:5]
	}
	return coverage
}

func buildTimeline(results []model.SearchResult) []SearchTimelineItem {
	buckets := []SearchTimelineItem{
		{Label: "Past day"},
		{Label: "Past week"},
		{Label: "Past month"},
		{Label: "Older"},
	}
	for _, result := range results {
		age := time.Since(result.CrawledAt)
		switch {
		case result.CrawledAt.IsZero():
			buckets[3].Count++
		case age <= 24*time.Hour:
			buckets[0].Count++
		case age <= 7*24*time.Hour:
			buckets[1].Count++
		case age <= 30*24*time.Hour:
			buckets[2].Count++
		default:
			buckets[3].Count++
		}
	}
	return buckets
}

func buildCompareCard(query string, results []model.SearchResult) *SearchCompareCard {
	if len(results) < 2 {
		return nil
	}
	items := make([]SearchCompareEntry, 0, min(3, len(results)))
	for _, result := range results[:min(3, len(results))] {
		items = append(items, SearchCompareEntry{
			Title:     result.Title,
			Domain:    result.Domain,
			Strength:  summarizeResultStrength(result),
			Trust:     result.Trust.Value,
			Freshness: freshnessLabel(result.CrawledAt),
		})
	}
	return &SearchCompareCard{
		Summary: fmt.Sprintf("Atlas sees %d strong angles for %s across the top-ranked results.", len(items), query),
		Items:   items,
	}
}

func summarizeResultStrength(result model.SearchResult) string {
	switch {
	case result.Signals.AuthorityBoost >= 0.35:
		return "authority-heavy"
	case result.Signals.HeadingMatchBoost >= 0.4:
		return "structured heading match"
	case result.Signals.FreshnessBoost >= 0.2:
		return "fresh coverage"
	case result.Signals.ExactPhraseBoost >= 1:
		return "tight phrase match"
	case result.Signals.TitleMatchBoost >= 0.7:
		return "title-led relevance"
	default:
		return "balanced relevance"
	}
}

func freshnessLabel(crawledAt time.Time) string {
	if crawledAt.IsZero() {
		return "undated"
	}
	age := time.Since(crawledAt)
	switch {
	case age <= 24*time.Hour:
		return "today"
	case age <= 7*24*time.Hour:
		return "this week"
	case age <= 30*24*time.Hour:
		return "this month"
	default:
		return "archived"
	}
}

func buildSiteLinks(doc model.Document) []string {
	links := make([]string, 0, min(3, len(doc.Headings)))
	for _, heading := range doc.Headings[:min(3, len(doc.Headings))] {
		links = append(links, heading)
	}
	return links
}

func buildMatchContext(doc model.Document, parsed parsedQuery) []string {
	context := make([]string, 0, 4)
	if fileType := detectFileType(doc.URL); fileType != "" {
		for _, requested := range parsed.fileTypes {
			if fileType == requested {
				context = append(context, fmt.Sprintf("Filetype matched %s", requested))
			}
		}
	}
	if parsed.after != nil && !doc.CrawledAt.IsZero() && !doc.CrawledAt.Before(*parsed.after) {
		context = append(context, fmt.Sprintf("After filter matched %s", parsed.after.Format("2006-01-02")))
	}
	if parsed.before != nil && !doc.CrawledAt.IsZero() && doc.CrawledAt.Before(parsed.before.Add(24*time.Hour)) {
		context = append(context, fmt.Sprintf("Before filter matched %s", parsed.before.Format("2006-01-02")))
	}
	for _, phrase := range parsed.phrases {
		if strings.Contains(strings.ToLower(doc.Content), phrase) || strings.Contains(strings.ToLower(doc.Title), phrase) {
			context = append(context, fmt.Sprintf("Exact phrase: %s", phrase))
		}
	}
	for _, term := range parsed.titleTerms {
		if strings.Contains(strings.ToLower(doc.Title), term) {
			context = append(context, fmt.Sprintf("Title operator matched %s", term))
		}
	}
	for _, term := range parsed.urlTerms {
		if strings.Contains(strings.ToLower(doc.URL), term) {
			context = append(context, fmt.Sprintf("URL operator matched %s", term))
		}
	}
	for _, heading := range doc.Headings {
		lowerHeading := strings.ToLower(heading)
		for _, term := range parsed.terms {
			if strings.Contains(lowerHeading, term) {
				context = append(context, fmt.Sprintf("Heading hit: %s", heading))
				if len(context) == 4 {
					return context
				}
				break
			}
		}
	}
	return uniqueContext(context)
}

func detectFileType(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := strings.ToLower(parsed.Path)
	lastSlash := strings.LastIndex(path, "/")
	lastDot := strings.LastIndex(path, ".")
	if lastDot == -1 || (lastSlash != -1 && lastDot < lastSlash) || lastDot == len(path)-1 {
		return "html"
	}
	return strings.TrimPrefix(path[lastDot:], ".")
}

func uniqueContext(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
		if len(unique) == 4 {
			break
		}
	}
	return unique
}

func paginateResults(results []model.SearchResult, offset, limit int) ([]model.SearchResult, bool) {
	if offset >= len(results) {
		return []model.SearchResult{}, false
	}
	end := min(len(results), offset+limit)
	return results[offset:end], end < len(results)
}

func (s *Service) recordSearchMetric(query string, latencyMS int64, cacheHit bool) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	s.metrics.totalQueries++
	if cacheHit {
		s.metrics.cacheHits++
	}
	s.metrics.totalLatencyMS += uint64(max64(latencyMS, 0))
	s.metrics.lastLatencyMS = latencyMS
	s.metrics.lastQueryAt = time.Now().UTC()
	s.metrics.lastCacheHit = cacheHit
	normalized := strings.TrimSpace(strings.ToLower(query))
	if normalized != "" {
		s.metrics.queryCounts[normalized]++
	}
}

func (s *Service) Analytics() AnalyticsResponse {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()

	type queryCount struct {
		query string
		count uint64
	}
	counts := make([]queryCount, 0, len(s.metrics.queryCounts))
	for query, count := range s.metrics.queryCounts {
		counts = append(counts, queryCount{query: query, count: count})
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].count == counts[j].count {
			return counts[i].query < counts[j].query
		}
		return counts[i].count > counts[j].count
	})

	items := make([]AnalyticsItem, 0, min(6, len(counts)))
	for _, item := range counts[:min(6, len(counts))] {
		items = append(items, AnalyticsItem{Query: item.query, Count: item.count})
	}

	var hitRate float64
	var avgMS float64
	if s.metrics.totalQueries > 0 {
		hitRate = float64(s.metrics.cacheHits) / float64(s.metrics.totalQueries)
		avgMS = float64(s.metrics.totalLatencyMS) / float64(s.metrics.totalQueries)
	}

	var lastQueryAt *time.Time
	if !s.metrics.lastQueryAt.IsZero() {
		timestamp := s.metrics.lastQueryAt
		lastQueryAt = &timestamp
	}

	return AnalyticsResponse{
		TotalQueries:  s.metrics.totalQueries,
		CacheHits:     s.metrics.cacheHits,
		CacheHitRate:  hitRate,
		AverageMS:     avgMS,
		LastLatencyMS: s.metrics.lastLatencyMS,
		LastCacheHit:  s.metrics.lastCacheHit,
		LastQueryAt:   lastQueryAt,
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		TopQueries:    items,
	}
}

func scoreTrust(doc model.Document, signals model.RankingSignals) model.TrustScore {
	score := 45
	reasons := make([]string, 0, 4)

	if doc.Title != "" {
		score += 10
		reasons = append(reasons, "clear title")
	}
	if doc.Description != "" {
		score += 8
		reasons = append(reasons, "has description")
	}
	if len(doc.Content) >= 160 {
		score += 10
		reasons = append(reasons, "substantial content")
	}
	if signals.TitleMatchBoost > 0 {
		score += 10
		reasons = append(reasons, "query matches title")
	}
	if signals.ExactPhraseBoost > 0 {
		score += 12
		reasons = append(reasons, "phrase match found")
	}
	if signals.DescriptionBoost > 0 {
		score += 5
	}
	if strings.HasPrefix(doc.URL, "https://") {
		score += 5
		reasons = append(reasons, "secure source")
	}
	if len(doc.Headings) > 0 {
		score += 4
		reasons = append(reasons, "structured headings")
	}
	if strings.Contains(doc.URL, "127.0.0.1") || strings.Contains(doc.URL, "localhost") {
		score -= 8
		reasons = append(reasons, "local development source")
	}
	if doc.Description == "" {
		score -= 5
	}
	if len(doc.Content) < 80 {
		score -= 10
	}

	score = max(0, min(100, score))
	level := "review"
	switch {
	case score >= 80:
		level = "high"
	case score >= 65:
		level = "good"
	}

	reason := "Limited page signals"
	if len(reasons) > 0 {
		reason = strings.Join(reasons[:min(2, len(reasons))], ", ")
	}

	return model.TrustScore{
		Value:  score,
		Level:  level,
		Reason: reason,
	}
}

func buildExplanations(doc model.Document, signals model.RankingSignals) []model.ResultExplain {
	explanations := []model.ResultExplain{
		{Label: "Base rank", Value: fmt.Sprintf("%.2f BM25-style score", signals.BaseScore)},
	}
	if signals.AuthorityBoost > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Authority",
			Value: fmt.Sprintf("+%.2f from linked-page authority", signals.AuthorityBoost),
		})
	}
	if signals.HeadingMatchBoost > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Heading match",
			Value: fmt.Sprintf("+%.2f from section-heading overlap", signals.HeadingMatchBoost),
		})
	}
	if signals.TitleMatchBoost > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Title match",
			Value: fmt.Sprintf("+%.2f from title keyword overlap", signals.TitleMatchBoost),
		})
	}
	if signals.ExactPhraseBoost > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Phrase match",
			Value: fmt.Sprintf("+%.2f from exact phrase relevance", signals.ExactPhraseBoost),
		})
	}
	if signals.DescriptionBoost > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Description / URL",
			Value: fmt.Sprintf("+%.2f from description and URL overlap", signals.DescriptionBoost),
		})
	}
	if len(doc.Headings) > 0 {
		explanations = append(explanations, model.ResultExplain{
			Label: "Structure",
			Value: fmt.Sprintf("%d extracted heading(s) available for sitelinks", len(doc.Headings)),
		})
	}
	if doc.Description == "" {
		explanations = append(explanations, model.ResultExplain{
			Label: "Metadata gap",
			Value: "Missing meta description lowers confidence",
		})
	}
	return explanations
}

func summarizeQuality(results []model.SearchResult) model.SearchQuality {
	if len(results) == 0 {
		return model.SearchQuality{
			TopTrustLevel: "review",
		}
	}

	totalTrust := 0
	trusted := 0
	review := 0
	bestLevel := "review"
	highlightCounts := map[string]int{}

	for _, result := range results {
		totalTrust += result.Trust.Value
		if result.Trust.Level == "high" || result.Trust.Level == "good" {
			trusted++
		} else {
			review++
		}
		if trustPriority(result.Trust.Level) > trustPriority(bestLevel) {
			bestLevel = result.Trust.Level
		}
		for _, explanation := range result.Explanations {
			highlightCounts[explanation.Label]++
		}
	}

	type highlight struct {
		label string
		count int
	}
	highlights := make([]highlight, 0, len(highlightCounts))
	for label, count := range highlightCounts {
		highlights = append(highlights, highlight{label: label, count: count})
	}
	sort.Slice(highlights, func(i, j int) bool {
		if highlights[i].count == highlights[j].count {
			return highlights[i].label < highlights[j].label
		}
		return highlights[i].count > highlights[j].count
	})

	summaryHighlights := make([]string, 0, min(3, len(highlights)))
	for _, item := range highlights[:min(3, len(highlights))] {
		summaryHighlights = append(summaryHighlights, fmt.Sprintf("%s across %d result(s)", item.label, item.count))
	}

	return model.SearchQuality{
		AverageTrust:   totalTrust / len(results),
		TopTrustLevel:  bestLevel,
		TrustedResults: trusted,
		NeedsReview:    review,
		Highlights:     summaryHighlights,
	}
}

func trustPriority(level string) int {
	switch level {
	case "high":
		return 3
	case "good":
		return 2
	default:
		return 1
	}
}

func (s *Service) Stats() map[string]int {
	stats := s.index.Stats()
	stats["documents"] = s.store.Count()
	stats["stale_documents"] = s.store.StaleCount(time.Now().UTC())

	s.jobsMu.RLock()
	stats["crawl_jobs"] = len(s.jobs)
	s.jobsMu.RUnlock()
	stats["deduplicated_urls"] = int(atomic.LoadUint64(&s.dedupCount))

	return stats
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
			return trimSnippetWindow(content, idx, idx+len(needle))
		}
	}

	for _, term := range index.Tokenize(query) {
		if idx := strings.Index(haystack, term); idx >= 0 {
			return trimSnippetWindow(content, idx, idx+len(term))
		}
	}

	if len(content) <= 200 {
		return content
	}
	return strings.TrimSpace(content[:200]) + "..."
}

func trimSnippetWindow(content string, start, end int) string {
	windowStart := max(0, start-90)
	windowEnd := min(len(content), end+150)
	snippet := strings.TrimSpace(content[windowStart:windowEnd])
	if windowStart > 0 {
		snippet = "..." + strings.TrimLeft(snippet, " .,")
	}
	if windowEnd < len(content) {
		snippet = strings.TrimRight(snippet, " .,") + "..."
	}
	return snippet
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

func buildRelatedQueries(query string, results []model.SearchResult, suggestions []string) []string {
	query = strings.ToLower(strings.TrimSpace(query))
	queryTerms := make(map[string]struct{})
	for _, term := range index.Tokenize(query) {
		queryTerms[term] = struct{}{}
	}

	candidateScores := make(map[string]int)
	for _, suggestion := range suggestions {
		suggestion = strings.TrimSpace(strings.ToLower(suggestion))
		if isDistinctSuggestion(query, suggestion) {
			candidateScores[suggestion] += 6
		}
	}

	for _, result := range results {
		phrases := append(extractRelatedPhrases(result.Title), extractRelatedPhrases(result.Description)...)
		for _, phrase := range phrases {
			if !isDistinctSuggestion(query, phrase) {
				continue
			}
			score := 2
			for _, term := range strings.Fields(phrase) {
				if _, exists := queryTerms[term]; exists {
					score++
				}
			}
			candidateScores[phrase] += score
		}
	}

	type candidate struct {
		query string
		score int
	}

	candidates := make([]candidate, 0, len(candidateScores))
	for suggestion, score := range candidateScores {
		candidates = append(candidates, candidate{query: suggestion, score: score})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].query < candidates[j].query
		}
		return candidates[i].score > candidates[j].score
	})

	related := make([]string, 0, min(6, len(candidates)))
	for _, candidate := range candidates {
		related = append(related, candidate.query)
		if len(related) == 6 {
			break
		}
	}

	return related
}

func extractRelatedPhrases(text string) []string {
	terms := index.Tokenize(text)
	if len(terms) < 2 {
		return nil
	}

	phrases := make([]string, 0, len(terms))
	for size := 2; size <= 3; size++ {
		for start := 0; start+size <= len(terms); start++ {
			phrases = append(phrases, strings.Join(terms[start:start+size], " "))
		}
	}
	return phrases
}

func isDistinctSuggestion(query, candidate string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	candidate = strings.TrimSpace(strings.ToLower(candidate))
	if candidate == "" || candidate == query {
		return false
	}

	queryTerms := index.Tokenize(query)
	candidateTerms := index.Tokenize(candidate)
	if len(candidateTerms) == 0 {
		return false
	}
	if len(strings.Fields(query)) > 1 && len(candidateTerms) <= len(strings.Fields(query)) {
		return false
	}
	if len(candidateTerms) == len(queryTerms) {
		matches := 0
		for idx := range candidateTerms {
			if idx < len(queryTerms) && candidateTerms[idx] == queryTerms[idx] {
				matches++
			}
		}
		if matches == len(queryTerms) {
			return false
		}
	}

	return true
}

func (s *Service) invalidateSearchCache() {
	if s.cache == nil {
		return
	}
	_ = s.cache.DeleteByPrefix(context.Background(), "search:")
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

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
