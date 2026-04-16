# Build Roadmap

## Phase 1

- bootstrap Go API
- fetch and index pages in memory
- expose crawl and search endpoints
- add Python summarization scaffold

## Phase 2

- move document storage to PostgreSQL
- add Redis query caching
- make crawl jobs asynchronous
- deduplicate pages by content fingerprint

## Phase 3

- add vector embeddings and hybrid ranking
- connect top results to the summarization layer
- add result explanations and trust scoring
- build a minimal search UI
