package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"atlas-search/internal/config"
	"atlas-search/internal/search"
)

type Server struct {
	httpServer *http.Server
}

func NewServer(cfg config.Config, service *search.Service) *Server {
	mux := http.NewServeMux()
	webDir := filepath.Join(".", "web")

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		indexPath := filepath.Join(webDir, "index.html")
		if _, err := os.Stat(indexPath); err != nil {
			http.Error(w, "web UI not found", http.StatusNotFound)
			return
		}

		http.ServeFile(w, r, indexPath)
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "ok",
			"stats":  service.Stats(),
		})
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		stats := service.Stats()
		writeJSON(w, http.StatusOK, map[string]any{
			"status":            "ready",
			"documents":         stats["documents"],
			"stale_documents":   stats["stale_documents"],
			"crawl_jobs":        stats["crawl_jobs"],
			"deduplicated_urls": stats["deduplicated_urls"],
		})
	})

	mux.HandleFunc("/api/v1/analytics", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, service.Analytics())
	})

	mux.HandleFunc("/api/v1/crawl", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req struct {
			URLs []string `json:"urls"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}

		response, err := service.Crawl(req.URLs)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}

		writeJSON(w, http.StatusCreated, response)
	})

	mux.HandleFunc("/api/v1/crawl/jobs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req struct {
			URLs []string `json:"urls"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
			return
		}

		job, err := service.EnqueueCrawl(req.URLs)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if len(job.URLs) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at least one url is required"})
			return
		}

		writeJSON(w, http.StatusAccepted, job)
	})

	mux.HandleFunc("/api/v1/crawl/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		jobID := strings.TrimPrefix(r.URL.Path, "/api/v1/crawl/jobs/")
		if jobID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "job id is required"})
			return
		}

		job, ok := service.GetCrawlJob(jobID)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "crawl job not found"})
			return
		}

		writeJSON(w, http.StatusOK, job)
	})

	mux.HandleFunc("/api/v1/crawl/stale", func(w http.ResponseWriter, r *http.Request) {
		limit := 10
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"documents": service.ListStaleDocuments(limit),
			})
		case http.MethodPost:
			job, err := service.EnqueueRecrawlDue(limit)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			if len(job.URLs) == 0 {
				writeJSON(w, http.StatusOK, map[string]any{
					"status": "noop",
					"urls":   []string{},
				})
				return
			}
			writeJSON(w, http.StatusAccepted, job)
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	})

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		query := r.URL.Query().Get("q")
		limit := 10
		minTrust := 0
		sortBy := r.URL.Query().Get("sort")
		domain := r.URL.Query().Get("domain")
		offset := 0
		trustedOnly := false
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}
		if rawMinTrust := r.URL.Query().Get("min_trust"); rawMinTrust != "" {
			if parsed, err := strconv.Atoi(rawMinTrust); err == nil {
				minTrust = parsed
			}
		}
		if rawOffset := r.URL.Query().Get("offset"); rawOffset != "" {
			if parsed, err := strconv.Atoi(rawOffset); err == nil {
				offset = parsed
			}
		}
		if rawTrusted := r.URL.Query().Get("trusted_only"); rawTrusted != "" {
			trustedOnly = rawTrusted == "1" || strings.EqualFold(rawTrusted, "true")
		}

		response, err := service.SearchWithOptions(r.Context(), query, limit, search.SearchOptions{
			SortBy:      sortBy,
			MinTrust:    minTrust,
			Domain:      domain,
			Offset:      offset,
			TrustedOnly: trustedOnly,
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
	})

	mux.HandleFunc("/api/v1/suggest", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		query := r.URL.Query().Get("q")
		limit := 6
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"query":       query,
			"suggestions": service.Suggest(query, limit),
		})
	})

	return &Server{
		httpServer: &http.Server{
			Addr:              cfg.Address,
			Handler:           withSecurityHeaders(mux),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      20 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
