package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"atlas-search/internal/model"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	store := &PostgresStore{db: db}
	if err := store.ensureSchema(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *PostgresStore) Upsert(doc model.Document) error {
	termsJSON, err := json.Marshal(doc.Terms)
	if err != nil {
		return fmt.Errorf("marshal terms: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO documents (id, url, title, description, content, terms, crawled_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
		ON CONFLICT (id) DO UPDATE SET
			url = EXCLUDED.url,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			content = EXCLUDED.content,
			terms = EXCLUDED.terms,
			crawled_at = EXCLUDED.crawled_at
	`, doc.ID, doc.URL, doc.Title, doc.Description, doc.Content, string(termsJSON), doc.CrawledAt)
	if err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}

	return nil
}

func (s *PostgresStore) Get(id string) (model.Document, error) {
	row := s.db.QueryRow(`
		SELECT id, url, title, description, content, terms, crawled_at
		FROM documents
		WHERE id = $1
	`, id)

	doc, err := scanDocument(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.Document{}, ErrDocumentNotFound
		}
		return model.Document{}, err
	}
	return doc, nil
}

func (s *PostgresStore) List() []model.Document {
	rows, err := s.db.Query(`
		SELECT id, url, title, description, content, terms, crawled_at
		FROM documents
		ORDER BY crawled_at DESC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	documents := make([]model.Document, 0)
	for rows.Next() {
		doc, err := scanDocument(rows.Scan)
		if err != nil {
			continue
		}
		documents = append(documents, doc)
	}
	return documents
}

func (s *PostgresStore) Count() int {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM documents`).Scan(&count); err != nil {
		return 0
	}
	return count
}

func (s *PostgresStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			terms JSONB NOT NULL DEFAULT '[]'::jsonb,
			crawled_at TIMESTAMPTZ NOT NULL
		);

		CREATE INDEX IF NOT EXISTS documents_crawled_at_idx ON documents (crawled_at DESC);
		CREATE INDEX IF NOT EXISTS documents_url_idx ON documents (url);
	`)
	if err != nil {
		return fmt.Errorf("ensure postgres schema: %w", err)
	}
	return nil
}

type scannerFn func(dest ...any) error

func scanDocument(scan scannerFn) (model.Document, error) {
	var doc model.Document
	var termsJSON []byte
	if err := scan(&doc.ID, &doc.URL, &doc.Title, &doc.Description, &doc.Content, &termsJSON, &doc.CrawledAt); err != nil {
		return model.Document{}, err
	}

	if len(termsJSON) > 0 {
		if err := json.Unmarshal(termsJSON, &doc.Terms); err != nil {
			return model.Document{}, fmt.Errorf("decode document terms: %w", err)
		}
	}

	return doc, nil
}
