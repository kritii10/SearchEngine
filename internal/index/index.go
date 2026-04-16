package index

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"

	"atlas-search/internal/model"
)

var nonAlphaNum = regexp.MustCompile(`[^a-z0-9\s]+`)

type Posting struct {
	DocumentID string
	TermFreq   int
}

type ResultScore struct {
	DocumentID string
	Score      float64
}

type Index struct {
	mu           sync.RWMutex
	postings     map[string][]Posting
	docLengths   map[string]int
	docTermFreqs map[string]map[string]int
	docCount     int
}

func New() *Index {
	return &Index{
		postings:     make(map[string][]Posting),
		docLengths:   make(map[string]int),
		docTermFreqs: make(map[string]map[string]int),
	}
}

func (i *Index) Add(doc model.Document) {
	termFreqs := map[string]int{}
	for _, term := range doc.Terms {
		termFreqs[term]++
	}

	i.mu.Lock()
	defer i.mu.Unlock()

	if previous, exists := i.docTermFreqs[doc.ID]; exists {
		for term := range previous {
			i.removePosting(term, doc.ID)
		}
	} else {
		i.docCount++
	}

	i.docLengths[doc.ID] = len(doc.Terms)
	i.docTermFreqs[doc.ID] = termFreqs
	for term, freq := range termFreqs {
		i.postings[term] = append(i.postings[term], Posting{
			DocumentID: doc.ID,
			TermFreq:   freq,
		})
	}
}

func (i *Index) Search(query string) []ResultScore {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return nil
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	if i.docCount == 0 {
		return nil
	}

	avgDocLength := 0.0
	for _, length := range i.docLengths {
		avgDocLength += float64(length)
	}
	avgDocLength /= float64(i.docCount)

	const k1 = 1.5
	const b = 0.75

	scores := map[string]float64{}
	for _, term := range terms {
		postings := i.postings[term]
		if len(postings) == 0 {
			continue
		}

		idf := math.Log(1 + (float64(i.docCount-len(postings))+0.5)/(float64(len(postings))+0.5))
		for _, posting := range postings {
			docLength := float64(i.docLengths[posting.DocumentID])
			tf := float64(posting.TermFreq)
			numerator := tf * (k1 + 1)
			denominator := tf + k1*(1-b+b*(docLength/avgDocLength))
			scores[posting.DocumentID] += idf * (numerator / denominator)
		}
	}

	results := make([]ResultScore, 0, len(scores))
	for documentID, score := range scores {
		results = append(results, ResultScore{DocumentID: documentID, Score: score})
	}

	sort.Slice(results, func(a, b int) bool {
		if results[a].Score == results[b].Score {
			return results[a].DocumentID < results[b].DocumentID
		}
		return results[a].Score > results[b].Score
	})

	return results
}

func (i *Index) removePosting(term, documentID string) {
	postings := i.postings[term]
	if len(postings) == 0 {
		return
	}

	filtered := postings[:0]
	for _, posting := range postings {
		if posting.DocumentID != documentID {
			filtered = append(filtered, posting)
		}
	}

	if len(filtered) == 0 {
		delete(i.postings, term)
		return
	}
	i.postings[term] = filtered
}

func Tokenize(content string) []string {
	normalized := strings.ToLower(content)
	normalized = nonAlphaNum.ReplaceAllString(normalized, " ")
	rawTerms := strings.Fields(normalized)

	terms := make([]string, 0, len(rawTerms))
	for _, term := range rawTerms {
		if len(term) < 2 {
			continue
		}
		if _, ignored := stopWords[term]; ignored {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}

var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "for": {}, "from": {},
	"has": {}, "he": {}, "in": {}, "is": {}, "it": {}, "its": {}, "of": {}, "on": {}, "that": {}, "the": {},
	"to": {}, "was": {}, "were": {}, "will": {}, "with": {},
}
