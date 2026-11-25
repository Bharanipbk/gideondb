package segment

import (
	"reflect"
	"sort"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func querySegment(t *testing.T, synonyms [][]string) *Segment {
	t.Helper()
	config := core.CollectionConfig{Name: "q", Dimension: 2, Metric: core.MetricCosine, ShardCount: 1, TextAnalyzer: "english", Synonyms: synonyms}.Normalized()
	s, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]string{
		"a": "the quick brown fox jumps over the lazy dog",
		"b": "a brown dog and a quick cat",
		"c": "television reviews and tv deals",
		"d": "vector database indexing guide",
		"e": "databases for vectors",
	}
	for id, text := range docs {
		if err := s.Upsert(core.Record{ID: id, Vector: []float32{1, 0}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func queryIDs(t *testing.T, s *Segment, text, syntax string) []string {
	t.Helper()
	results, err := s.SearchHybrid("", core.HybridQuery{Text: text, Syntax: syntax, TopK: 10}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(results))
	for i, result := range results {
		out[i] = result.ID
	}
	return out
}

func sortedIDs(values []string) []string { sort.Strings(values); return values }

func TestSimpleQuerySyntax(t *testing.T) {
	s := querySegment(t, nil)
	cases := []struct {
		query string
		want  []string
	}{
		{`"quick brown"`, []string{"a"}},    // phrase: adjacent in a only
		{`quick brown`, []string{"a", "b"}}, // OR
		{`+quick +cat`, []string{"b"}},      // both required
		{`brown -cat`, []string{"a"}},       // exclusion
		{`-cat brown dog`, []string{"a"}},   // order of clauses does not matter
		{`+"lazy dog" fox`, []string{"a"}},  // required phrase
		{`quikc~`, []string{"a", "b"}},      // fuzzy (edit distance 1 after stemming)
		{`databse~`, []string{"d", "e"}},    // fuzzy matches the stemmed vocabulary
		{`ind*`, []string{"d"}},             // prefix
		{`+brown +zebra`, nil},              // a required term absent everywhere
		{`"dog quick"`, nil},                // wrong order
	}
	for _, c := range cases {
		if got := sortedIDs(queryIDs(t, s, c.query, core.QuerySyntaxSimple)); !reflect.DeepEqual(got, sortedIDs(append([]string(nil), c.want...))) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("%s: got %v, want %v", c.query, got, c.want)
		}
	}
	// Boosts reorder: cat^10 puts b first even though a also matches brown.
	if got := queryIDs(t, s, `brown cat^10`, core.QuerySyntaxSimple); len(got) == 0 || got[0] != "b" {
		t.Errorf("boost order = %v", got)
	}
	if got := queryIDs(t, s, `brown^10 cat`, core.QuerySyntaxSimple); len(got) < 2 {
		t.Errorf("boost results = %v", got)
	}
}

func TestPlainTextScoresUnchangedByCompiledPath(t *testing.T) {
	s := querySegment(t, nil)
	plain, err := s.SearchHybrid("", core.HybridQuery{Text: "quick brown dog", TopK: 10}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	simple, err := s.SearchHybrid("", core.HybridQuery{Text: "quick brown dog", Syntax: core.QuerySyntaxSimple, TopK: 10}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != len(simple) {
		t.Fatalf("plain %v vs simple %v", plain, simple)
	}
	for i := range plain {
		if plain[i].ID != simple[i].ID || abs(plain[i].Score-simple[i].Score) > 1e-5 {
			t.Fatalf("row %d: plain %+v vs simple %+v", i, plain[i], simple[i])
		}
	}
}

func TestSynonymsExpandQueries(t *testing.T) {
	s := querySegment(t, [][]string{{"tv", "television"}, {"hound", "dog"}})
	if got := sortedIDs(queryIDs(t, s, "hound", "")); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("synonym (plain) = %v", got)
	}
	if got := queryIDs(t, s, `"lazy hound"`, core.QuerySyntaxSimple); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("synonym inside phrase = %v", got)
	}
	if got := queryIDs(t, s, "television", ""); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("synonym group = %v", got)
	}
}

func TestParseSimpleQueryErrors(t *testing.T) {
	for _, bad := range []string{`"open`, `word~5`, `word^0`, `word^abc`} {
		if _, err := ParseSimpleQuery(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got := HighlightTerms(`+"lazy dog" -cat quick~ ind*^2`); got != "lazy dog quick ind" {
		t.Errorf("HighlightTerms = %q", got)
	}
}

func TestBoundedLevenshtein(t *testing.T) {
	cases := []struct {
		a, b  string
		limit int
		want  int
	}{{"kitten", "sitting", 3, 3}, {"kitten", "sitting", 2, 3}, {"abc", "abc", 1, 0}, {"ab", "ba", 2, 1}, {"quikc", "quick", 1, 1}, {"café", "cafe", 1, 1}}
	for _, c := range cases {
		if got := boundedLevenshtein(c.a, c.b, c.limit); got != c.want {
			t.Errorf("lev(%s,%s,%d) = %d, want %d", c.a, c.b, c.limit, got, c.want)
		}
	}
}

func abs(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}
