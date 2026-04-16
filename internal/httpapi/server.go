package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

	mux.HandleFunc("/api/v1/crawl", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		var req struct {
			URLs []string `json:"urls"`
		}
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

	mux.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}

		query := r.URL.Query().Get("q")
		limit := 10
		if rawLimit := r.URL.Query().Get("limit"); rawLimit != "" {
			if parsed, err := strconv.Atoi(rawLimit); err == nil {
				limit = parsed
			}
		}

		response, err := service.SearchWithAnswer(r.Context(), query, limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:    cfg.Address,
			Handler: mux,
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
