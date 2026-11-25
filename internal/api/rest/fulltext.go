package rest

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/Bharanipbk/gideondb/internal/analysis"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/segment"
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
	switch r.Syntax {
	case "":
	case core.QuerySyntaxSimple:
		if r.Text == "" {
			return fmt.Errorf("%w: syntax requires text", core.ErrInvalidArgument)
		}
		if _, err := segment.ParseSimpleQuery(r.Text); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: syntax must be omitted or %q", core.ErrInvalidArgument, core.QuerySyntaxSimple)
	}
	if err := r.matchSpec().Validate(); err != nil {
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
		if request.Syntax == core.QuerySyntaxSimple {
			query = segment.HighlightTerms(query)
		}
	}
	analyzer := analysis.MustGet(config.TextAnalyzer)
	options := analysis.HighlightOptions{PreTag: h.PreTag, PostTag: h.PostTag, FragmentSize: h.FragmentSize, Fragments: h.Fragments, Raw: h.Raw}
	for i := range results {
		if results[i].Text != "" {
			results[i].Highlights = analyzer.Highlight(results[i].Text, query, options)
		}
	}
}

// matchSpec is what the request counts over its matching records.
func (r searchRequest) matchSpec() core.MatchSpec {
	return core.MatchSpec{Facets: r.Facets, Aggregations: r.Aggregations}
}

// matchCounts counts facets and aggregations over the given shards on this
// node (nil means every shard).
func (s *Server) matchCounts(collection string, request searchRequest, filter *metadata.Expr, shards []uint32) (core.MatchCounts, error) {
	if request.matchSpec().Empty() {
		return core.NewMatchCounts(), nil
	}
	return s.engine.Aggregate(collection, request.Namespace, filter, request.Text, request.Syntax, request.matchSpec(), shards)
}

// addMatchResults adds facets and aggregations to a response.
func addMatchResults(response map[string]any, request searchRequest, counts core.MatchCounts) {
	if len(request.Facets) > 0 {
		response["facets"] = counts.Facets.Top(request.Facets)
	}
	if len(request.Aggregations) > 0 {
		response["aggregations"] = counts.Aggregations.Results(request.Aggregations)
	}
}

// suggest serves GET /v1/collections/{name}/suggest?text=...&size=N.
func (s *Server) suggest(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if text == "" || len(text) > 1024 {
		writeError(w, fmt.Errorf("%w: text must be 1 to 1024 bytes", core.ErrInvalidArgument))
		return
	}
	size := 5
	if raw := r.URL.Query().Get("size"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, fmt.Errorf("%w: size must be between 1 and 50", core.ErrInvalidArgument))
			return
		}
		size = parsed
	}
	result, err := s.engine.Suggest(r.PathValue("name"), text, size)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
