package crawler

import (
	"strings"
	"testing"
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

	title, description, content := extractDocumentParts(html)

	if title != "Go Site" {
		t.Fatalf("expected title Go Site, got %q", title)
	}
	if description != "The Go programming language" {
		t.Fatalf("expected clean description, got %q", description)
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
	_, _, content := extractDocumentParts(html)

	if content != "Hello Atlas Search" {
		t.Fatalf("expected visible text only, got %q", content)
	}
}
