package crawler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtractDocumentPartsSkipsScriptAndStyleNoise(t *testing.T) {
	html := `
	<html>
	  <head>
	    <title>Go Site</title>
	    <meta name="description" content="The Go programming language">
	    <style>body { color: red; }</style>
	    <script>console.log("tracking")</script>
	  </head>
	  <body>
	    <header>Skip to main content</header>
	    <nav>Docs Tutorials Pricing</nav>
	    <main>
	      <h1>Build simple, secure, scalable systems with Go</h1>
	      <p>Go is an open source programming language.</p>
	    </main>
	    <footer>Copyright footer links</footer>
	  </body>
	</html>`

	title, description, headings, _, content := extractDocumentParts(html, "https://example.com")

	if title != "Go Site" {
		t.Fatalf("expected title Go Site, got %q", title)
	}
	if description != "The Go programming language" {
		t.Fatalf("expected clean description, got %q", description)
	}
	if len(headings) == 0 || headings[0] != "Build simple, secure, scalable systems with Go" {
		t.Fatalf("expected extracted headings, got %#v", headings)
	}
	if strings.Contains(content, "console.log") {
		t.Fatalf("expected script content to be removed, got %q", content)
	}
	if strings.Contains(content, "color: red") {
		t.Fatalf("expected style content to be removed, got %q", content)
	}
	if strings.Contains(content, "Docs Tutorials Pricing") {
		t.Fatalf("expected nav content to be removed, got %q", content)
	}
	if strings.Contains(content, "Copyright footer links") {
		t.Fatalf("expected footer content to be removed, got %q", content)
	}
	if !strings.Contains(content, "Go is an open source programming language.") {
		t.Fatalf("expected visible body text in content, got %q", content)
	}
}

func TestExtractDocumentPartsFallsBackToVisibleText(t *testing.T) {
	html := `<html><body><div>Hello <strong>Atlas</strong> Search</div></body></html>`
	_, _, _, _, content := extractDocumentParts(html, "https://example.com")

	if content != "Hello Atlas Search" {
		t.Fatalf("expected visible text only, got %q", content)
	}
}

func TestExtractDomainNormalizesHostname(t *testing.T) {
	domain := extractDomain("https://Docs.Example.com/path?q=1")
	if domain != "docs.example.com" {
		t.Fatalf("expected normalized domain, got %q", domain)
	}
}

func TestFetcherHonorsRobotsDisallow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private"))
		case "/private/page":
			_, _ = w.Write([]byte("<html><body>secret</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(server.Client(), "AtlasSearchBot/0.1")
	if _, err := fetcher.Fetch(server.URL + "/private/page"); err == nil || !strings.Contains(err.Error(), "robots.txt") {
		t.Fatalf("expected robots block, got %v", err)
	}
}

func TestExtractLinksResolvesRelativeURLs(t *testing.T) {
	html := `<html><body><a href="/docs">Docs</a><a href="https://example.com/about">About</a></body></html>`
	links := extractLinks(html, "https://example.com/start")
	if len(links) != 2 {
		t.Fatalf("expected 2 links, got %#v", links)
	}
	if links[0] != "https://example.com/docs" {
		t.Fatalf("expected resolved relative link, got %#v", links)
	}
}

func TestRecommendRecrawlAtPrioritizesNewsAheadOfGuides(t *testing.T) {
	now := time.Now().UTC()
	newsRecrawl := recommendRecrawlAt("https://example.com/news/latest", "Latest Search Update", "", nil, "breaking search update")
	guideRecrawl := recommendRecrawlAt("https://example.com/docs/guide", "Search Guide", "", []string{"Guide"}, "tutorial")
	if !newsRecrawl.After(now) || !guideRecrawl.After(now) {
		t.Fatalf("expected future recrawl times, got %v and %v", newsRecrawl, guideRecrawl)
	}
	if !guideRecrawl.After(newsRecrawl) {
		t.Fatalf("expected guide to recrawl later than news, got %v and %v", guideRecrawl, newsRecrawl)
	}
}

func TestExtractDocumentPartsResolvesCanonicalURL(t *testing.T) {
	html := `<html><head><link rel="canonical" href="/guide"></head><body><h1>Guide</h1></body></html>`
	_, _, _, canonicalURL, _ := extractDocumentParts(html, "https://example.com/start")
	if canonicalURL != "https://example.com/guide" {
		t.Fatalf("expected canonical url, got %q", canonicalURL)
	}
}

func TestFetcherDiscoverUsesSitemapForRootURLs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sitemap.xml":
			_, _ = w.Write([]byte(`<?xml version="1.0"?><urlset><url><loc>/guide</loc></url><url><loc>/docs</loc></url></urlset>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	fetcher := NewFetcher(server.Client(), "AtlasSearchBot/0.1")
	urls, err := fetcher.Discover(server.URL + "/")
	if err != nil {
		t.Fatalf("discover failed: %v", err)
	}
	if len(urls) != 3 {
		t.Fatalf("expected root plus sitemap urls, got %#v", urls)
	}
}
