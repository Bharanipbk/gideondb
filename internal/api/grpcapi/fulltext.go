package grpcapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/analysis"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/segment"
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
			if request.GetSyntax() == core.QuerySyntaxSimple {
				query = segment.HighlightTerms(query)
			}
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
	spec := core.MatchSpec{Facets: fromProtoFacets(request.GetFacets()), Aggregations: fromProtoAggregations(request.GetAggregations())}
	if !spec.Empty() {
		counts, err := s.engine.Aggregate(request.GetCollection(), request.GetNamespace(), filter, request.GetText(), request.GetSyntax(), spec, nil)
		if err != nil {
			return mapError(err)
		}
		if len(spec.Facets) > 0 {
			if response.Facets, err = toProtoFacets(counts.Facets.Top(spec.Facets)); err != nil {
				return status.Error(codes.Internal, "convert facets")
			}
		}
		if len(spec.Aggregations) > 0 {
			if response.Aggregations, err = toProtoAggregations(counts.Aggregations.Results(spec.Aggregations)); err != nil {
				return status.Error(codes.Internal, "convert aggregations")
			}
		}
	}
	return nil
}

func fromProtoSynonyms(groups []*v1.SynonymGroup) [][]string {
	if len(groups) == 0 {
		return nil
	}
	out := make([][]string, len(groups))
	for i, group := range groups {
		out[i] = group.GetWords()
	}
	return out
}

func toProtoSynonyms(groups [][]string) []*v1.SynonymGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]*v1.SynonymGroup, len(groups))
	for i, words := range groups {
		out[i] = &v1.SynonymGroup{Words: words}
	}
	return out
}

func fromProtoSort(fields []*v1.SortField) ([]core.SortField, error) {
	if len(fields) == 0 {
		return nil, nil
	}
	out := make([]core.SortField, len(fields))
	for i, field := range fields {
		out[i] = core.SortField{Field: field.GetField(), Order: field.GetOrder()}
	}
	return core.NormalizeSort(out)
}

func fromProtoAggregations(values map[string]*v1.AggregationRequest) map[string]core.AggregationRequest {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]core.AggregationRequest, len(values))
	for name, value := range values {
		request := core.AggregationRequest{Type: value.GetType(), Field: value.GetField(), Interval: value.GetInterval(), Size: int(value.GetSize())}
		for _, r := range value.GetRanges() {
			request.Ranges = append(request.Ranges, core.AggregationRange{Key: r.GetKey(), From: r.From, To: r.To})
		}
		out[name] = request
	}
	return out
}

func toProtoAggregations(values map[string]core.AggregationResult) (map[string]*v1.AggregationResult, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]*v1.AggregationResult, len(values))
	for name, value := range values {
		result := &v1.AggregationResult{Min: value.Min, Max: value.Max, Sum: value.Sum, Avg: value.Avg}
		if value.Count != nil {
			count := uint64(*value.Count)
			result.Count = &count
		}
		for _, bucket := range value.Buckets {
			key, err := structpb.NewValue(bucket.Key)
			if err != nil {
				return nil, err
			}
			result.Buckets = append(result.Buckets, &v1.AggregationBucket{Key: key, From: bucket.From, To: bucket.To, Count: uint64(bucket.Count)})
		}
		out[name] = result
	}
	return out, nil
}

// Suggest completes the last word of a typed query and proposes a fix.
func (s *Server) Suggest(_ context.Context, request *v1.SuggestRequest) (*v1.SuggestResponse, error) {
	if request.GetText() == "" || len(request.GetText()) > 1024 {
		return nil, status.Error(codes.InvalidArgument, "text must be 1 to 1024 bytes")
	}
	size := int(request.GetSize())
	if size == 0 {
		size = 5
	}
	if size > 50 {
		return nil, status.Error(codes.InvalidArgument, "size must be between 1 and 50")
	}
	result, err := s.engine.Suggest(request.GetCollection(), request.GetText(), size)
	if err != nil {
		return nil, mapError(err)
	}
	response := &v1.SuggestResponse{DidYouMean: result.DidYouMean}
	for _, completion := range result.Completions {
		response.Completions = append(response.Completions, &v1.SuggestCompletion{Text: completion.Text, Word: completion.Word, Count: uint64(completion.Count)})
	}
	return response, nil
}

// IngestDocument hands the file to the REST document pipeline, so format
// detection, extraction, chunking, embedding and replacement behave the same.
func (s *Server) IngestDocument(ctx context.Context, request *v1.IngestDocumentRequest) (*v1.IngestDocumentResponse, error) {
	if s.options.DocumentIngest == nil {
		return nil, status.Error(codes.Unimplemented, "document ingestion is not configured on this node")
	}
	if len(request.GetContent()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "content is required")
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	fields := map[string]string{"id": request.GetId(), "namespace": request.GetNamespace()}
	if request.GetChunkSize() != 0 {
		fields["chunk_size"] = strconv.FormatUint(uint64(request.GetChunkSize()), 10)
	}
	if request.GetChunkOverlap() != 0 {
		fields["chunk_overlap"] = strconv.FormatUint(uint64(request.GetChunkOverlap()), 10)
	}
	if request.GetMetadata() != nil {
		encoded, err := json.Marshal(request.GetMetadata().AsMap())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "metadata cannot be encoded")
		}
		fields["metadata"] = string(encoded)
	}
	for key, value := range fields {
		if value != "" {
			_ = form.WriteField(key, value)
		}
	}
	filename := request.GetFilename()
	if filename == "" {
		filename = "document"
	}
	header := textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name="file"; filename=%q`, filename)}}
	if request.GetContentType() != "" {
		header.Set("Content-Type", request.GetContentType())
	}
	part, err := form.CreatePart(header)
	if err != nil {
		return nil, status.Error(codes.Internal, "encode document")
	}
	_, _ = part.Write(request.GetContent())
	_ = form.Close()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/collections/"+url.PathEscape(request.GetCollection())+"/documents", &body)
	if err != nil {
		return nil, status.Error(codes.Internal, "construct document request")
	}
	httpRequest.Header.Set("Content-Type", form.FormDataContentType())
	httpRequest.SetPathValue("name", request.GetCollection())
	capture := &responseCapture{header: make(http.Header)}
	s.options.DocumentIngest.ServeHTTP(capture, httpRequest)
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	if capture.status != http.StatusOK {
		var problem struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(capture.body.Bytes(), &problem)
		code := httpStatusCode(capture.status)
		switch capture.status {
		case http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
			code = codes.InvalidArgument
		}
		if problem.Message == "" {
			problem.Message = http.StatusText(capture.status)
		}
		return nil, status.Error(code, problem.Message)
	}
	var result struct {
		DocumentID            string `json:"document_id"`
		Title                 string `json:"title"`
		Format                string `json:"format"`
		Chunks                uint32 `json:"chunks"`
		Characters            uint64 `json:"characters"`
		ReplacedChunksRemoved uint32 `json:"replaced_chunks_removed"`
	}
	if err := json.Unmarshal(capture.body.Bytes(), &result); err != nil {
		return nil, status.Error(codes.Internal, "decode document response")
	}
	return &v1.IngestDocumentResponse{DocumentId: result.DocumentID, Title: result.Title, Format: result.Format, Chunks: result.Chunks, Characters: result.Characters, ReplacedChunksRemoved: result.ReplacedChunksRemoved}, nil
}
