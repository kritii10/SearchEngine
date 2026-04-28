package search

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"atlas-search/internal/cache"
	"atlas-search/internal/index"
	"atlas-search/internal/model"
	"atlas-search/internal/store"
)

func TestSearchBoostsTitleAndPhraseMatches(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:          "doc-1",
			URL:         "https://example.com/go-search",
			Title:       "Go Search Engine",
			Description: "Build a search engine in Go",
			Content:     "This guide explains how to build a search engine in Go with ranking and indexing.",
			Terms:       index.Tokenize("Go Search Engine Build a search engine in Go This guide explains how to build a search engine in Go with ranking and indexing"),
		},
		{
			ID:          "doc-2",
			URL:         "https://example.com/distributed-systems",
			Title:       "Distributed Systems Notes",
			Description: "Search architecture notes",
			Content:     "These notes mention search once, but mostly discuss distributed systems tradeoffs.",
			Terms:       index.Tokenize("Distributed Systems Notes Search architecture notes These notes mention search once but mostly discuss distributed systems tradeoffs"),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{
		documents: map[string]model.Document{
			"https://example.com": {
				ID:          "doc-2",
				URL:         "https://example.com",
				Title:       "Example Search",
				Description: "Example document",
				Content:     "Search engines crawl and rank documents.",
				Terms:       index.Tokenize("Example Search Example document Search engines crawl and rank documents"),
			},
		},
	})

	results, err := service.Search("go search engine", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	if results[0].DocumentID != "doc-1" {
		t.Fatalf("expected doc-1 to rank first, got %s", results[0].DocumentID)
	}

	if results[0].Signals.TitleMatchBoost <= 0 {
		t.Fatalf("expected title match boost, got %+v", results[0].Signals)
	}

	if results[0].Signals.ExactPhraseBoost <= 0 {
		t.Fatalf("expected exact phrase boost, got %+v", results[0].Signals)
	}
}

func TestBuildSnippetPrefersMatchingRegion(t *testing.T) {
	content := "Distributed indexing is useful. The best search engine needs ranking, snippets, and cache-aware query handling for good latency."

	snippet := buildSnippet(content, "search engine")
	if snippet == "" {
		t.Fatal("expected non-empty snippet")
	}

	if !strings.Contains(strings.ToLower(snippet), "search engine") {
		t.Fatalf("expected snippet to contain search engine, got %q", snippet)
	}
}

func TestSearchWithAnswerIncludesRelatedQueriesAndTiming(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:          "doc-1",
			URL:         "https://example.com/go-search-engine",
			Title:       "Go Search Engine",
			Description: "Go search engine tutorial",
			Content:     "Go search engine ranking signals and snippets.",
			Terms:       index.Tokenize("Go Search Engine Go search engine tutorial Go search engine ranking signals and snippets"),
		},
		{
			ID:          "doc-2",
			URL:         "https://example.com/go-search-dashboard",
			Title:       "Go Search Dashboard",
			Description: "Premium search dashboard",
			Content:     "Go search dashboard for ranking analytics and premium search views.",
			Terms:       index.Tokenize("Go Search Dashboard Premium search dashboard Go search dashboard for ranking analytics"),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithAnswer(t.Context(), "go se", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if response.TookMS < 0 {
		t.Fatalf("expected non-negative took ms, got %d", response.TookMS)
	}
	if len(response.RelatedQueries) == 0 {
		t.Fatalf("expected related queries, got %+v", response)
	}
	if response.RelatedQueries[0] != "go search engine" {
		t.Fatalf("expected phrase suggestion to lead related queries, got %#v", response.RelatedQueries)
	}
	if response.Quality.AverageTrust == 0 {
		t.Fatalf("expected quality summary, got %+v", response.Quality)
	}
	if len(response.Results[0].Explanations) == 0 {
		t.Fatalf("expected explanations on result, got %+v", response.Results[0])
	}
	if response.Results[0].Trust.Level == "" || response.Results[0].Trust.Value == 0 {
		t.Fatalf("expected trust score on result, got %+v", response.Results[0].Trust)
	}
}

func TestStatsIncludesIndexAndJobCounts(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:          "doc-1",
		URL:         "https://example.com/atlas",
		Title:       "Atlas Search Premium Dashboard",
		Description: "Search analytics",
		Content:     "Atlas Search premium dashboard with analytics.",
		Terms:       index.Tokenize("Atlas Search Premium Dashboard Search analytics Atlas Search premium dashboard with analytics"),
	}
	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewService(documentStore, searchIndex, stubFetcher{
		documents: map[string]model.Document{
			"https://example.com": {
				ID:          "doc-2",
				URL:         "https://example.com",
				Title:       "Example Search",
				Description: "Example document",
				Content:     "Search engines crawl and rank documents.",
				Terms:       index.Tokenize("Example Search Example document Search engines crawl and rank documents"),
			},
		},
	})
	if _, err := service.EnqueueCrawl([]string{"https://example.com"}); err != nil {
		t.Fatalf("enqueue crawl: %v", err)
	}

	stats := service.Stats()
	if stats["documents"] != 1 {
		t.Fatalf("expected document count in stats, got %+v", stats)
	}
	if stats["terms"] == 0 || stats["phrases"] == 0 {
		t.Fatalf("expected index stats, got %+v", stats)
	}
	if stats["crawl_jobs"] != 1 {
		t.Fatalf("expected crawl job count, got %+v", stats)
	}
}

