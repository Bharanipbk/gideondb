package segment

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Bharanipbk/gideondb/internal/core"
)

// The "simple" query syntax, a subset of Lucene's simple_query_string:
//
//	word        should match (OR with the other clauses)
//	+word       must match          -word   must not match
//	"a b c"     phrase: terms adjacent and in order (+"..." and -"..." work)
//	word~       fuzzy: edit distance 1 (2 for words of 6+ letters); word~2 sets it
//	pre*        prefix: up to maxExpansions indexed terms starting with pre
//	word^2.5    boost a word or phrase
//
// Words are analyzed with the collection analyzer, and synonyms expand them.

const (
	maxExpansions = 64
	maxClauses    = 64
	fuzzyWeight   = 0.8
)

type occur uint8

const (
	occurShould occur = iota
	occurMust
	occurMustNot
)

type clauseKind uint8

const (
	clauseTerm clauseKind = iota
	clauseFuzzy
	clausePrefix
	clausePhrase
)

// rawClause is a parsed, not yet analyzed, clause.
type rawClause struct {
	occur occur
	kind  clauseKind
	text  string // word, prefix stem, or phrase text
	edits int
	boost float32
}

// ParseSimpleQuery parses the simple syntax and reports syntax errors.
func ParseSimpleQuery(text string) ([]rawClause, error) {
	var clauses []rawClause
	runes := []rune(text)
	for i := 0; i < len(runes); {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}
		clause := rawClause{boost: 1}
		switch runes[i] {
		case '+':
			clause.occur = occurMust
			i++
		case '-':
			clause.occur = occurMustNot
			i++
		}
		if i < len(runes) && runes[i] == '"' {
			end := i + 1
			for end < len(runes) && runes[end] != '"' {
				end++
			}
			if end >= len(runes) {
				return nil, fmt.Errorf("%w: unterminated phrase in query", core.ErrInvalidArgument)
			}
			clause.kind, clause.text = clausePhrase, string(runes[i+1:end])
			i = end + 1
		} else {
			start := i
			for i < len(runes) && !unicode.IsSpace(runes[i]) && runes[i] != '^' {
				i++
			}
			word := string(runes[start:i])
			switch {
			case strings.HasSuffix(word, "*"):
				clause.kind, clause.text = clausePrefix, strings.TrimRight(word, "*")
			case strings.Contains(word, "~"):
				base, distance, _ := strings.Cut(word, "~")
				clause.kind, clause.text = clauseFuzzy, base
				if distance != "" {
					n, err := strconv.Atoi(distance)
					if err != nil || n < 0 || n > 2 {
						return nil, fmt.Errorf("%w: fuzzy distance must be 0, 1 or 2", core.ErrInvalidArgument)
					}
					clause.edits = n
				} else {
					clause.edits = -1 // by length
				}
			default:
				clause.text = word
			}
		}
		if i < len(runes) && runes[i] == '^' {
			start := i + 1
			end := start
			for end < len(runes) && !unicode.IsSpace(runes[end]) {
				end++
			}
			boost, err := strconv.ParseFloat(string(runes[start:end]), 32)
			if err != nil || boost <= 0 || boost > 1000 || math.IsNaN(boost) {
				return nil, fmt.Errorf("%w: boost must be a number in (0, 1000]", core.ErrInvalidArgument)
			}
			clause.boost = float32(boost)
			i = end
		}
		if clause.text == "" && clause.kind != clausePhrase {
			continue
		}
		clauses = append(clauses, clause)
		if len(clauses) > maxClauses {
			return nil, fmt.Errorf("%w: at most %d query clauses", core.ErrInvalidArgument, maxClauses)
		}
	}
	return clauses, nil
}

// HighlightTerms returns the plain words of a simple query (operators,
// boosts and phrase quotes removed; must-not clauses dropped) for
// highlighting.
func HighlightTerms(text string) string {
	clauses, err := ParseSimpleQuery(text)
	if err != nil {
		return text
	}
	words := make([]string, 0, len(clauses))
	for _, clause := range clauses {
		if clause.occur != occurMustNot {
			words = append(words, clause.text)
		}
	}
	return strings.Join(words, " ")
}

// variant is one indexed term a query position accepts, with its weight.
type variant struct {
	term   string
	weight float32
}

type compiledClause struct {
	occur occur
	boost float32
	// positions holds one variant group per term; a non-phrase clause has
	// exactly one group. offsets are the terms' token positions relative
	// to the first, including gaps left by stopwords.
	positions [][]variant
	offsets   []int
	phrase    bool
}

// textQuery is a query compiled against one segment's vocabulary.
type textQuery struct {
	index      *lexicalIndex
	clauses    []compiledClause
	hasMust    bool
	hasMustNot bool
	termScore  map[string]func(uint64) float32
	records    func(uint64) (core.Record, bool)
	phraseMemo map[uint64]map[int]string
}

