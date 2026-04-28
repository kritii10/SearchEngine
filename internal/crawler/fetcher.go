package crawler

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"

	"atlas-search/internal/index"
	"atlas-search/internal/model"
)

var spacePattern = regexp.MustCompile(`\s+`)

type Fetcher struct {
	client    *http.Client
	userAgent string
	robotsMu  sync.Mutex
	robots    map[string]robotsPolicy
}

type robotsPolicy struct {
	fetchedAt time.Time
	rules     map[string][]string
}

func NewFetcher(client *http.Client, userAgent string) *Fetcher {
	return &Fetcher{
		client:    client,
		userAgent: userAgent,
		robots:    make(map[string]robotsPolicy),
	}
}

func (f *Fetcher) Fetch(url string) (model.Document, error) {
	allowed, err := f.allowedByRobots(url)
	if err != nil {
		return model.Document{}, err
	}
	if !allowed {
		return model.Document{}, fmt.Errorf("blocked by robots.txt")
	}

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
	title, description, headings, canonicalURL, content := extractDocumentParts(rawHTML, url)
	links := extractLinks(rawHTML, url)
	if title == "" {
		title = url
	}
	if canonicalURL != "" {
		url = canonicalURL
	}

	hash := sha1.Sum([]byte(url))
	contentFingerprint := sha1.Sum([]byte(strings.Join([]string{
		cleanWhitespace(title),
		cleanWhitespace(description),
		cleanWhitespace(content),
	}, "\n")))

	doc := model.Document{
		ID:                 hex.EncodeToString(hash[:]),
		URL:                url,
		Domain:             extractDomain(url),
		Links:              links,
		Headings:           headings,
		Title:              cleanWhitespace(title),
		Description:        cleanWhitespace(description),
		Content:            cleanWhitespace(content),
		Terms:              index.Tokenize(strings.Join([]string{title, description, content}, " ")),
		ContentFingerprint: hex.EncodeToString(contentFingerprint[:]),
		CrawledAt:          time.Now().UTC(),
		RecrawlAfter:       recommendRecrawlAt(url, title, description, headings, content),
	}

	return doc, nil
}

func (f *Fetcher) Discover(rawURL string) ([]string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return []string{rawURL}, nil
	}

	sitemapURL := parsed.Scheme + "://" + parsed.Host + "/sitemap.xml"
	req, err := http.NewRequest(http.MethodGet, sitemapURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build sitemap request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.client.Do(req)
	if err != nil || resp.StatusCode >= 400 {
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return []string{rawURL}, nil
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, fmt.Errorf("read sitemap: %w", err)
	}

	urls := parseSitemapURLs(body, parsed)
	if len(urls) == 0 {
		return []string{rawURL}, nil
	}
	urls = append([]string{rawURL}, urls...)
	if len(urls) > 25 {
		urls = urls[:25]
	}
	return urls, nil
}

func extractDomain(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

func (f *Fetcher) allowedByRobots(rawURL string) (bool, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false, fmt.Errorf("parse url: %w", err)
	}
	hostKey := parsed.Scheme + "://" + parsed.Host

	policy, err := f.loadRobotsPolicy(hostKey)
	if err != nil {
		return false, err
	}
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}

	for _, disallow := range matchingRobotRules(policy, strings.ToLower(f.userAgent)) {
		if disallow == "" {
			continue
		}
		if strings.HasPrefix(path, disallow) {
			return false, nil
		}
	}
	return true, nil
}

func (f *Fetcher) loadRobotsPolicy(hostKey string) (robotsPolicy, error) {
	f.robotsMu.Lock()
	cached, ok := f.robots[hostKey]
	if ok && time.Since(cached.fetchedAt) < 15*time.Minute {
		f.robotsMu.Unlock()
		return cached, nil
	}
	f.robotsMu.Unlock()

	req, err := http.NewRequest(http.MethodGet, hostKey+"/robots.txt", nil)
	if err != nil {
		return robotsPolicy{}, fmt.Errorf("build robots request: %w", err)
	}
	req.Header.Set("User-Agent", f.userAgent)

	resp, err := f.client.Do(req)
	if err != nil {
		return robotsPolicy{}, fmt.Errorf("fetch robots.txt: %w", err)
	}
	defer resp.Body.Close()

	policy := robotsPolicy{
		fetchedAt: time.Now(),
		rules:     map[string][]string{"*": {}},
	}
	if resp.StatusCode == http.StatusNotFound {
		f.robotsMu.Lock()
		f.robots[hostKey] = policy
		f.robotsMu.Unlock()
		return policy, nil
	}
	if resp.StatusCode >= 400 {
		return robotsPolicy{}, fmt.Errorf("fetch robots.txt: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return robotsPolicy{}, fmt.Errorf("read robots.txt: %w", err)
	}

	currentAgents := []string{"*"}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])
		switch key {
		case "user-agent":
			agent := strings.ToLower(value)
			currentAgents = []string{agent}
			if _, exists := policy.rules[agent]; !exists {
				policy.rules[agent] = []string{}
			}
		case "disallow":
			for _, agent := range currentAgents {
				policy.rules[agent] = append(policy.rules[agent], value)
			}
		}
	}

	f.robotsMu.Lock()
	f.robots[hostKey] = policy
	f.robotsMu.Unlock()
	return policy, nil
}

