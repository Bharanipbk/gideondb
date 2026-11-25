package grpcapi

import (
	"context"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestSearchFacetsAndHighlights(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{engine: db}
	ctx := context.Background()
	if _, err := server.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: &v1.Collection{
		Name: "docs", Dimension: 2, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, ShardCount: 2, TextAnalyzer: "english",
		Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT},
	}}); err != nil {
		t.Fatal(err)
	}
	described, err := server.DescribeCollection(ctx, &v1.DescribeCollectionRequest{Name: "docs"})
	if err != nil || described.GetCollection().GetTextAnalyzer() != "english" {
		t.Fatalf("text_analyzer not round-tripped: %v %v", described, err)
	}
	for _, record := range []struct{ id, text, kind string }{{"a", "Indexing vectors quickly", "guide"}, {"b", "An index of recipes", "book"}, {"c", "Weather report", "news"}} {
		meta, _ := structpb.NewStruct(map[string]any{"kind": record.kind})
		if _, err := server.Upsert(ctx, &v1.UpsertRequest{Collection: "docs", Record: &v1.Record{Id: record.id, Vector: []float32{1, 0}, Text: record.text, Metadata: meta}}); err != nil {
			t.Fatal(err)
		}
	}
	response, err := server.Search(ctx, &v1.SearchRequest{
		Collection: "docs", Text: "indexes", TopK: 5,
		Facets:    []*v1.FacetRequest{{Field: "kind"}},
		Highlight: &v1.HighlightOptions{PreTag: "[", PostTag: "]"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetResults()) != 2 {
		t.Fatalf("results = %v", response.GetResults())
	}
	for _, result := range response.GetResults() {
		if len(result.GetHighlights()) != 1 || !strings.Contains(result.GetHighlights()[0], "[") {
			t.Fatalf("highlights = %v", result.GetHighlights())
		}
	}
	buckets := response.GetFacets()["kind"].GetBuckets()
	if len(buckets) != 2 || buckets[0].GetCount() != 1 {
		t.Fatalf("facets = %v", response.GetFacets())
	}
	if _, err := server.Search(ctx, &v1.SearchRequest{Collection: "docs", Vector: []float32{1, 0}, TopK: 1, Highlight: &v1.HighlightOptions{}}); err == nil {
		t.Fatal("highlight without query accepted")
	}
}

func TestSearchAggregationsSortAndSuggest(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{engine: db}
	ctx := context.Background()
	if _, err := server.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: &v1.Collection{
		Name: "shop", Dimension: 2, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, ShardCount: 2, TextAnalyzer: "english",
		Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id, text string
		price    float64
	}{{"a", "red running shoes", 120}, {"b", "blue running shoes", 80}, {"c", "red trail shoes", 95}, {"d", "winter jacket", 200}} {
		meta, _ := structpb.NewStruct(map[string]any{"price": item.price})
		if _, err := server.Upsert(ctx, &v1.UpsertRequest{Collection: "shop", Record: &v1.Record{Id: item.id, Vector: []float32{1, 0}, Text: item.text, Metadata: meta}}); err != nil {
			t.Fatal(err)
		}
	}
	to := 100.0
	response, err := server.Search(ctx, &v1.SearchRequest{
		Collection: "shop", Text: "+shoes -blue", Syntax: "simple", TopK: 2,
		Sort: []*v1.SortField{{Field: "price", Order: "desc"}},
		Aggregations: map[string]*v1.AggregationRequest{
			"stats": {Type: "stats", Field: "price"},
			"bands": {Type: "range", Field: "price", Ranges: []*v1.AggregationRange{{To: &to}, {From: &to, Key: "premium"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetResults()) != 2 || response.GetResults()[0].GetId() != "a" || response.GetResults()[1].GetId() != "c" {
		t.Fatalf("sorted results = %v", response.GetResults())
	}
	stats := response.GetAggregations()["stats"]
	if stats.GetCount() != 2 || stats.GetMax() != 120 || stats.GetAvg() != 107.5 {
		t.Fatalf("stats = %v", stats)
	}
	bands := response.GetAggregations()["bands"].GetBuckets()
	if len(bands) != 2 || bands[0].GetCount() != 1 || bands[1].GetKey().GetStringValue() != "premium" || bands[1].GetCount() != 1 {
		t.Fatalf("bands = %v", bands)
	}
	if _, err := server.Search(ctx, &v1.SearchRequest{Collection: "shop", Vector: []float32{1, 0}, TopK: 1, Sort: []*v1.SortField{{Field: "price", Order: "up"}}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad sort order = %v", err)
	}
	suggestions, err := server.Suggest(ctx, &v1.SuggestRequest{Collection: "shop", Text: "runn"})
	if err != nil || len(suggestions.GetCompletions()) == 0 || suggestions.GetCompletions()[0].GetWord() != "running" {
		t.Fatalf("suggest = %v, %v", suggestions, err)
	}
	if fixed, err := server.Suggest(ctx, &v1.SuggestRequest{Collection: "shop", Text: "wintr jaket "}); err != nil || fixed.GetDidYouMean() != "winter jacket " {
		t.Fatalf("did you mean = %v, %v", fixed, err)
	}
	if _, err := server.Suggest(ctx, &v1.SuggestRequest{Collection: "shop", Text: "x", Size: 99}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("size 99 = %v", err)
	}
}

func TestIngestDocumentBridgesToREST(t *testing.T) {
	var seen *http.Request
	var file []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("multipart: %v", err)
		}
		f, header, err := r.FormFile("file")
		if err != nil || header.Filename != "guide.md" || header.Header.Get("Content-Type") != "text/markdown" {
			t.Errorf("file part: %v %+v", err, header)
		} else {
			file, _ = io.ReadAll(f)
		}
		_, _ = w.Write([]byte(`{"document_id":"guide","title":"Guide","format":"markdown","chunks":3,"characters":2400,"replaced_chunks_removed":1}`))
	})
	server := &Server{options: Options{DocumentIngest: handler}}
	metadata, _ := structpb.NewStruct(map[string]any{"team": "docs"})
	response, err := server.IngestDocument(context.Background(), &v1.IngestDocumentRequest{
		Collection: "kb", Content: []byte("# Guide"), Filename: "guide.md", ContentType: "text/markdown",
		Id: "guide", Namespace: "ns", Metadata: metadata, ChunkSize: 500, ChunkOverlap: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen.PathValue("name") != "kb" || seen.FormValue("id") != "guide" || seen.FormValue("namespace") != "ns" || seen.FormValue("chunk_size") != "500" ||
		seen.FormValue("chunk_overlap") != "50" || seen.FormValue("metadata") != `{"team":"docs"}` || string(file) != "# Guide" {
		t.Fatalf("bridged form = %v", seen.MultipartForm.Value)
	}
	if response.GetDocumentId() != "guide" || response.GetChunks() != 3 || response.GetCharacters() != 2400 || response.GetReplacedChunksRemoved() != 1 {
		t.Fatalf("response = %v", response)
	}
	// Errors keep the REST message and map to gRPC codes.
	failing := &Server{options: Options{DocumentIngest: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"code":"extraction_failed","message":"PDF has no extractable text"}`))
	})}}
	if _, err := failing.IngestDocument(context.Background(), &v1.IngestDocumentRequest{Collection: "kb", Content: []byte("x")}); status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "PDF has no extractable text" {
		t.Fatalf("mapped error = %v", err)
	}
	if _, err := (&Server{}).IngestDocument(context.Background(), &v1.IngestDocumentRequest{Collection: "kb", Content: []byte("x")}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("unconfigured = %v", err)
	}
	if err := authorize(&Principal{Role: "reader"}, v1.GideonDBService_IngestDocument_FullMethodName, &v1.IngestDocumentRequest{Collection: "kb"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("reader may ingest: %v", err)
	}
	if err := authorize(&Principal{Role: "reader", CollectionPrefixes: []string{"a."}}, v1.GideonDBService_Suggest_FullMethodName, &v1.SuggestRequest{Collection: "b.x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("suggest ignores collection scope: %v", err)
	}
}