// compileTextQuery analyzes and expands a text query for this segment.
// syntax "" treats the text as plain words (each a should clause), which
// with no synonyms scores exactly like bm25Scorer.
func (l *lexicalIndex) compileTextQuery(text, syntax string, records func(uint64) (core.Record, bool)) (*textQuery, error) {
	var raw []rawClause
	if syntax == core.QuerySyntaxSimple {
		var err error
		if raw, err = ParseSimpleQuery(text); err != nil {
			return nil, err
		}
	} else {
		raw = []rawClause{{kind: clausePhrase, text: text, boost: 1}}
	}
	q := &textQuery{index: l, termScore: map[string]func(uint64) float32{}, records: records, phraseMemo: map[uint64]map[int]string{}}
	for _, clause := range raw {
		compiled := compiledClause{occur: clause.occur, boost: clause.boost}
		switch clause.kind {
		case clausePhrase:
			compiled.positions, compiled.offsets = l.analyzePhrase(clause.text)
			compiled.phrase = syntax == core.QuerySyntaxSimple && len(compiled.positions) > 1
			if syntax != core.QuerySyntaxSimple {
				// Plain text: every distinct word is its own should clause.
				seen := map[string]struct{}{}
				for _, group := range compiled.positions {
					if _, dup := seen[group[0].term]; dup {
						continue
					}
					seen[group[0].term] = struct{}{}
					q.clauses = append(q.clauses, compiledClause{occur: occurShould, boost: 1, positions: [][]variant{group}})
				}
				continue
			}
		case clauseTerm:
			compiled.positions, compiled.offsets = l.analyzePhrase(clause.text)
			if len(compiled.positions) > 1 {
				// "e-mail" analyzes to two terms; treat it as a phrase.
				compiled.phrase = true
			}
		case clauseFuzzy:
			terms := l.analyzer.Terms(clause.text)
			if len(terms) == 1 {
				compiled.positions = [][]variant{l.expandFuzzy(terms[0], clause.edits)}
			}
		case clausePrefix:
			prefix := l.analyzer.Normalize(clause.text)
			if prefix != "" {
				compiled.positions = [][]variant{l.expandPrefix(prefix)}
			}
		}
		if len(compiled.positions) == 0 {
			if compiled.occur == occurMust {
				// A required clause with no indexable terms cannot match.
				compiled.positions = [][]variant{{}}
			} else {
				continue
			}
		}
		q.clauses = append(q.clauses, compiled)
	}
	for _, clause := range q.clauses {
		q.hasMust = q.hasMust || clause.occur == occurMust
		q.hasMustNot = q.hasMustNot || clause.occur == occurMustNot
		for _, group := range clause.positions {
			for _, v := range group {
				if _, ok := q.termScore[v.term]; !ok {
					q.termScore[v.term] = l.termScorer(v.term)
				}
			}
		}
	}
	return q, nil
}

// analyzePhrase returns each term's variants and its position offset.
func (l *lexicalIndex) analyzePhrase(text string) ([][]variant, []int) {
	tokens := l.analyzer.Analyze(text)
	positions := make([][]variant, len(tokens))
	offsets := make([]int, len(tokens))
	for i, token := range tokens {
		positions[i] = l.expandTerm(token.Term)
		offsets[i] = token.Position - tokens[0].Position
	}
	return positions, offsets
}

// expandTerm returns the term and its synonyms.
func (l *lexicalIndex) expandTerm(term string) []variant {
	out := []variant{{term, 1}}
	for _, synonym := range l.synonyms[term] {
		if synonym != term {
			out = append(out, variant{synonym, 1})
		}
	}
	return out
}

func (l *lexicalIndex) expandFuzzy(term string, edits int) []variant {
	if edits < 0 {
		edits = 1
		if utf8.RuneCountInString(term) >= 6 {
			edits = 2
		}
		if utf8.RuneCountInString(term) <= 2 {
			edits = 0
		}
	}
	out := l.expandTerm(term)
	if edits == 0 {
		return out
	}
	type scored struct {
		term     string
		distance int
		postings int
	}
	var matches []scored
	for candidate, postings := range l.text {
		if candidate == term {
			continue
		}
		if d := boundedLevenshtein(term, candidate, edits); d <= edits {
			matches = append(matches, scored{candidate, d, len(postings)})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].distance != matches[j].distance {
			return matches[i].distance < matches[j].distance
		}
		if matches[i].postings != matches[j].postings {
			return matches[i].postings > matches[j].postings
		}
		return matches[i].term < matches[j].term
	})
	for _, match := range matches[:min(len(matches), maxExpansions)] {
		out = append(out, variant{match.term, fuzzyWeight})
	}
	return out
}

func (l *lexicalIndex) expandPrefix(prefix string) []variant {
	type scored struct {
		term     string
		postings int
	}
	var matches []scored
	for candidate, postings := range l.text {
		if strings.HasPrefix(candidate, prefix) {
			matches = append(matches, scored{candidate, len(postings)})
		}
	}
	// Prefer common terms, so short prefixes expand to useful words.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].postings != matches[j].postings {
			return matches[i].postings > matches[j].postings
		}
		return matches[i].term < matches[j].term
	})
	out := make([]variant, 0, min(len(matches), maxExpansions))
	for _, match := range matches[:min(len(matches), maxExpansions)] {
		out = append(out, variant{match.term, 1})
	}
	return out
}

