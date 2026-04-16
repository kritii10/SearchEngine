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

	"golang.org/x/net/html"

	"atlas-search/internal/index"
	"atlas-search/internal/model"
)

var spacePattern = regexp.MustCompile(`\s+`)

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
	title, description, content := extractDocumentParts(rawHTML)
	if title == "" {
		title = url
	}

	hash := sha1.Sum([]byte(url))

	doc := model.Document{
		ID:          hex.EncodeToString(hash[:]),
		URL:         url,
		Title:       cleanWhitespace(title),
		Description: cleanWhitespace(description),
		Content:     cleanWhitespace(content),
		Terms:       index.Tokenize(strings.Join([]string{title, description, content}, " ")),
		CrawledAt:   time.Now().UTC(),
	}

	return doc, nil
}

func extractDocumentParts(input string) (title, description, content string) {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", "", cleanWhitespace(stripTagsFallback(input))
	}

	var textParts []string
	var walk func(*html.Node, bool)
	walk = func(node *html.Node, hidden bool) {
		if node == nil {
			return
		}

		currentHidden := hidden || isIgnoredElement(node)

		if node.Type == html.ElementNode {
			switch node.Data {
			case "title":
				if title == "" {
					title = extractText(node)
				}
			case "meta":
				if description == "" {
					name := strings.ToLower(getAttr(node, "name"))
					property := strings.ToLower(getAttr(node, "property"))
					if name == "description" || property == "og:description" {
						description = getAttr(node, "content")
					}
				}
			case "br", "p", "div", "section", "article", "li", "h1", "h2", "h3", "h4", "h5", "h6":
				textParts = append(textParts, " ")
			}
		}

		if node.Type == html.TextNode && !currentHidden {
			text := cleanWhitespace(html.UnescapeString(node.Data))
			if text != "" {
				textParts = append(textParts, text)
			}
		}

		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, currentHidden)
		}
	}

	walk(root, false)
	return cleanWhitespace(title), cleanWhitespace(description), cleanWhitespace(strings.Join(textParts, " "))
}

func extractText(node *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(current *html.Node) {
		if current == nil {
			return
		}
		if current.Type == html.TextNode {
			text := cleanWhitespace(html.UnescapeString(current.Data))
			if text != "" {
				parts = append(parts, text)
			}
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(parts, " ")
}

func isIgnoredElement(node *html.Node) bool {
	if node.Type != html.ElementNode {
		return false
	}

	switch node.Data {
	case "script", "style", "noscript", "svg", "canvas", "iframe", "nav", "footer", "aside", "form":
		return true
	}
	return false
}

func getAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func stripTagsFallback(input string) string {
	inTag := false
	var builder strings.Builder
	for _, r := range input {
		switch r {
		case '<':
			inTag = true
			builder.WriteRune(' ')
		case '>':
			inTag = false
			builder.WriteRune(' ')
		default:
			if !inTag {
				builder.WriteRune(r)
			}
		}
	}
	return builder.String()
}

func cleanWhitespace(input string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(input, " "))
}