func TestSearchQualityFlagsMetadataGapsForReview(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:      "doc-1",
		URL:     "http://localhost:18081/thin",
		Title:   "Thin Search Result",
		Content: "short search note",
		Terms:   index.Tokenize("Thin Search Result short search note"),
	}
	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithAnswer(t.Context(), "search", 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(response.Results))
	}
	if response.Results[0].Trust.Level != "review" {
		t.Fatalf("expected review trust level, got %+v", response.Results[0].Trust)
	}
	if response.Quality.NeedsReview != 1 {
		t.Fatalf("expected quality summary to flag review, got %+v", response.Quality)
	}
}

func TestSearchWithOptionsFiltersByMinTrust(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:                 "doc-high",
			URL:                "https://example.com/high",
			Title:              "Premium Search Intelligence",
			Description:        "Strong metadata",
			Content:            "Premium search intelligence with ranking signals and trusted explanations.",
			Terms:              index.Tokenize("Premium Search Intelligence Strong metadata Premium search intelligence with ranking signals and trusted explanations"),
			ContentFingerprint: "fp-high",
			CrawledAt:          time.Now().UTC(),
		},
		{
			ID:                 "doc-low",
			URL:                "http://localhost:18081/low",
			Title:              "Search Note",
			Content:            "short search note",
			Terms:              index.Tokenize("Search Note short search note"),
			ContentFingerprint: "fp-low",
			CrawledAt:          time.Now().Add(-time.Hour).UTC(),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "search", 10, SearchOptions{MinTrust: 80})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) != 1 {
		t.Fatalf("expected only high-trust result, got %+v", response.Results)
	}
	if response.Results[0].DocumentID != "doc-high" {
		t.Fatalf("expected high-trust result, got %+v", response.Results[0])
	}
	if response.MinTrust != 80 {
		t.Fatalf("expected response to echo min trust, got %+v", response)
	}
}

func TestSearchWithOptionsSortsByRecent(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	older := model.Document{
		ID:                 "doc-old",
		URL:                "https://example.com/old",
		Title:              "Search Archive",
		Description:        "Older result",
		Content:            "Search archive and ranking notes.",
		Terms:              index.Tokenize("Search Archive Older result Search archive and ranking notes"),
		ContentFingerprint: "fp-old",
		CrawledAt:          time.Now().Add(-2 * time.Hour).UTC(),
	}
	newer := model.Document{
		ID:                 "doc-new",
		URL:                "https://example.com/new",
		Title:              "Search Archive",
		Description:        "Newer result",
		Content:            "Search archive and ranking notes.",
		Terms:              index.Tokenize("Search Archive Newer result Search archive and ranking notes"),
		ContentFingerprint: "fp-new",
		CrawledAt:          time.Now().UTC(),
	}

	for _, doc := range []model.Document{older, newer} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "search archive", 10, SearchOptions{SortBy: "recent"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) < 2 {
		t.Fatalf("expected both results, got %+v", response.Results)
	}
	if response.Results[0].DocumentID != "doc-new" {
		t.Fatalf("expected newest result first, got %+v", response.Results)
	}
	if response.SortBy != "recent" {
		t.Fatalf("expected response to echo sort, got %+v", response)
	}
}

