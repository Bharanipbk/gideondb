package grpcapi

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/api/rest"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestIngestDocumentThroughRealRESTHandler(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	server := &Server{engine: db, options: Options{DocumentIngest: rest.New(db, nil).DocumentIngestHandler()}}
	ctx := context.Background()
	if _, err := server.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: &v1.Collection{
		Name: "kb", Dimension: 2, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, ShardCount: 1, Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT},
	}}); err != nil {
		t.Fatal(err)
	}
	_, err = server.IngestDocument(ctx, &v1.IngestDocumentRequest{Collection: "kb", Filename: "a.txt", Content: []byte("hello")})
	if status.Code(err) != codes.InvalidArgument || !strings.Contains(status.Convert(err).Message(), "vectorizer") {
		t.Fatalf("no-vectorizer error = %v", err)
	}
	_, err = server.IngestDocument(ctx, &v1.IngestDocumentRequest{Collection: "missing", Filename: "a.txt", Content: []byte("hello")})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("missing collection = %v", err)
	}
}
