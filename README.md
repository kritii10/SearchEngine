# Atlas Search

Atlas Search is a Google-inspired hybrid search engine with Go as the core runtime and Python as the AI enhancement layer.

This first version includes:

- concurrent-ish crawl pipeline with bounded fan-out
- asynchronous crawl job queue with status tracking
- in-memory document store
- inverted index with BM25-style scoring
- reranking with title and exact-phrase boosts
- HTTP API for crawling and searching
- optional Python summarization service for grounded answer generation

## Architecture

- `cmd/api`: Go API entrypoint
- `internal/crawler`: URL fetch and document extraction
- `internal/index`: tokenization and inverted index
- `internal/search`: query scoring and result snippets
- `internal/store`: document persistence interface and in-memory implementation
- `ai`: Python summarization service scaffold

## HTTP API

### Health check

`GET /healthz`

### Crawl URLs

`POST /api/v1/crawl`

```json
{
  "urls": [
    "https://example.com"
  ]
}
```

### Enqueue crawl job

`POST /api/v1/crawl/jobs`

```json
{
  "urls": [
    "https://example.com",
    "https://go.dev"
  ]
}
```

### Crawl job status

`GET /api/v1/crawl/jobs/{jobID}`

### Search

`GET /api/v1/search?q=example`

When the AI service is running, search responses also include an `answer` field with a grounded summary built from the top snippets.

## Local setup

### Go API

1. Install Go 1.24 or newer.
2. Run `go run ./cmd/api`.

### Python AI scaffold

1. Create a virtual environment.
2. Install dependencies from `ai/requirements.txt`.
3. Run `uvicorn app:app --reload --host 127.0.0.1 --port 8001` from the `ai` directory.

## Next steps

- add PostgreSQL-backed document storage
- add Redis query caching
- support recrawl scheduling and deduplication
- connect search results to the AI summarization endpoint
- add a simple frontend for result exploration