func TestFreshnessBoostPrefersNewerDocument(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	oldDoc := model.Document{
		ID:                 "doc-old",
		URL:                "https://example.com/old",
		Domain:             "example.com",
		Title:              "Premium Search Update",
		Description:        "Older search update",
		Content:            "Premium search update and ranking notes.",
		Terms:              index.Tokenize("Premium Search Update Older search update Premium search update and ranking notes"),
		ContentFingerprint: "old-fp",
		CrawledAt:          time.Now().Add(-10 * 24 * time.Hour).UTC(),
	}
	newDoc := oldDoc
	newDoc.ID = "doc-new"
	newDoc.URL = "https://example.com/new"
	newDoc.ContentFingerprint = "new-fp"
	newDoc.CrawledAt = time.Now().UTC()

	for _, doc := range []model.Document{oldDoc, newDoc} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "premium search update", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("expected 2 results, got %+v", response.Results)
	}
	if response.Results[0].DocumentID != "doc-new" {
		t.Fatalf("expected fresher result first, got %+v", response.Results)
	}
	if response.Results[0].Signals.FreshnessBoost <= response.Results[1].Signals.FreshnessBoost {
		t.Fatalf("expected larger freshness boost for recent doc, got %+v", response.Results)
	}
}

func TestAuthorityBoostPrefersLinkedDocument(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	target := model.Document{
		ID:                 "doc-target",
		URL:                "https://example.com/target",
		Domain:             "example.com",
		Title:              "Search Target",
		Description:        "Target result",
		Content:            "Search target explanation and ranking.",
		Terms:              index.Tokenize("Search Target Target result Search target explanation and ranking"),
		ContentFingerprint: "fp-target",
		CrawledAt:          time.Now().UTC(),
	}
	source := model.Document{
		ID:                 "doc-source",
		URL:                "https://example.com/source",
		Domain:             "example.com",
		Title:              "Search Source",
		Description:        "Source result",
		Content:            "Search source explanation and ranking.",
		Terms:              index.Tokenize("Search Source Source result Search source explanation and ranking"),
		Links:              []string{target.URL},
		ContentFingerprint: "fp-source",
		CrawledAt:          time.Now().UTC(),
	}

	for _, doc := range []model.Document{target, source} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "search", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) < 2 {
		t.Fatalf("expected two results, got %+v", response.Results)
	}
	if response.Results[0].Signals.AuthorityBoost < response.Results[1].Signals.AuthorityBoost {
		t.Fatalf("expected linked target to gain more authority, got %+v", response.Results)
	}
}

func TestSearchWithOptionsFiltersByDomainAndReturnsFacets(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:                 "doc-1",
			URL:                "https://alpha.example.com/a",
			Domain:             "alpha.example.com",
			Title:              "Premium Search Alpha",
			Description:        "Alpha page",
			Content:            "Premium search alpha ranking notes.",
			Terms:              index.Tokenize("Premium Search Alpha Alpha page Premium search alpha ranking notes"),
			ContentFingerprint: "fp-a",
			CrawledAt:          time.Now().UTC(),
		},
		{
			ID:                 "doc-2",
			URL:                "https://beta.example.com/b",
			Domain:             "beta.example.com",
			Title:              "Premium Search Beta",
			Description:        "Beta page",
			Content:            "Premium search beta ranking notes.",
			Terms:              index.Tokenize("Premium Search Beta Beta page Premium search beta ranking notes"),
			ContentFingerprint: "fp-b",
			CrawledAt:          time.Now().UTC(),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "premium search", 10, SearchOptions{Domain: "beta.example.com"})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) != 1 || response.Results[0].Domain != "beta.example.com" {
		t.Fatalf("expected only beta results, got %+v", response.Results)
	}
	if len(response.Domains) != 1 || response.Domains[0].Domain != "beta.example.com" {
		t.Fatalf("expected filtered domain facet, got %+v", response.Domains)
	}
}

