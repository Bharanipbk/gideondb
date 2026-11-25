package segment

import (
	"github.com/Bharanipbk/gideondb/internal/analysis"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

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
	analyzer   *analysis.Analyzer
	// synonyms maps an analyzed term to every term of its synonym groups.
	synonyms map[string][]string
	// words counts documents per surface word (case- and accent-normalized,
	// not stemmed, stopwords removed) for completion and spelling hints.
	words map[string]uint32
}

func newLexicalIndex(analyzer *analysis.Analyzer, synonyms ...[]string) *lexicalIndex {
	if analyzer == nil {
		analyzer = analysis.MustGet("")
	}
	return &lexicalIndex{analyzer: analyzer, synonyms: synonymMap(analyzer, synonyms), words: map[string]uint32{},
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
		for _, token := range l.analyzer.Terms(record.Text) {
			frequencies[token]++
			length++
		}
		if length == 0 {
			return
		}
		for word := range l.surfaceWords(record.Text) {
			l.words[word]++
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
		for _, token := range l.analyzer.Terms(record.Text) {
			if postings := l.text[token]; postings != nil {
				delete(postings, ordinal)
				if len(postings) == 0 {
					delete(l.text, token)
				}
			}
		}
		for word := range l.surfaceWords(record.Text) {
			if l.words[word] <= 1 {
				delete(l.words, word)
			} else {
				l.words[word]--
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
	for _, token := range l.analyzer.Terms(text) {
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

// termScorer returns one term's BM25 contribution per document.
func (l *lexicalIndex) termScorer(term string) func(uint64) float32 {
	documents := len(l.docLength)
	postings := l.text[term]
	if documents == 0 || len(postings) == 0 {
		return func(uint64) float32 { return 0 }
	}
	averageLength := float64(l.totalTerms) / float64(documents)
	frequency := float64(len(postings))
	idf := math.Log(1 + (float64(documents)-frequency+0.5)/(frequency+0.5))
	return func(ordinal uint64) float32 {
		tf := float64(postings[ordinal])
		length, ok := l.docLength[ordinal]
		if tf == 0 || !ok {
			return 0
		}
		return float32(idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*float64(length)/averageLength)))
	}
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
	for _, token := range l.analyzer.Terms(text) {
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

// Tokenize returns the "standard" analyzer's terms: lowercase, split on any
// rune that is not a letter or digit, drop a short English stopword list.
func Tokenize(text string) []string { return analysis.MustGet(analysis.DefaultName).Terms(text) }

// synonymMap analyzes each synonym group and links every member to all
// members. Multi-word synonyms are kept only as single analyzed terms.
func synonymMap(analyzer *analysis.Analyzer, groups [][]string) map[string][]string {
	if len(groups) == 0 {
		return nil
	}
	out := map[string][]string{}
	for _, group := range groups {
		var terms []string
		for _, word := range group {
			if analyzed := analyzer.Terms(word); len(analyzed) == 1 {
				terms = append(terms, analyzed[0])
			}
		}
		for _, term := range terms {
			for _, other := range terms {
				if other != term && !contains(out[term], other) {
					out[term] = append(out[term], other)
				}
			}
		}
	}
	return out
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// maxSuggestWordBytes skips long tokens (IDs, hashes) in the word list.
const maxSuggestWordBytes = 40

// surfaceWords returns the distinct normalized words of text that survive
// the analyzer (so stopwords are excluded), before stemming.
func (l *lexicalIndex) surfaceWords(text string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, token := range l.analyzer.Analyze(text) {
		if word := l.analyzer.Normalize(text[token.Start:token.End]); len(word) <= maxSuggestWordBytes {
			out[word] = struct{}{}
		}
	}
	return out
}

// suggest returns document frequencies of words starting with prefix and of
// words within the fuzzy distance of each of the given words.
func (l *lexicalIndex) suggest(prefix string, words []string, limit int) core.SuggestCounts {
	counts := core.NewSuggestCounts()
	if prefix != "" {
		type scored struct {
			word  string
			count uint32
		}
		var matches []scored
		for word, count := range l.words {
			if strings.HasPrefix(word, prefix) && word != prefix {
				matches = append(matches, scored{word, count})
			}
		}
		sort.Slice(matches, func(i, j int) bool {
			if matches[i].count != matches[j].count {
				return matches[i].count > matches[j].count
			}
			return matches[i].word < matches[j].word
		})
		// Keep extra candidates so merged counts across segments stay accurate.
		for _, match := range matches[:min(len(matches), limit*4)] {
			counts.Completions[match.word] += int(match.count)
		}
	}
	for _, word := range words {
		if word == "" {
			continue
		}
		candidates := map[string]int{}
		if count, ok := l.words[word]; ok {
			candidates[word] = int(count)
		}
		edits := 1
		if utf8.RuneCountInString(word) >= 6 {
			edits = 2
		}
		if utf8.RuneCountInString(word) > 2 {
			for candidate, count := range l.words {
				if candidate != word && boundedLevenshtein(word, candidate, edits) <= edits {
					candidates[candidate] = int(count)
				}
			}
		}
		counts.Corrections[word] = candidates
	}
	return counts
}
