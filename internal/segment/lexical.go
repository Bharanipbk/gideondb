package segment

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/Bharanipbk/gideondb/internal/core"
)

// BM25 parameters follow the common Lucene defaults.
const (
	bm25K1 = 1.2
	bm25B  = 0.75
	// bm25Saturation maps an unbounded BM25 score into [0,1) for weighted
	// fusion without depending on per-segment maxima, so scores remain
	// comparable when shards and segments are merged.
	bm25Saturation = 2.0
)

// lexicalIndex holds per-segment inverted indexes for sparse vectors and
// tokenized text. Callers hold the segment lock.
type lexicalIndex struct {
	sparse     map[string]map[uint64]float32
	sparseNorm map[uint64]float64
	text       map[string]map[uint64]uint32
	docLength  map[uint64]uint32
	totalTerms uint64
}

func newLexicalIndex() *lexicalIndex {
	return &lexicalIndex{
		sparse: map[string]map[uint64]float32{}, sparseNorm: map[uint64]float64{},
		text: map[string]map[uint64]uint32{}, docLength: map[uint64]uint32{},
	}
}

func (l *lexicalIndex) add(ordinal uint64, record core.Record) {
	if len(record.SparseVector) != 0 {
		var squared float64
		for term, weight := range record.SparseVector {
			if weight == 0 {
				continue
			}
			postings := l.sparse[term]
			if postings == nil {
				postings = map[uint64]float32{}
				l.sparse[term] = postings
			}
			postings[ordinal] = weight
			squared += float64(weight) * float64(weight)
		}
		if squared > 0 {
			l.sparseNorm[ordinal] = math.Sqrt(squared)
		}
	}
	if record.Text != "" {
		frequencies := map[string]uint32{}
		var length uint32
		for _, token := range Tokenize(record.Text) {
			frequencies[token]++
			length++
		}
		if length == 0 {
			return
		}
		for token, frequency := range frequencies {
			postings := l.text[token]
			if postings == nil {
				postings = map[uint64]uint32{}
				l.text[token] = postings
			}
			postings[ordinal] = frequency
		}
		l.docLength[ordinal] = length
		l.totalTerms += uint64(length)
	}
}

func (l *lexicalIndex) remove(ordinal uint64, record core.Record) {
	for term := range record.SparseVector {
		if postings := l.sparse[term]; postings != nil {
			delete(postings, ordinal)
			if len(postings) == 0 {
				delete(l.sparse, term)
			}
		}
	}
	delete(l.sparseNorm, ordinal)
	if length, ok := l.docLength[ordinal]; ok {
		for _, token := range Tokenize(record.Text) {
			if postings := l.text[token]; postings != nil {
				delete(postings, ordinal)
				if len(postings) == 0 {
					delete(l.text, token)
				}
			}
		}
		delete(l.docLength, ordinal)
		l.totalTerms -= uint64(length)
	}
}

type scoredOrdinal struct {
	ordinal uint64
	score   float32
}

// searchSparse scores only documents sharing at least one query term
// (term-at-a-time accumulation) and returns cosine similarities.
func (l *lexicalIndex) searchSparse(query map[string]float32, allow func(uint64) bool, limit int) []scoredOrdinal {
	queryNorm := sparseNorm(query)
	if queryNorm == 0 {
		return nil
	}
	accumulator := map[uint64]float64{}
	for term, queryWeight := range query {
		if queryWeight == 0 {
			continue
		}
		for ordinal, weight := range l.sparse[term] {
			accumulator[ordinal] += float64(queryWeight) * float64(weight)
		}
	}
	results := make([]scoredOrdinal, 0, len(accumulator))
	for ordinal, product := range accumulator {
		norm := l.sparseNorm[ordinal]
		if norm == 0 || !allow(ordinal) {
			continue
		}
		results = append(results, scoredOrdinal{ordinal, float32(product / (queryNorm * norm))})
	}
	return topScored(results, limit)
}

// searchText ranks documents by Okapi BM25 over this segment's statistics,
// visiting only documents that contain a query token.
func (l *lexicalIndex) searchText(text string, allow func(uint64) bool, limit int) []scoredOrdinal {
	score := l.bm25Scorer(text)
	candidates := map[uint64]struct{}{}
	for _, token := range Tokenize(text) {
		for ordinal := range l.text[token] {
			candidates[ordinal] = struct{}{}
		}
	}
	results := make([]scoredOrdinal, 0, len(candidates))
	for ordinal := range candidates {
		if allow(ordinal) {
			results = append(results, scoredOrdinal{ordinal, score(ordinal)})
		}
	}
	return topScored(results, limit)
}

// bm25Scorer precomputes query statistics so individual candidates found by
// another signal can be scored exactly during weighted fusion.
func (l *lexicalIndex) bm25Scorer(text string) func(uint64) float32 {
	documents := len(l.docLength)
	if documents == 0 {
		return func(uint64) float32 { return 0 }
	}
	averageLength := float64(l.totalTerms) / float64(documents)
	type term struct {
		postings map[uint64]uint32
		idf      float64
	}
	var terms []term
	seen := map[string]struct{}{}
	for _, token := range Tokenize(text) {
		if _, duplicate := seen[token]; duplicate {
			continue
		}
		seen[token] = struct{}{}
		if postings := l.text[token]; len(postings) != 0 {
			frequency := float64(len(postings))
			terms = append(terms, term{postings, math.Log(1 + (float64(documents)-frequency+0.5)/(frequency+0.5))})
		}
	}
	return func(ordinal uint64) float32 {
		length, ok := l.docLength[ordinal]
		if !ok {
			return 0
		}
		var score float64
		for _, t := range terms {
			if tf := float64(t.postings[ordinal]); tf > 0 {
				score += t.idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(length)/averageLength))
			}
		}
		return float32(score)
	}
}

func topScored(results []scoredOrdinal, limit int) []scoredOrdinal {
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score || (results[i].score == results[j].score && results[i].ordinal < results[j].ordinal)
	})
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

func normalizeBM25(score float32) float32 { return score / (score + bm25Saturation) }

// stopwords is a small English list; tokens are otherwise kept verbatim
// (lowercased, no stemming) so behaviour is predictable across languages.
var stopwords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {}, "by": {}, "for": {}, "from": {},
	"in": {}, "is": {}, "it": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "this": {}, "to": {},
	"was": {}, "with": {},
}

// Tokenize lowercases text and splits on any rune that is not a letter or
// digit, dropping English stopwords.
func Tokenize(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	tokens := fields[:0]
	for _, field := range fields {
		if _, stop := stopwords[field]; !stop {
			tokens = append(tokens, field)
		}
	}
	return tokens
}
