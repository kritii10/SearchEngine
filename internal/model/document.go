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
	DocumentID  string  `json:"document_id"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
	Description string  `json:"description"`
}
