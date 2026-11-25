package analysis

import (
	"html"
	"sort"
	"strings"
	"unicode/utf8"
)

// HighlightOptions controls Highlight. Zero values select the defaults.
type HighlightOptions struct {
	PreTag       string // default "<em>"
	PostTag      string // default "</em>"
	FragmentSize int    // target fragment length in bytes; default 150
	Fragments    int    // maximum fragments; default 3
	// Escape HTML-escapes the text between tags (default true via
	// HighlightOptions.Normalized when Raw is false).
	Raw bool
}

func (o HighlightOptions) normalized() HighlightOptions {
	if o.PreTag == "" && o.PostTag == "" {
		o.PreTag, o.PostTag = "<em>", "</em>"
	}
	if o.FragmentSize <= 0 {
		o.FragmentSize = 150
	}
	if o.Fragments <= 0 {
		o.Fragments = 3
	}
	return o
}

// Highlight returns up to opts.Fragments snippets of text around the
// tokens whose analyzed term appears in query's analyzed terms, with each
// match wrapped in the tags. Fragments are ordered by how many matches
// they hold, then by position. It returns nil when nothing matches.
func (a *Analyzer) Highlight(text, query string, opts HighlightOptions) []string {
	opts = opts.normalized()
	want := map[string]struct{}{}
	for _, term := range a.Terms(query) {
		want[term] = struct{}{}
	}
	if len(want) == 0 {
		return nil
	}
	var matches []Token
	for _, token := range a.Analyze(text) {
		if _, ok := want[token.Term]; ok {
			matches = append(matches, token)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	type fragment struct {
		start, end int
		hits       []Token
	}
	var fragments []fragment
	for i := 0; i < len(matches); {
		center := matches[i]
		start := snapBack(text, max(0, center.Start-opts.FragmentSize/3))
		end := snapForward(text, min(len(text), start+opts.FragmentSize))
		if end < center.End {
			end = center.End
		}
		f := fragment{start: start, end: end}
		for i < len(matches) && matches[i].End <= end {
			f.hits = append(f.hits, matches[i])
			i++
		}
		fragments = append(fragments, f)
	}
	sort.SliceStable(fragments, func(i, j int) bool { return len(fragments[i].hits) > len(fragments[j].hits) })
	fragments = fragments[:min(len(fragments), opts.Fragments)]
	sort.Slice(fragments, func(i, j int) bool { return fragments[i].start < fragments[j].start })
	escape := func(s string) string {
		if opts.Raw {
			return s
		}
		return html.EscapeString(s)
	}
	out := make([]string, 0, len(fragments))
	for _, f := range fragments {
		var b strings.Builder
		if f.start > 0 {
			b.WriteString("…")
		}
		cursor := f.start
		for _, hit := range f.hits {
			b.WriteString(escape(text[cursor:hit.Start]))
			b.WriteString(opts.PreTag)
			b.WriteString(escape(text[hit.Start:hit.End]))
			b.WriteString(opts.PostTag)
			cursor = hit.End
		}
		b.WriteString(escape(text[cursor:f.end]))
		if f.end < len(text) {
			b.WriteString("…")
		}
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}

// snapBack moves offset back to a word boundary (at most 20 bytes) and to
// a rune boundary.
func snapBack(text string, offset int) int {
	for limit := offset - 20; offset > 0 && offset > limit && !isSpaceByte(text[offset-1]); offset-- {
	}
	for offset > 0 && !utf8.RuneStart(text[offset]) {
		offset--
	}
	return offset
}

func snapForward(text string, offset int) int {
	for limit := offset + 20; offset < len(text) && offset < limit && !isSpaceByte(text[offset]); offset++ {
	}
	for offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset++
	}
	return offset
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\n' || b == '\t' || b == '\r' }
