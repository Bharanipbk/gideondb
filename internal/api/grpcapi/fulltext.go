package grpcapi

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/analysis"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

func fromProtoFacets(values []*v1.FacetRequest) []core.FacetRequest {
	if len(values) == 0 {
		return nil
	}
	out := make([]core.FacetRequest, len(values))
	for i, value := range values {
		out[i] = core.FacetRequest{Field: value.GetField(), Size: int(value.GetSize())}
	}
	return out
}

// highlightJSON mirrors the REST highlight object for the coordinator.
type highlightJSON struct {
	Query        string `json:"query,omitempty"`
	PreTag       string `json:"pre_tag,omitempty"`
	PostTag      string `json:"post_tag,omitempty"`
	FragmentSize int    `json:"fragment_size,omitempty"`
	Fragments    int    `json:"fragments,omitempty"`
	Raw          bool   `json:"raw,omitempty"`
}

func fromProtoHighlight(value *v1.HighlightOptions) *highlightJSON {
	if value == nil {
		return nil
	}
	return &highlightJSON{Query: value.GetQuery(), PreTag: value.GetPreTag(), PostTag: value.GetPostTag(), FragmentSize: int(value.GetFragmentSize()), Fragments: int(value.GetFragments()), Raw: value.GetRaw()}
}

func toProtoFacets(values map[string][]core.FacetBucket) (map[string]*v1.FacetBuckets, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]*v1.FacetBuckets, len(values))
	for field, buckets := range values {
		list := make([]*v1.FacetBucket, len(buckets))
		for i, bucket := range buckets {
			value, err := structpb.NewValue(bucket.Value)
			if err != nil {
				return nil, err
			}
			list[i] = &v1.FacetBucket{Value: value, Count: uint64(bucket.Count)}
		}
		out[field] = &v1.FacetBuckets{Buckets: list}
	}
	return out, nil
}

// applyLocalFullText adds highlights and facets for a single-node search.
func (s *Server) applyLocalFullText(request *v1.SearchRequest, filter *metadata.Expr, results []core.SearchResult, response *v1.SearchResponse) error {
	if h := request.GetHighlight(); h != nil {
		query := h.GetQuery()
		if query == "" {
			query = request.GetText()
		}
		if query == "" {
			return status.Error(codes.InvalidArgument, "highlight needs a text query or highlight.query")
		}
		if h.GetFragmentSize() > 10000 || h.GetFragments() > 20 || len(h.GetPreTag()) > 64 || len(h.GetPostTag()) > 64 {
			return status.Error(codes.InvalidArgument, "highlight options are out of range")
		}
		if err := s.engine.AttachText(request.GetCollection(), results); err != nil {
			return mapError(err)
		}
		config, err := s.engine.CollectionConfig(request.GetCollection())
		if err != nil {
			return mapError(err)
		}
		analyzer := analysis.MustGet(config.TextAnalyzer)
		options := analysis.HighlightOptions{PreTag: h.GetPreTag(), PostTag: h.GetPostTag(), FragmentSize: int(h.GetFragmentSize()), Fragments: int(h.GetFragments()), Raw: h.GetRaw()}
		for i, result := range results {
			if result.Text != "" {
				response.Results[i].Highlights = analyzer.Highlight(result.Text, query, options)
			}
		}
	}
	if facets := fromProtoFacets(request.GetFacets()); facets != nil {
		top, err := s.engine.Facets(request.GetCollection(), request.GetNamespace(), filter, request.GetText(), facets)
		if err != nil {
			return mapError(err)
		}
		if response.Facets, err = toProtoFacets(top); err != nil {
			return status.Error(codes.Internal, "convert facets")
		}
	}
	return nil
}