func TestSearchWithOptionsPaginatesResults(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	for idx := 0; idx < 3; idx++ {
		doc := model.Document{
			ID:                 fmt.Sprintf("doc-%d", idx),
			URL:                fmt.Sprintf("https://example.com/%d", idx),
			Domain:             fmt.Sprintf("site-%d.example.com", idx),
			Title:              fmt.Sprintf("Premium Search %d", idx),
			Description:        "Paged result",
			Content:            "Premium search ranking and trusted explanations.",
			Terms:              index.Tokenize(fmt.Sprintf("Premium Search %d Paged result Premium search ranking and trusted explanations", idx)),
			ContentFingerprint: fmt.Sprintf("fp-%d", idx),
			CrawledAt:          time.Now().Add(time.Duration(idx) * time.Minute).UTC(),
		}
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "premium search", 2, SearchOptions{Offset: 1})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if response.Count != 3 {
		t.Fatalf("expected total count 3, got %+v", response)
	}
	if len(response.Results) != 2 {
		t.Fatalf("expected paged results, got %+v", response.Results)
	}
	if response.Offset != 1 || response.Limit != 2 {
		t.Fatalf("expected pagination echo, got %+v", response)
	}
	if response.HasMore {
		t.Fatalf("expected no extra page at offset 1, got %+v", response)
	}
}

func TestSearchWithOptionsCorrectsTypoQuery(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:                 "doc-1",
		URL:                "https://example.com/premium",
		Domain:             "example.com",
		Title:              "Premium Search",
		Description:        "Search quality",
		Content:            "Premium search intelligence and ranking.",
		Terms:              index.Tokenize("Premium Search Search quality Premium search intelligence and ranking"),
		ContentFingerprint: "fp-1",
		CrawledAt:          time.Now().UTC(),
	}
	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "premum searh", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if response.CorrectedQuery != "premium search" {
		t.Fatalf("expected corrected query, got %+v", response)
	}
	if response.Count != 1 {
		t.Fatalf("expected corrected search to find result, got %+v", response)
	}
}

func TestSearchWithOptionsParsesSitePhraseAndExclusionOperators(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:                 "doc-1",
			URL:                "https://docs.example.com/guide",
			Domain:             "docs.example.com",
			Title:              "Premium Search Guide",
			Description:        "Architecture overview",
			Content:            "This premium search guide explains indexing and ranking architecture in detail.",
			Terms:              index.Tokenize("Premium Search Guide Architecture overview This premium search guide explains indexing and ranking architecture in detail"),
			ContentFingerprint: "fp-guide",
			CrawledAt:          time.Now().UTC(),
		},
		{
			ID:                 "doc-2",
			URL:                "https://blog.example.com/guide",
			Domain:             "blog.example.com",
			Title:              "Premium Search Guide",
			Description:        "Blog post",
			Content:            "This premium search guide focuses on ranking and ads.",
			Terms:              index.Tokenize("Premium Search Guide Blog post This premium search guide focuses on ranking and ads"),
			ContentFingerprint: "fp-blog",
			CrawledAt:          time.Now().UTC(),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), `site:example.com "premium search guide" -ads`, 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) != 1 || response.Results[0].Domain != "docs.example.com" {
		t.Fatalf("expected filtered operator result, got %+v", response.Results)
	}
	if response.Interpretation.Site != "example.com" {
		t.Fatalf("expected parsed site operator, got %+v", response.Interpretation)
	}
	if len(response.Interpretation.Phrases) != 1 || response.Interpretation.Phrases[0] != "premium search guide" {
		t.Fatalf("expected parsed phrase operator, got %+v", response.Interpretation)
	}
	if len(response.Interpretation.ExcludeTerms) != 1 || response.Interpretation.ExcludeTerms[0] != "ads" {
		t.Fatalf("expected parsed exclusion operator, got %+v", response.Interpretation)
	}
	if response.Interpretation.Intent != "navigational" {
		t.Fatalf("expected navigational intent, got %+v", response.Interpretation)
	}
}

