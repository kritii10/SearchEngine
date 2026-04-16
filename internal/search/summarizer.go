package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"atlas-search/internal/model"
)

type Summarizer interface {
	Summarize(ctx context.Context, query string, snippets []string) (model.AnswerSummary, error)
}

type HTTPSummarizer struct {
	baseURL string
	client  *http.Client
}

func NewHTTPSummarizer(baseURL string) *HTTPSummarizer {
	return &HTTPSummarizer{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 4 * time.Second,
		},
	}
}

func (s *HTTPSummarizer) Summarize(ctx context.Context, query string, snippets []string) (model.AnswerSummary, error) {
	payload := map[string]any{
		"query":    query,
		"snippets": snippets,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return model.AnswerSummary{}, fmt.Errorf("marshal summarize request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/summarize", bytes.NewReader(body))
	if err != nil {
		return model.AnswerSummary{}, fmt.Errorf("build summarize request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return model.AnswerSummary{}, fmt.Errorf("call summarize endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return model.AnswerSummary{}, fmt.Errorf("summarizer returned status %d", resp.StatusCode)
	}

	var summary model.AnswerSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		return model.AnswerSummary{}, fmt.Errorf("decode summarize response: %w", err)
	}
	summary.Generated = true
	return summary, nil
}
