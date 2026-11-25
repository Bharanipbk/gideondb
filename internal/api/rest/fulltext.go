package rest

import (
	"fmt"

	"github.com/Bharanipbk/gideondb/internal/analysis"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// highlightRequest asks for snippets of each result's record text with the
// query terms wrapped in tags. Query defaults to the search's text.
type highlightRequest struct {
	Query        string `json:"query,omitempty"`
	PreTag       string `json:"pre_tag,omitempty"`
	PostTag      string `json:"post_tag,omitempty"`
	FragmentSize int    `json:"fragment_size,omitempty"`
	Fragments    int    `json:"fragments,omitempty"`
	// Raw skips HTML-escaping the text around the tags.
	Raw bool `json:"raw,omitempty"`
}

func (r searchRequest) validateFullText() error {
	if err := core.ValidateFacets(r.Facets); err != nil {
		return err
	}
	if h := r.Highlight; h != nil {
		if h.Query == "" && r.Text == "" {
			return fmt.Errorf("%w: highlight needs a text query or highlight.query", core.ErrInvalidArgument)
		}
		if err := core.ValidateText(h.Query); err != nil {
			return err
		}
		if h.FragmentSize < 0 || h.FragmentSize > 10000 || h.Fragments < 0 || h.Fragments > 20 {
			return fmt.Errorf("%w: highlight fragment_size must be at most 10000 and fragments at most 20", core.ErrInvalidArgument)
		}
		if len(h.PreTag) > 64 || len(h.PostTag) > 64 {
			return fmt.Errorf("%w: highlight tags must be at most 64 bytes", core.ErrInvalidArgument)
		}
	}
	return nil
}

// needsText reports whether shard stages must attach record text.
func (r searchRequest) needsText() bool { return r.Highlight != nil }

// applyHighlights fills Highlights from each result's attached Text.
func applyHighlights(config core.CollectionConfig, request searchRequest, results []core.SearchResult) {
	h := request.Highlight
	if h == nil {
		return
	}
	query := h.Query
	if query == "" {
		query = request.Text
	}
	analyzer := analysis.MustGet(config.TextAnalyzer)
	options := analysis.HighlightOptions{PreTag: h.PreTag, PostTag: h.PostTag, FragmentSize: h.FragmentSize, Fragments: h.Fragments, Raw: h.Raw}
	for i := range results {
		if results[i].Text != "" {
			results[i].Highlights = analyzer.Highlight(results[i].Text, query, options)
		}
	}
}

// localFacets counts facets over every shard of a collection on this node.
func (s *Server) localFacets(collection string, request searchRequest, filter *metadata.Expr) (map[string][]core.FacetBucket, error) {
	if len(request.Facets) == 0 {
		return nil, nil
	}
	return s.engine.Facets(collection, request.Namespace, filter, request.Text, request.Facets)
}
