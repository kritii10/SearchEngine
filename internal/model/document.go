package model

import "time"

type Document struct {
	ID                 string    `json:"id"`
	URL                string    `json:"url"`
	Domain             string    `json:"domain"`
	Links              []string  `json:"links"`
	Headings           []string  `json:"headings"`
	Title              string    `json:"title"`
	Description        string    `json:"description"`
	Content            string    `json:"content"`
	Terms              []string  `json:"terms"`
	ContentFingerprint string    `json:"content_fingerprint"`
	CrawledAt          time.Time `json:"crawled_at"`
	RecrawlAfter       time.Time `json:"recrawl_after"`
}

type SearchResult struct {
	DocumentID   string          `json:"document_id"`
	URL          string          `json:"url"`
	Domain       string          `json:"domain"`
	FileType     string          `json:"file_type,omitempty"`
	Title        string          `json:"title"`
	Snippet      string          `json:"snippet"`
	SiteLinks    []string        `json:"site_links,omitempty"`
	MatchContext []string        `json:"match_context,omitempty"`
	Score        float64         `json:"score"`
	Description  string          `json:"description"`
	CrawledAt    time.Time       `json:"crawled_at"`
	RecrawlAfter time.Time       `json:"recrawl_after"`
	Signals      RankingSignals  `json:"signals"`
	Trust        TrustScore      `json:"trust"`
	Explanations []ResultExplain `json:"explanations,omitempty"`
}

type AnswerSummary struct {
	Query            string     `json:"query"`
	Summary          string     `json:"summary"`
	GroundedPoints   []string   `json:"grounded_points"`
	Citations        []Citation `json:"citations,omitempty"`
	RelatedQuestions []string   `json:"related_questions,omitempty"`
	Generated        bool       `json:"generated"`
}

type Citation struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type RankingSignals struct {
	BaseScore         float64 `json:"base_score"`
	AuthorityBoost    float64 `json:"authority_boost"`
	HeadingMatchBoost float64 `json:"heading_match_boost"`
	TitleMatchBoost   float64 `json:"title_match_boost"`
	ExactPhraseBoost  float64 `json:"exact_phrase_boost"`
	DescriptionBoost  float64 `json:"description_boost"`
	FreshnessBoost    float64 `json:"freshness_boost"`
	CombinedScoreHint float64 `json:"combined_score_hint"`
}

type TrustScore struct {
	Value  int    `json:"value"`
	Level  string `json:"level"`
	Reason string `json:"reason"`
}

type ResultExplain struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type SearchQuality struct {
	AverageTrust   int      `json:"average_trust"`
	TopTrustLevel  string   `json:"top_trust_level"`
	TrustedResults int      `json:"trusted_results"`
	NeedsReview    int      `json:"needs_review"`
	Highlights     []string `json:"highlights,omitempty"`
}
