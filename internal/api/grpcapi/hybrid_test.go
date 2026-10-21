package grpcapi

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/embedding"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

// keywordEmbedder maps "go" to x and "rust" to y so ranking is predictable.
type keywordEmbedder struct{}

func (keywordEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for i, text := range texts {
		vector := []float32{0, 0, 0.1}
		for position, word := range []string{"go", "rust"} {
			if strings.Contains(strings.ToLower(text), word) {
				vector[position] = 1
			}
		}
		vectors[i] = vector
	}
	return vectors, nil
}

func hybridClient(t *testing.T) (v1.GideonDBServiceClient, context.Context) {
	t.Helper()
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	registry := embedding.NewRegistryWithModels(embedding.NewModel(embedding.Config{Name: "keywords", Model: "keywords", Dimension: 3}, keywordEmbedder{}))
	listener := bufconn.Listen(1 << 20)
	server := NewServerWithOptions(db, Options{APIKey: "test-secret", Embedders: registry})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return v1.NewGideonDBServiceClient(connection), metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer test-secret")
}

func TestHybridSearchSparseAndText(t *testing.T) {
	client, ctx := hybridClient(t)
	collection := &v1.Collection{Name: "docs", Dimension: 3, ShardCount: 1, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE,
		Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT, Quantization: v1.Quantization_QUANTIZATION_BINARY}}
	created, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: collection})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetCollection().GetIndex().GetQuantization() != v1.Quantization_QUANTIZATION_BINARY || created.GetCollection().GetIndex().GetOversampling() != 4 {
		t.Fatalf("created index = %v", created.GetCollection().GetIndex())
	}
	records := []*v1.Record{
		{Id: "a", Vector: []float32{1, 0, 0}, Text: "graph search with hnsw", SparseVector: map[string]float32{"graph": 1}},
		{Id: "b", Vector: []float32{0, 1, 0}, Text: "write ahead log recovery", SparseVector: map[string]float32{"wal": 1}},
	}
	if _, err := client.BatchUpsert(ctx, &v1.BatchUpsertRequest{Collection: "docs", Records: records}); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(ctx, &v1.GetRequest{Collection: "docs", Id: "a"})
	if err != nil || got.GetRecord().GetText() != "graph search with hnsw" || got.GetRecord().GetSparseVector()["graph"] != 1 {
		t.Fatalf("get = %v, %v", got, err)
	}
	for _, request := range []*v1.SearchRequest{
		{Collection: "docs", Text: "recovery log", TopK: 1},
		{Collection: "docs", Text: "recovery log", TopK: 1, Fusion: v1.FusionMode_FUSION_MODE_RRF},
		{Collection: "docs", SparseVector: map[string]float32{"wal": 1}, TopK: 1},
		{Collection: "docs", Vector: []float32{1, 0, 0}, SparseVector: map[string]float32{"wal": 1}, Alpha: proto.Float32(0), TopK: 1},
	} {
		response, err := client.Search(ctx, request)
		if err != nil || len(response.GetResults()) != 1 || response.GetResults()[0].GetId() != "b" {
			t.Fatalf("search %v = %v, %v", request, response, err)
		}
	}
	if _, err := client.Search(ctx, &v1.SearchRequest{Collection: "docs", Vector: []float32{1, 0, 0}, Text: "x", Alpha: proto.Float32(2), TopK: 1}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("alpha out of range code = %v", status.Code(err))
	}
	bad := &v1.Collection{Name: "bad", Dimension: 3, ShardCount: 1, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE,
		Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_HNSW, Quantization: v1.Quantization_QUANTIZATION_BINARY}}
	if _, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: bad}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("binary hnsw code = %v", status.Code(err))
	}
}

func TestServerSideEmbedding(t *testing.T) {
	client, ctx := hybridClient(t)
	collection := &v1.Collection{Name: "articles", Dimension: 3, ShardCount: 1, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, Vectorizer: "keywords"}
	created, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: collection})
	if err != nil || created.GetCollection().GetVectorizer() != "keywords" {
		t.Fatalf("create = %v, %v", created, err)
	}
	if _, err := client.Upsert(ctx, &v1.UpsertRequest{Collection: "articles", Record: &v1.Record{Id: "go", Text: "Go channels"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.BatchUpsert(ctx, &v1.BatchUpsertRequest{Collection: "articles", Records: []*v1.Record{{Id: "rust", Text: "Rust ownership"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := client.Get(ctx, &v1.GetRequest{Collection: "articles", Id: "rust"})
	if err != nil || len(got.GetRecord().GetVector()) != 3 || got.GetRecord().GetVector()[1] != 1 {
		t.Fatalf("embedded record = %v, %v", got, err)
	}
	response, err := client.Search(ctx, &v1.SearchRequest{Collection: "articles", Text: "rust", TopK: 1})
	if err != nil || len(response.GetResults()) != 1 || response.GetResults()[0].GetId() != "rust" {
		t.Fatalf("text search = %v, %v", response, err)
	}
	unknown := &v1.Collection{Name: "unknown", Dimension: 3, ShardCount: 1, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, Vectorizer: "missing"}
	if _, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: unknown}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unknown vectorizer code = %v", status.Code(err))
	}
}
