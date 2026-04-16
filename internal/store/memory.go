package store

import (
	"errors"
	"sync"

	"atlas-search/internal/model"
)

var ErrDocumentNotFound = errors.New("document not found")

type DocumentStore interface {
	Upsert(doc model.Document) error
	Get(id string) (model.Document, error)
	List() []model.Document
	Count() int
}

type MemoryStore struct {
	mu        sync.RWMutex
	documents map[string]model.Document
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		documents: make(map[string]model.Document),
	}
}

func (s *MemoryStore) Upsert(doc model.Document) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[doc.ID] = doc
	return nil
}

func (s *MemoryStore) Get(id string) (model.Document, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	doc, ok := s.documents[id]
	if !ok {
		return model.Document{}, ErrDocumentNotFound
	}
	return doc, nil
}

func (s *MemoryStore) List() []model.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()

	documents := make([]model.Document, 0, len(s.documents))
	for _, doc := range s.documents {
		documents = append(documents, doc)
	}
	return documents
}

func (s *MemoryStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.documents)
}