func matchingRobotRules(policy robotsPolicy, userAgent string) []string {
	if rules, ok := policy.rules[userAgent]; ok {
		return rules
	}
	for agent, rules := range policy.rules {
		if agent != "*" && strings.Contains(userAgent, agent) {
			return rules
		}
	}
	return policy.rules["*"]
}

func extractDocumentParts(input, baseURL string) (title, description string, headings []string, canonicalURL, content string) {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return "", "", nil, "", cleanWhitespace(stripTagsFallback(input))
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
			case "h1", "h2", "h3":
				heading := extractText(node)
				if heading != "" {
					headings = append(headings, cleanWhitespace(heading))
				}
			case "meta":
				if description == "" {
					name := strings.ToLower(getAttr(node, "name"))
					property := strings.ToLower(getAttr(node, "property"))
					if name == "description" || property == "og:description" {
						description = getAttr(node, "content")
					}
				}
			case "link":
				if canonicalURL == "" && strings.EqualFold(getAttr(node, "rel"), "canonical") {
					canonicalURL = resolveURL(baseURL, getAttr(node, "href"))
				}
			case "br", "p", "div", "section", "article", "li", "h4", "h5", "h6":
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
	return cleanWhitespace(title), cleanWhitespace(description), uniqueStrings(headings), canonicalURL, cleanWhitespace(strings.Join(textParts, " "))
}

func recommendRecrawlAt(rawURL, title, description string, headings []string, content string) time.Time {
	joined := strings.ToLower(strings.Join([]string{
		rawURL,
		title,
		description,
		strings.Join(headings, " "),
		content,
	}, " "))

	interval := 7 * 24 * time.Hour
	switch {
	case strings.Contains(joined, "news") || strings.Contains(joined, "latest") || strings.Contains(joined, "update") || strings.Contains(joined, "release"):
		interval = 24 * time.Hour
	case strings.Contains(joined, "blog") || strings.Contains(joined, "changelog") || strings.Contains(joined, "announc"):
		interval = 3 * 24 * time.Hour
	case strings.Contains(joined, "guide") || strings.Contains(joined, "docs") || strings.Contains(joined, "reference") || strings.Contains(joined, "tutorial"):
		interval = 21 * 24 * time.Hour
	}
	return time.Now().UTC().Add(interval)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = cleanWhitespace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func extractLinks(input, baseURL string) []string {
	root, err := html.Parse(strings.NewReader(input))
	if err != nil {
		return nil
	}

	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}

	seen := map[string]struct{}{}
	links := make([]string, 0, 8)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			href := strings.TrimSpace(getAttr(node, "href"))
			if href != "" {
				if parsed, err := url.Parse(href); err == nil {
					resolved := base.ResolveReference(parsed)
					if resolved.Scheme == "http" || resolved.Scheme == "https" {
						normalized := resolved.String()
						if _, ok := seen[normalized]; !ok {
							seen[normalized] = struct{}{}
							links = append(links, normalized)
						}
					}
				}
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(root)
	return links
}

func parseSitemapURLs(body []byte, base *url.URL) []string {
	type urlSet struct {
		URLs []struct {
			Loc string `xml:"loc"`
		} `xml:"url"`
	}
	type sitemapIndex struct {
		Sitemaps []struct {
			Loc string `xml:"loc"`
		} `xml:"sitemap"`
	}

	var set urlSet
	if err := xml.Unmarshal(body, &set); err == nil && len(set.URLs) > 0 {
		urls := make([]string, 0, len(set.URLs))
		for _, item := range set.URLs {
			if resolved := resolveURL(base.String(), item.Loc); resolved != "" {
				urls = append(urls, resolved)
			}
		}
		return urls
	}

	var index sitemapIndex
	if err := xml.Unmarshal(body, &index); err == nil && len(index.Sitemaps) > 0 {
		urls := make([]string, 0, len(index.Sitemaps))
		for _, item := range index.Sitemaps {
			if resolved := resolveURL(base.String(), item.Loc); resolved != "" {
				urls = append(urls, resolved)
			}
		}
		return urls
	}

	return nil
}

func resolveURL(baseURL, raw string) string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	resolved.Fragment = ""
	return resolved.String()
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