func TestSearchWithOptionsSupportsTitleAndURLOperators(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:                 "doc-1",
			URL:                "https://example.com/guide/search",
			Domain:             "example.com",
			Title:              "Premium Search Guide",
			Description:        "Structured doc",
			Content:            "Search guide content.",
			Headings:           []string{"Premium Search Architecture", "Ranking Signals"},
			Terms:              index.Tokenize("Premium Search Guide Structured doc Search guide content"),
			ContentFingerprint: "fp-1",
			CrawledAt:          time.Now().UTC(),
		},
		{
			ID:                 "doc-2",
			URL:                "https://example.com/blog/overview",
			Domain:             "example.com",
			Title:              "Platform Overview",
			Description:        "General doc",
			Content:            "Platform overview content.",
			Terms:              index.Tokenize("Platform Overview General doc Platform overview content"),
			ContentFingerprint: "fp-2",
			CrawledAt:          time.Now().UTC(),
		},
	}
	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), `intitle:premium inurl:search guide`, 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].DocumentID != "doc-1" {
		t.Fatalf("expected operator-filtered result, got %+v", response.Results)
	}
	if len(response.Results[0].SiteLinks) == 0 {
		t.Fatalf("expected sitelinks from headings, got %+v", response.Results[0])
	}
	if len(response.Results[0].MatchContext) == 0 {
		t.Fatalf("expected match context, got %+v", response.Results[0])
	}
	if len(response.Interpretation.TitleTerms) != 1 || len(response.Interpretation.URLTerms) != 1 {
		t.Fatalf("expected parsed title/url operators, got %+v", response.Interpretation)
	}
}

func TestSearchWithOptionsSupportsFiletypeAndDateOperators(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	newer := model.Document{
		ID:                 "doc-1",
		URL:                "https://example.com/report.pdf",
		Domain:             "example.com",
		Title:              "Premium Search Report",
		Description:        "Fresh PDF",
		Content:            "Premium search report content.",
		Terms:              index.Tokenize("Premium Search Report Fresh PDF Premium search report content"),
		ContentFingerprint: "fp-1",
		CrawledAt:          time.Date(2026, 4, 18, 10, 0, 0, 0, time.UTC),
	}
	older := model.Document{
		ID:                 "doc-2",
		URL:                "https://example.com/archive.html",
		Domain:             "example.com",
		Title:              "Premium Search Archive",
		Description:        "Old HTML",
		Content:            "Premium search archive content.",
		Terms:              index.Tokenize("Premium Search Archive Old HTML Premium search archive content"),
		ContentFingerprint: "fp-2",
		CrawledAt:          time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC),
	}
	for _, doc := range []model.Document{newer, older} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), `premium search filetype:pdf after:2026-04-15 before:2026-04-19`, 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].FileType != "pdf" {
		t.Fatalf("expected filetype/date filtered result, got %+v", response.Results)
	}
	if response.Interpretation.After != "2026-04-15" || response.Interpretation.Before != "2026-04-19" {
		t.Fatalf("expected parsed date operators, got %+v", response.Interpretation)
	}
	if len(response.Interpretation.FileTypes) != 1 || response.Interpretation.FileTypes[0] != "pdf" {
		t.Fatalf("expected parsed filetype operator, got %+v", response.Interpretation)
	}
}

func TestSearchWithOptionsDiversifiesDomainsForRelevance(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{ID: "a1", URL: "https://alpha.example.com/1", Domain: "alpha.example.com", Title: "Search Alpha One", Content: "search alpha", Terms: index.Tokenize("Search Alpha One search alpha"), ContentFingerprint: "a1"},
		{ID: "a2", URL: "https://alpha.example.com/2", Domain: "alpha.example.com", Title: "Search Alpha Two", Content: "search alpha", Terms: index.Tokenize("Search Alpha Two search alpha"), ContentFingerprint: "a2"},
		{ID: "a3", URL: "https://alpha.example.com/3", Domain: "alpha.example.com", Title: "Search Alpha Three", Content: "search alpha", Terms: index.Tokenize("Search Alpha Three search alpha"), ContentFingerprint: "a3"},
		{ID: "b1", URL: "https://beta.example.com/1", Domain: "beta.example.com", Title: "Search Beta One", Content: "search beta", Terms: index.Tokenize("Search Beta One search beta"), ContentFingerprint: "b1"},
	}
	for _, doc := range docs {
		doc.CrawledAt = time.Now().UTC()
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "search", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	alphaCount := 0
	for _, result := range response.Results {
		if result.Domain == "alpha.example.com" {
			alphaCount++
		}
	}
	if alphaCount > 2 {
		t.Fatalf("expected diversified domains, got %+v", response.Results)
	}
}

