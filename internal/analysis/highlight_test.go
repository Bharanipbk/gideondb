package analysis

import (
	"reflect"
	"strings"
	"testing"
)

func TestHighlightMarksStemmedMatches(t *testing.T) {
	text := "Vector databases index embeddings. Indexing makes search fast."
	got := MustGet("english").Highlight(text, "indexes", HighlightOptions{})
	want := []string{"Vector databases <em>index</em> embeddings. <em>Indexing</em> makes search fast."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("highlight = %q, want %q", got, want)
	}
}

func TestHighlightEscapesAndFragments(t *testing.T) {
	text := "<b>alpha</b> " + strings.Repeat("filler ", 60) + "alpha again"
	got := MustGet("").Highlight(text, "alpha", HighlightOptions{FragmentSize: 40, Fragments: 5, PreTag: "[", PostTag: "]"})
	if len(got) != 2 {
		t.Fatalf("want 2 fragments, got %q", got)
	}
	if !strings.HasPrefix(got[0], "&lt;b&gt;[alpha]&lt;/b&gt;") || !strings.HasSuffix(got[0], "…") {
		t.Fatalf("first fragment = %q", got[0])
	}
	if !strings.HasPrefix(got[1], "…") || !strings.Contains(got[1], "[alpha] again") {
		t.Fatalf("second fragment = %q", got[1])
	}
	raw := MustGet("").Highlight("<i>alpha</i>", "alpha", HighlightOptions{Raw: true})
	if raw[0] != "<i><em>alpha</em></i>" {
		t.Fatalf("raw highlight = %q", raw)
	}
}

func TestHighlightNoMatch(t *testing.T) {
	if got := MustGet("").Highlight("nothing here", "absent", HighlightOptions{}); got != nil {
		t.Fatalf("want nil, got %q", got)
	}
	if got := MustGet("").Highlight("the", "the", HighlightOptions{}); got != nil {
		t.Fatalf("stopword-only query must not highlight: %q", got)
	}
}

func TestHighlightKeepsRuneBoundaries(t *testing.T) {
	text := strings.Repeat("ünïcödé ", 40) + "target " + strings.Repeat("ünïcödé ", 40)
	for _, fragment := range MustGet("folding").Highlight(text, "target", HighlightOptions{FragmentSize: 33}) {
		if !strings.Contains(fragment, "<em>target</em>") || !utf8Valid(fragment) {
			t.Fatalf("bad fragment %q", fragment)
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }
