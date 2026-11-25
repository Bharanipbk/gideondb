package grpcapi

import (
	"context"
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
