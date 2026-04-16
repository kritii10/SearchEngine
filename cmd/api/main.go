package main

import (
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

	documentStore := store.NewMemoryStore()
	searchIndex := index.New()
	fetcher := crawler.NewFetcher(&http.Client{
		Timeout: 10 * time.Second,
	}, cfg.UserAgent)
	service := search.NewService(documentStore, searchIndex, fetcher)

	server := httpapi.NewServer(cfg, service)

	log.Printf("atlas-search listening on %s", cfg.Address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Printf("server failed: %v", err)
		os.Exit(1)
	}
}
