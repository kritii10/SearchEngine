package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"atlas-search/internal/config"
	"atlas-search/internal/crawler"
	"atlas-search/internal/httpapi"
	"atlas-search/internal/index"
	"atlas-search/internal/search"
	"atlas-search/internal/store"
)

func main() {
	cfg := config.Load()

	documentStore := mustBuildStore(cfg)
	searchIndex := index.New()
	for _, doc := range documentStore.List() {
		searchIndex.Add(doc)
	}

	fetcher := crawler.NewFetcher(&http.Client{
		Timeout: 10 * time.Second,
	}, cfg.UserAgent)
	summarizer := search.NewHTTPSummarizer(cfg.AIBaseURL)
	service := search.NewServiceWithSummarizer(documentStore, searchIndex, fetcher, summarizer)

	server := httpapi.NewServer(cfg, service)

	log.Printf("atlas-search listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server failed: %v", err)
		os.Exit(1)
	}
}

func mustBuildStore(cfg config.Config) store.DocumentStore {
	if cfg.StorageDriver == "postgres" {
		if cfg.DatabaseURL == "" {
			log.Printf("ATLAS_STORAGE_DRIVER=postgres but ATLAS_DATABASE_URL is empty, falling back to memory store")
			return store.NewMemoryStore()
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		documentStore, err := store.NewPostgresStore(ctx, cfg.DatabaseURL)
		if err != nil {
			log.Printf("postgres store unavailable: %v; falling back to memory store", err)
			return store.NewMemoryStore()
		}

		log.Printf("using postgres document store")
		return documentStore
	}

	return store.NewMemoryStore()
}