func TestSearchWithOptionsMarksCachedResponses(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	doc := model.Document{
		ID:                 "doc-1",
		URL:                "https://example.com/cache",
		Domain:             "example.com",
		Title:              "Cache Friendly Search",
		Description:        "Fast result",
		Content:            "Cache friendly search engine result.",
		Terms:              index.Tokenize("Cache Friendly Search Fast result Cache friendly search engine result"),
		ContentFingerprint: "fp-cache",
		CrawledAt:          time.Now().UTC(),
	}
	if err := documentStore.Upsert(doc); err != nil {
		t.Fatalf("upsert document: %v", err)
	}
	searchIndex.Add(doc)

	service := NewServiceWithDependencies(documentStore, searchIndex, stubFetcher{}, nil, cache.NewMemoryCache())

	first, err := service.SearchWithOptions(t.Context(), "cache search", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("first search failed: %v", err)
	}
	second, err := service.SearchWithOptions(t.Context(), "cache search", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("second search failed: %v", err)
	}

	if first.Cached {
		t.Fatalf("expected fresh response to not be cached, got %+v", first)
	}
	if !second.Cached {
		t.Fatalf("expected second response to be marked cached, got %+v", second)
	}
}

func TestSearchWithOptionsBuildsCoverageTimelineAndCompare(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	now := time.Now().UTC()

	docs := []model.Document{
		{
			ID:                 "doc-1",
			URL:                "https://alpha.example.com/guide",
			Domain:             "alpha.example.com",
			Title:              "Premium Search Guide",
			Description:        "Best match",
			Content:            "Premium search guide with ranking and authority details.",
			Terms:              index.Tokenize("Premium Search Guide Best match Premium search guide with ranking and authority details"),
			ContentFingerprint: "fp-1",
			CrawledAt:          now,
		},
		{
			ID:                 "doc-2",
			URL:                "https://alpha.example.com/fresh",
			Domain:             "alpha.example.com",
			Title:              "Fresh Search Update",
			Description:        "Recent result",
			Content:            "Fresh search update with ranking details.",
			Terms:              index.Tokenize("Fresh Search Update Recent result Fresh search update with ranking details"),
			ContentFingerprint: "fp-2",
			CrawledAt:          now.Add(-48 * time.Hour),
		},
		{
			ID:                 "doc-3",
			URL:                "https://beta.example.com/notes",
			Domain:             "beta.example.com",
			Title:              "Search Notes",
			Description:        "Archive result",
			Content:            "Search notes with ranking details.",
			Terms:              index.Tokenize("Search Notes Archive result Search notes with ranking details"),
			ContentFingerprint: "fp-3",
			CrawledAt:          now.Add(-40 * 24 * time.Hour),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "search", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Coverage) == 0 || response.Coverage[0].Domain != "alpha.example.com" {
		t.Fatalf("expected source coverage, got %+v", response.Coverage)
	}
	if len(response.Timeline) != 4 {
		t.Fatalf("expected timeline buckets, got %+v", response.Timeline)
	}
	if response.Compare == nil || len(response.Compare.Items) < 2 {
		t.Fatalf("expected compare card, got %+v", response.Compare)
	}
}

func TestSearchBoostsHeadingMatchesAndTracksStaleDocs(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	oldDoc := model.Document{
		ID:                 "doc-1",
		URL:                "https://example.com/guide",
		Domain:             "example.com",
		Title:              "Search Platform",
		Description:        "Guide",
		Headings:           []string{"Premium Search Architecture"},
		Content:            "Platform guide with indexing notes.",
		Terms:              index.Tokenize("Search Platform Guide Platform guide with indexing notes"),
		ContentFingerprint: "fp-1",
		CrawledAt:          time.Now().Add(-48 * time.Hour).UTC(),
		RecrawlAfter:       time.Now().Add(-time.Hour).UTC(),
	}
	plainDoc := model.Document{
		ID:                 "doc-2",
		URL:                "https://example.com/plain",
		Domain:             "example.com",
		Title:              "Search Platform",
		Description:        "Plain page",
		Content:            "Platform page with indexing notes.",
		Terms:              index.Tokenize("Search Platform Plain page Platform page with indexing notes"),
		ContentFingerprint: "fp-2",
		CrawledAt:          time.Now().UTC(),
		RecrawlAfter:       time.Now().Add(24 * time.Hour).UTC(),
	}

	for _, doc := range []model.Document{oldDoc, plainDoc} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "premium search architecture", 10, SearchOptions{})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	if len(response.Results) == 0 || response.Results[0].Signals.HeadingMatchBoost <= 0 {
		t.Fatalf("expected heading boost on best result, got %+v", response.Results)
	}
	stats := service.Stats()
	if stats["stale_documents"] != 1 {
		t.Fatalf("expected stale document count, got %+v", stats)
	}
}

