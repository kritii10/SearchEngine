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
	docPhrases   map[string]map[string]int
	phraseFreqs  map[string]int
	docCount     int
}

func New() *Index {
	return &Index{
		postings:     make(map[string][]Posting),
		docLengths:   make(map[string]int),
		docTermFreqs: make(map[string]map[string]int),
		docPhrases:   make(map[string]map[string]int),
		phraseFreqs:  make(map[string]int),
	}
}

func (i *Index) Add(doc model.Document) {
	termFreqs := map[string]int{}
	for _, term := range doc.Terms {
		termFreqs[term]++
	}
	phraseFreqs := buildPhraseFreqs(doc)

	i.mu.Lock()
	defer i.mu.Unlock()

	if previous, exists := i.docTermFreqs[doc.ID]; exists {
		for term := range previous {
			i.removePosting(term, doc.ID)
		}
		for phrase, freq := range i.docPhrases[doc.ID] {
			i.phraseFreqs[phrase] -= freq
			if i.phraseFreqs[phrase] <= 0 {
				delete(i.phraseFreqs, phrase)
			}
		}
	} else {
		i.docCount++
	}

	i.docLengths[doc.ID] = len(doc.Terms)
	i.docTermFreqs[doc.ID] = termFreqs
	i.docPhrases[doc.ID] = phraseFreqs
	for term, freq := range termFreqs {
		i.postings[term] = append(i.postings[term], Posting{
			DocumentID: doc.ID,
			TermFreq:   freq,
		})
	}
	for phrase, freq := range phraseFreqs {
		i.phraseFreqs[phrase] += freq
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

func (i *Index) Suggest(prefix string, limit int) []string {
	prefix = strings.TrimSpace(strings.ToLower(prefix))
	if prefix == "" {
		return nil
	}
	if limit <= 0 {
		limit = 5
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	prefixParts := strings.Fields(prefix)

	type candidate struct {
		value    string
		count    int
		phrase   bool
		prefixes int
	}

	candidatesByValue := make(map[string]candidate, len(i.postings))
	for term, postings := range i.postings {
		if strings.HasPrefix(term, prefix) {
			candidatesByValue[term] = candidate{
				value:    term,
				count:    len(postings),
				prefixes: 1,
			}
		}
	}
	for phrase, freq := range i.phraseFreqs {
		if !strings.HasPrefix(phrase, prefix) {
			continue
		}
		if len(prefixParts) > 1 && len(strings.Fields(phrase)) <= len(prefixParts) {
			continue
		}
		prefixMatches := countPrefixMatches(phrase, prefix)
		if prefixMatches == 0 {
			continue
		}
		existing, ok := candidatesByValue[phrase]
		if ok {
			existing.count += freq
			existing.prefixes = max(existing.prefixes, prefixMatches)
			existing.phrase = strings.Contains(phrase, " ")
			candidatesByValue[phrase] = existing
			continue
		}
		candidatesByValue[phrase] = candidate{
			value:    phrase,
			count:    freq,
			phrase:   strings.Contains(phrase, " "),
			prefixes: prefixMatches,
		}
	}

	candidates := make([]candidate, 0, len(candidatesByValue))
	for _, candidate := range candidatesByValue {
		candidates = append(candidates, candidate)
	}

	sort.Slice(candidates, func(a, b int) bool {
		if candidates[a].prefixes == candidates[b].prefixes && candidates[a].phrase != candidates[b].phrase {
			if len(prefixParts) > 1 {
				return candidates[a].phrase
			}
			return !candidates[a].phrase
		}
		if candidates[a].prefixes != candidates[b].prefixes {
			return candidates[a].prefixes > candidates[b].prefixes
		}
		if candidates[a].count == candidates[b].count {
			return candidates[a].value < candidates[b].value
		}
		return candidates[a].count > candidates[b].count
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	suggestions := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		suggestions = append(suggestions, candidate.value)
	}
	return suggestions
}

func (i *Index) Stats() map[string]int {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return map[string]int{
		"documents": len(i.docLengths),
		"terms":     len(i.postings),
		"phrases":   len(i.phraseFreqs),
	}
}

func (i *Index) CorrectQuery(query string) string {
	terms := Tokenize(query)
	if len(terms) == 0 {
		return strings.TrimSpace(strings.ToLower(query))
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	corrected := make([]string, 0, len(terms))
	for _, term := range terms {
		best := term
		bestDistance := 3
		bestFreq := 0
		for candidate, postings := range i.postings {
			distance := levenshtein(term, candidate)
			if distance > 2 {
				continue
			}
			freq := len(postings)
			if distance < bestDistance || (distance == bestDistance && freq > bestFreq) {
				best = candidate
				bestDistance = distance
				bestFreq = freq
			}
		}
		corrected = append(corrected, best)
	}
	return strings.Join(corrected, " ")
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

func buildPhraseFreqs(doc model.Document) map[string]int {
	phraseFreqs := make(map[string]int)

	sources := []string{
		doc.Title,
		doc.Description,
		doc.Content,
	}

	for _, source := range sources {
		terms := Tokenize(source)
		if len(terms) == 0 {
			continue
		}
		for size := 2; size <= 4; size++ {
			for start := 0; start+size <= len(terms); start++ {
				phrase := strings.Join(terms[start:start+size], " ")
				phraseFreqs[phrase]++
			}
		}
	}

	return phraseFreqs
}

func countPrefixMatches(candidate, prefix string) int {
	candidateParts := strings.Fields(candidate)
	prefixParts := strings.Fields(prefix)
	if len(candidateParts) == 0 || len(prefixParts) == 0 || len(prefixParts) > len(candidateParts) {
		return 0
	}

	for idx := 0; idx < len(prefixParts)-1; idx++ {
		if candidateParts[idx] != prefixParts[idx] {
			return 0
		}
	}

	last := len(prefixParts) - 1
	if !strings.HasPrefix(candidateParts[last], prefixParts[last]) {
		return 0
	}

	return len(prefixParts)
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			current[j] = min(
				min(current[j-1]+1, prev[j]+1),
				prev[j-1]+cost,
			)
		}
		prev = current
	}
	return prev[len(b)]
}

var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "for": {}, "from": {},
	"has": {}, "he": {}, "in": {}, "is": {}, "it": {}, "its": {}, "of": {}, "on": {}, "that": {}, "the": {},
	"to": {}, "was": {}, "were": {}, "will": {}, "with": {},
}
