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
	linksJSON, err := json.Marshal(doc.Links)
	if err != nil {
		return fmt.Errorf("marshal links: %w", err)
	}
	headingsJSON, err := json.Marshal(doc.Headings)
	if err != nil {
		return fmt.Errorf("marshal headings: %w", err)
	}

	_, err = s.db.Exec(`
		INSERT INTO documents (id, url, domain, title, description, content, terms, links, headings, content_fingerprint, crawled_at, recrawl_after)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11, $12)
		ON CONFLICT (id) DO UPDATE SET
			url = EXCLUDED.url,
			domain = EXCLUDED.domain,
			title = EXCLUDED.title,
			description = EXCLUDED.description,
			content = EXCLUDED.content,
			terms = EXCLUDED.terms,
			links = EXCLUDED.links,
			headings = EXCLUDED.headings,
			content_fingerprint = EXCLUDED.content_fingerprint,
			crawled_at = EXCLUDED.crawled_at,
			recrawl_after = EXCLUDED.recrawl_after
	`, doc.ID, doc.URL, doc.Domain, doc.Title, doc.Description, doc.Content, string(termsJSON), string(linksJSON), string(headingsJSON), doc.ContentFingerprint, doc.CrawledAt, doc.RecrawlAfter)
	if err != nil {
		return fmt.Errorf("upsert document: %w", err)
	}

	return nil
}

func (s *PostgresStore) Get(id string) (model.Document, error) {
	row := s.db.QueryRow(`
		SELECT id, url, domain, title, description, content, terms, links, headings, content_fingerprint, crawled_at, recrawl_after
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

func (s *PostgresStore) FindByContentFingerprint(fingerprint string) (model.Document, error) {
	row := s.db.QueryRow(`
		SELECT id, url, domain, title, description, content, terms, links, headings, content_fingerprint, crawled_at, recrawl_after
		FROM documents
		WHERE content_fingerprint = $1
		ORDER BY crawled_at DESC
		LIMIT 1
	`, fingerprint)

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
		SELECT id, url, domain, title, description, content, terms, links, headings, content_fingerprint, crawled_at, recrawl_after
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

func (s *PostgresStore) StaleCount(now time.Time) int {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM documents WHERE recrawl_after <= $1`, now).Scan(&count); err != nil {
		return 0
	}
	return count
}

func (s *PostgresStore) ensureSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS documents (
			id TEXT PRIMARY KEY,
			url TEXT NOT NULL,
			domain TEXT NOT NULL DEFAULT '',
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL,
			terms JSONB NOT NULL DEFAULT '[]'::jsonb,
			links JSONB NOT NULL DEFAULT '[]'::jsonb,
			headings JSONB NOT NULL DEFAULT '[]'::jsonb,
			content_fingerprint TEXT NOT NULL DEFAULT '',
			crawled_at TIMESTAMPTZ NOT NULL,
			recrawl_after TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);

		ALTER TABLE documents
		ADD COLUMN IF NOT EXISTS content_fingerprint TEXT NOT NULL DEFAULT '';
		ALTER TABLE documents
		ADD COLUMN IF NOT EXISTS domain TEXT NOT NULL DEFAULT '';
		ALTER TABLE documents
		ADD COLUMN IF NOT EXISTS links JSONB NOT NULL DEFAULT '[]'::jsonb;
		ALTER TABLE documents
		ADD COLUMN IF NOT EXISTS headings JSONB NOT NULL DEFAULT '[]'::jsonb;
		ALTER TABLE documents
		ADD COLUMN IF NOT EXISTS recrawl_after TIMESTAMPTZ NOT NULL DEFAULT NOW();

		CREATE INDEX IF NOT EXISTS documents_crawled_at_idx ON documents (crawled_at DESC);
		CREATE INDEX IF NOT EXISTS documents_recrawl_after_idx ON documents (recrawl_after);
		CREATE INDEX IF NOT EXISTS documents_url_idx ON documents (url);
		CREATE INDEX IF NOT EXISTS documents_domain_idx ON documents (domain);
		CREATE INDEX IF NOT EXISTS documents_content_fingerprint_idx ON documents (content_fingerprint);
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
	var linksJSON []byte
	var headingsJSON []byte
	if err := scan(&doc.ID, &doc.URL, &doc.Domain, &doc.Title, &doc.Description, &doc.Content, &termsJSON, &linksJSON, &headingsJSON, &doc.ContentFingerprint, &doc.CrawledAt, &doc.RecrawlAfter); err != nil {
		return model.Document{}, err
	}

	if len(termsJSON) > 0 {
		if err := json.Unmarshal(termsJSON, &doc.Terms); err != nil {
			return model.Document{}, fmt.Errorf("decode document terms: %w", err)
		}
	}
	if len(linksJSON) > 0 {
		if err := json.Unmarshal(linksJSON, &doc.Links); err != nil {
			return model.Document{}, fmt.Errorf("decode document links: %w", err)
		}
	}
	if len(headingsJSON) > 0 {
		if err := json.Unmarshal(headingsJSON, &doc.Headings); err != nil {
			return model.Document{}, fmt.Errorf("decode document headings: %w", err)
		}
	}

	return doc, nil
}
