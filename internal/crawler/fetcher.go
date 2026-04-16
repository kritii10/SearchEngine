package crawler

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"atlas-search/internal/index"
	"atlas-search/internal/model"
)

var (
	titlePattern       = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	metaDescriptionTag = regexp.MustCompile(`(?is)<meta[^>]*name=["']description["'][^>]*content=["'](.*?)["'][^>]*>`)
	tagPattern         = regexp.MustCompile(`(?s)<[^>]*>`)
	spacePattern       = regexp.MustCompile(`\s+`)
)

type Fetcher struct {
	client    *http.Client
	userAgent string
}

func NewFetcher(client *http.Client, userAgent string) *Fetcher {
	return &Fetcher{
		client:    client,
		userAgent: userAgent,
	}
}

func (f *Fetcher) Fetch(url string) (model.Document, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return model.Document{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return model.Document{}, fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return model.Document{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return model.Document{}, fmt.Errorf("read response body: %w", err)
	}

	rawHTML := string(body)
	title := firstMatch(titlePattern, rawHTML)
	description := firstMatch(metaDescriptionTag, rawHTML)
	content := textFromHTML(rawHTML)
	if title == "" {
		title = url
	}

	hash := sha1.Sum([]byte(url))

	doc := model.Document{
		ID:          hex.EncodeToString(hash[:]),
		URL:         url,
		Title:       cleanWhitespace(title),
		Description: cleanWhitespace(description),
		Content:     content,
		Terms:       index.Tokenize(strings.Join([]string{title, description, content}, " ")),
		CrawledAt:   time.Now().UTC(),
	}

	return doc, nil
}

func firstMatch(pattern *regexp.Regexp, input string) string {
	matches := pattern.FindStringSubmatch(input)
	if len(matches) < 2 {
		return ""
	}
	return stripTags(matches[1])
}

func textFromHTML(input string) string {
	return cleanWhitespace(stripTags(input))
}

func stripTags(input string) string {
	return tagPattern.ReplaceAllString(input, " ")
}

func cleanWhitespace(input string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(input, " "))
}