// boundedLevenshtein returns the optimal-string-alignment distance (an
// adjacent transposition counts as one edit, as in Lucene), or limit+1 once
// it exceeds limit.
func boundedLevenshtein(a, b string, limit int) int {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > limit || -d > limit {
		return limit + 1
	}
	rows := [3][]int{make([]int, len(rb)+1), make([]int, len(rb)+1), make([]int, len(rb)+1)}
	for j := range rows[1] {
		rows[1][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		before, previous, current := rows[0], rows[1], rows[2]
		current[0] = i
		best := current[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				current[j] = min(current[j], before[j-2]+1)
			}
			best = min(best, current[j])
		}
		if best > limit {
			return limit + 1
		}
		rows[0], rows[1], rows[2] = previous, current, before
	}
	if d := rows[1][len(rb)]; d <= limit {
		return d
	}
	return limit + 1
}

// groupScore is the best-scoring variant of one position in a document.
func (q *textQuery) groupScore(group []variant, ordinal uint64) float32 {
	var best float32
	for _, v := range group {
		if postings := q.index.text[v.term]; postings != nil {
			if _, ok := postings[ordinal]; ok {
				if score := q.termScore[v.term](ordinal) * v.weight; score > best {
					best = score
				}
			}
		}
	}
	return best
}

func (q *textQuery) groupContains(group []variant, ordinal uint64) bool {
	for _, v := range group {
		if _, ok := q.index.text[v.term][ordinal]; ok {
			return true
		}
	}
	return false
}

// clauseMatches reports whether a document satisfies a clause.
func (q *textQuery) clauseMatches(clause compiledClause, ordinal uint64) bool {
	for _, group := range clause.positions {
		if !q.groupContains(group, ordinal) {
			return false
		}
	}
	if clause.phrase {
		return q.phraseMatches(clause, ordinal)
	}
	return true
}

// phraseMatches checks that the terms appear at their relative positions
// in the record's analyzed text.
func (q *textQuery) phraseMatches(clause compiledClause, ordinal uint64) bool {
	terms, ok := q.phraseMemo[ordinal]
	if !ok {
		terms = map[int]string{}
		if record, found := q.records(ordinal); found {
			for _, token := range q.index.analyzer.Analyze(record.Text) {
				terms[token.Position] = token.Term
			}
		}
		q.phraseMemo[ordinal] = terms
	}
	for start, term := range terms {
		if !groupHas(clause.positions[0], term) {
			continue
		}
		matched := true
		for i := 1; i < len(clause.positions); i++ {
			if !groupHas(clause.positions[i], terms[start+clause.offsets[i]]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func groupHas(group []variant, term string) bool {
	for _, v := range group {
		if v.term == term {
			return true
		}
	}
	return false
}

// allows reports whether a document passes the must and must-not clauses.
// Documents without any should or must match still pass; score decides.
func (q *textQuery) allows(ordinal uint64) bool {
	for _, clause := range q.clauses {
		switch clause.occur {
		case occurMust:
			if !q.clauseMatches(clause, ordinal) {
				return false
			}
		case occurMustNot:
			if q.clauseMatches(clause, ordinal) {
				return false
			}
		}
	}
	return true
}

// constrained reports whether allows can reject documents.
func (q *textQuery) constrained() bool { return q.hasMust || q.hasMustNot }

// score is the boosted BM25 sum of matching should and must clauses, or 0
// when the document fails allows.
func (q *textQuery) score(ordinal uint64) float32 {
	if !q.allows(ordinal) {
		return 0
	}
	var total float32
	for _, clause := range q.clauses {
		if clause.occur == occurMustNot || !q.clauseMatches(clause, ordinal) {
			continue
		}
		var clauseScore float32
		for _, group := range clause.positions {
			clauseScore += q.groupScore(group, ordinal)
		}
		total += clause.boost * clauseScore
	}
	return total
}

// candidates are documents holding a term of some positive clause.
func (q *textQuery) candidates() map[uint64]struct{} {
	out := map[uint64]struct{}{}
	for _, clause := range q.clauses {
		if clause.occur == occurMustNot {
			continue
		}
		for _, group := range clause.positions {
			for _, v := range group {
				for ordinal := range q.index.text[v.term] {
					out[ordinal] = struct{}{}
				}
			}
		}
	}
	return out
}

// search ranks candidate documents by score.
func (q *textQuery) search(allow func(uint64) bool, limit int) []scoredOrdinal {
	results := make([]scoredOrdinal, 0)
	for ordinal := range q.candidates() {
		if !allow(ordinal) {
			continue
		}
		if score := q.score(ordinal); score > 0 {
			results = append(results, scoredOrdinal{ordinal, score})
		}
	}
	return topScored(results, limit)
}