func TestListStaleDocumentsAndEnqueueRecrawlDue(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	now := time.Now().UTC()

	stale := model.Document{
		ID:                 "doc-stale",
		URL:                "https://example.com/stale",
		Domain:             "example.com",
		Title:              "Stale Search Guide",
		Content:            "Stale content",
		Terms:              index.Tokenize("Stale Search Guide Stale content"),
		ContentFingerprint: "fp-stale",
		CrawledAt:          now.Add(-72 * time.Hour),
		RecrawlAfter:       now.Add(-2 * time.Hour),
	}
	fresh := model.Document{
		ID:                 "doc-fresh",
		URL:                "https://example.com/fresh",
		Domain:             "example.com",
		Title:              "Fresh Search Guide",
		Content:            "Fresh content",
		Terms:              index.Tokenize("Fresh Search Guide Fresh content"),
		ContentFingerprint: "fp-fresh",
		CrawledAt:          now,
		RecrawlAfter:       now.Add(48 * time.Hour),
	}

	for _, doc := range []model.Document{stale, fresh} {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{
		documents: map[string]model.Document{
			stale.URL: stale,
		},
	})
	candidates := service.ListStaleDocuments(10)
	if len(candidates) != 1 || candidates[0].URL != stale.URL {
		t.Fatalf("expected one stale candidate, got %+v", candidates)
	}

	job, err := service.EnqueueRecrawlDue(10)
	if err != nil {
		t.Fatalf("enqueue recrawl due: %v", err)
	}
	if len(job.URLs) != 1 || job.URLs[0] != stale.URL {
		t.Fatalf("expected stale url in recrawl job, got %+v", job)
	}
}

func TestSearchWithTrustedOnlyFiltersToTrustedHosts(t *testing.T) {
	documentStore := store.NewMemoryStore()
	searchIndex := index.New()

	docs := []model.Document{
		{
			ID:                 "doc-gov",
			URL:                "https://www.cdc.gov/search",
			Domain:             "www.cdc.gov",
			Title:              "Public Health Search",
			Description:        "Trusted source",
			Content:            "Health search guidance and public health information.",
			Terms:              index.Tokenize("Public Health Search Trusted source Health search guidance and public health information"),
			ContentFingerprint: "fp-gov",
			CrawledAt:          time.Now().UTC(),
		},
		{
			ID:                 "doc-blog",
			URL:                "https://example.com/search",
			Domain:             "example.com",
			Title:              "Blog Search",
			Description:        "General source",
			Content:            "Health search commentary from a blog.",
			Terms:              index.Tokenize("Blog Search General source Health search commentary from a blog"),
			ContentFingerprint: "fp-blog",
			CrawledAt:          time.Now().UTC(),
		},
	}

	for _, doc := range docs {
		if err := documentStore.Upsert(doc); err != nil {
			t.Fatalf("upsert document: %v", err)
		}
		searchIndex.Add(doc)
	}

	service := NewService(documentStore, searchIndex, stubFetcher{})
	response, err := service.SearchWithOptions(t.Context(), "health search", 10, SearchOptions{TrustedOnly: true})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Domain != "www.cdc.gov" {
		t.Fatalf("expected only trusted source result, got %+v", response.Results)
	}
	if !response.TrustedOnly || response.MinTrust < 80 {
		t.Fatalf("expected trusted-only constraints, got %+v", response)
	}
}
