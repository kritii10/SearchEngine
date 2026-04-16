package model

import "time"

type Document struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Content     string    `json:"content"`
	Terms       []string  `json:"terms"`
	CrawledAt   time.Time `json:"crawled_at"`
}

type SearchResult struct {
	DocumentID  string         `json:"document_id"`
	URL         string         `json:"url"`
	Title       string         `json:"title"`
	Snippet     string         `json:"snippet"`
	Score       float64        `json:"score"`
	Description string         `json:"description"`
	Signals     RankingSignals `json:"signals"`
}

type RankingSignals struct {
	BaseScore         float64 `json:"base_score"`
	TitleMatchBoost   float64 `json:"title_match_boost"`
	ExactPhraseBoost  float64 `json:"exact_phrase_boost"`
	DescriptionBoost  float64 `json:"description_boost"`
	CombinedScoreHint float64 `json:"combined_score_hint"`
}
