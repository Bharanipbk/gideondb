package grpcapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

type recordedAudit struct {
	method string
	status int
}

type auditRecorder struct{ events []recordedAudit }

type clusterSnapshotterFunc func(context.Context, string, string) error

func (f clusterSnapshotterFunc) CreateClusterBackup(ctx context.Context, operation, destination string) error {
	return f(ctx, operation, destination)
}

func (r *auditRecorder) RecordGRPCAudit(method string, statusCode int, _ time.Duration) {
	r.events = append(r.events, recordedAudit{method: method, status: statusCode})
}

func TestServerLifecycleAndGuards(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	listener := bufconn.Listen(1 << 20)
	const nodeID = "11111111111111111111111111111111"
	restorePath := filepath.Join(t.TempDir(), "grpc-restored")
	server := NewServerWithOptions(db, Options{
		APIKey: "test-secret", NodeID: nodeID,
		ClusterID: "22222222222222222222222222222222", AdvertiseAddress: "127.0.0.1:6333",
		MetadataEpoch: 1, ReplicationFactor: 1, PlacementCapacity: 1,
		RestoreDirectory: restorePath,
		ClusterSnapshotter: clusterSnapshotterFunc(func(_ context.Context, operation, destination string) error {
			if len(operation) != 32 {
				t.Errorf("cluster snapshot operation = %q", operation)
			}
			return os.WriteFile(destination, []byte("cluster-archive"), 0o600)
		}),
		PrincipalResolver: func(token string) (*Principal, error) {
			switch token {
			case "reader-token":
				return &Principal{Name: "reader-a", Role: "reader", CollectionPrefixes: []string{"tenant-a."}}, nil
			case "writer-token":
				return &Principal{Name: "writer-a", Role: "writer", CollectionPrefixes: []string{"tenant-a."}}, nil
			default:
				return nil, nil
			}
		},
	})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	connection, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := v1.NewGideonDBServiceClient(connection)

	withoutDeadline := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer test-secret")
	if _, err := client.Health(withoutDeadline, &v1.HealthRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Health without deadline code = %v, want %v", status.Code(err), codes.InvalidArgument)
	}

	deadlineContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := client.Health(deadlineContext, &v1.HealthRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Health without authentication code = %v, want %v", status.Code(err), codes.Unauthenticated)
	}
	ctx := metadata.AppendToOutgoingContext(deadlineContext, "authorization", "Bearer test-secret")
	readerContext := metadata.AppendToOutgoingContext(deadlineContext, "authorization", "Bearer reader-token")
	writerContext := metadata.AppendToOutgoingContext(deadlineContext, "authorization", "Bearer writer-token")

	collection := &v1.Collection{
		Name: "documents", Dimension: 3, ShardCount: 1,
		Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE,
		Index:  &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT},
	}
	created, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: collection})
	if err != nil {
		t.Fatal(err)
	}
	if created.GetCollection().GetName() != "documents" {
		t.Fatalf("created collection = %q", created.GetCollection().GetName())
	}
	if _, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: collection}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("duplicate create code = %v, want %v", status.Code(err), codes.AlreadyExists)
	}
	for _, name := range []string{"tenant-a.docs", "tenant-b.docs"} {
		if _, err := client.CreateCollection(ctx, &v1.CreateCollectionRequest{Collection: &v1.Collection{Name: name, Dimension: 1, ShardCount: 1, Metric: v1.DistanceMetric_DISTANCE_METRIC_COSINE, Index: &v1.IndexConfig{Type: v1.IndexType_INDEX_TYPE_FLAT}}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Upsert(readerContext, &v1.UpsertRequest{Collection: "tenant-a.docs", Record: &v1.Record{Id: "reader-write", Vector: []float32{1}}}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("reader write code = %v, want %v", status.Code(err), codes.PermissionDenied)
	}
	if _, err := client.Upsert(writerContext, &v1.UpsertRequest{Collection: "tenant-a.docs", Record: &v1.Record{Id: "writer-write", Vector: []float32{1}}}); err != nil {
		t.Fatalf("writer upsert: %v", err)
	}
	if _, err := client.Get(readerContext, &v1.GetRequest{Collection: "tenant-b.docs", Id: "missing"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-tenant read code = %v, want %v", status.Code(err), codes.PermissionDenied)
	}
	visible, err := client.ListCollections(readerContext, &v1.ListCollectionsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visible.GetCollections()) != 1 || visible.GetCollections()[0].GetName() != "tenant-a.docs" {
		t.Fatalf("reader-visible collections = %+v", visible.GetCollections())
	}
	visibleShards, err := client.ListShards(readerContext, &v1.ListShardsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(visibleShards.GetShards()) != 1 || visibleShards.GetShards()[0].GetCollection() != "tenant-a.docs" {
		t.Fatalf("reader-visible shards = %+v", visibleShards.GetShards())
	}
	visibleStats, err := client.Stats(readerContext, &v1.StatsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if visibleStats.GetCollectionCount() != 1 || visibleStats.GetVectorCount() != 1 {
		t.Fatalf("reader-visible stats = %+v", visibleStats)
	}
	readerSnapshot, err := client.Snapshot(readerContext, &v1.SnapshotRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readerSnapshot.Recv(); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("reader snapshot code = %v, want %v", status.Code(err), codes.PermissionDenied)
	}

	clusterStatus, err := client.ClusterStatus(ctx, &v1.ClusterStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !clusterStatus.GetReady() || !clusterStatus.GetAuthoritative() || clusterStatus.GetLocalNodeId() != nodeID || clusterStatus.GetMetadataEpoch() != 1 {
		t.Fatalf("unexpected ClusterStatus response: %+v", clusterStatus)
	}
	nodes, err := client.ListNodes(ctx, &v1.ListNodesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes.GetNodes()) != 1 || nodes.GetNodes()[0].GetNodeId() != nodeID || nodes.GetNodes()[0].GetHealth() != "local" {
		t.Fatalf("unexpected ListNodes response: %+v", nodes)
	}
	shards, err := client.ListShards(ctx, &v1.ListShardsRequest{Collection: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	if !shards.GetAuthoritative() || len(shards.GetShards()) != 1 || shards.GetShards()[0].GetLeaderNodeId() != nodeID {
		t.Fatalf("unexpected ListShards response: %+v", shards)
	}
	if _, err := client.ListShards(ctx, &v1.ListShardsRequest{Collection: "missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing collection ListShards code = %v, want %v", status.Code(err), codes.NotFound)
	}

	attributes, err := structpb.NewStruct(map[string]any{"topic": "database"})
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []*v1.Record{
		{Id: "a", Namespace: "docs", Vector: []float32{1, 0, 0}, Metadata: attributes},
		{Id: "b", Namespace: "docs", Vector: []float32{0.9, 0.1, 0}},
	} {
		if _, err := client.Upsert(ctx, &v1.UpsertRequest{Collection: "documents", Record: record}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := client.Get(ctx, &v1.GetRequest{Collection: "documents", Namespace: "docs", Id: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRecord().GetId() != "a" || len(got.GetRecord().GetVector()) != 3 {
		t.Fatalf("unexpected Get response: %+v", got.GetRecord())
	}

	search, err := client.Search(ctx, &v1.SearchRequest{Collection: "documents", Namespace: "docs", Vector: []float32{1, 0, 0}, TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(search.GetResults()) != 2 || search.GetResults()[0].GetId() != "a" {
		t.Fatalf("unexpected Search response: %+v", search.GetResults())
	}

	firstPage, err := client.Scroll(ctx, &v1.ScrollRequest{Collection: "documents", Namespace: "docs", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage.GetRecords()) != 1 || firstPage.GetNextCursor() == "" || len(firstPage.GetRecords()[0].GetVector()) != 0 {
		t.Fatalf("unexpected first Scroll page: %+v", firstPage)
	}
	secondPage, err := client.Scroll(ctx, &v1.ScrollRequest{Collection: "documents", Namespace: "docs", Limit: 1, Cursor: firstPage.GetNextCursor(), IncludeVector: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage.GetRecords()) != 1 || len(secondPage.GetRecords()[0].GetVector()) != 3 {
		t.Fatalf("unexpected second Scroll page: %+v", secondPage)
	}

	if _, err := client.Delete(ctx, &v1.DeleteRequest{Collection: "documents", Namespace: "docs", Id: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Get(ctx, &v1.GetRequest{Collection: "documents", Namespace: "docs", Id: "a"}); status.Code(err) != codes.NotFound {
		t.Fatalf("deleted Get code = %v, want %v", status.Code(err), codes.NotFound)
	}

	staleSnapshot, err := client.Snapshot(ctx, &v1.SnapshotRequest{ExpectedMetadataEpoch: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := staleSnapshot.Recv(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale Snapshot code = %v, want %v", status.Code(err), codes.FailedPrecondition)
	}
	snapshot, err := client.Snapshot(ctx, &v1.SnapshotRequest{ExpectedMetadataEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "snapshot.tar.gz")
	archive, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	var operationID string
	var offset uint64
	for {
		chunk, receiveErr := snapshot.Recv()
		if receiveErr != nil {
			t.Fatalf("receive snapshot: %v", receiveErr)
		}
		if operationID == "" {
			operationID = chunk.GetOperationId()
		}
		if chunk.GetOperationId() != operationID || chunk.GetOffset() != offset || len(chunk.GetData()) > snapshotChunkBytes {
			t.Fatalf("invalid snapshot chunk: %+v offset=%d", chunk, offset)
		}
		if len(chunk.GetData()) > 0 {
			if _, err := archive.Write(chunk.GetData()); err != nil {
				t.Fatal(err)
			}
			_, _ = hash.Write(chunk.GetData())
			offset += uint64(len(chunk.GetData()))
		}
		if chunk.GetEof() {
			if string(chunk.GetArchiveSha256()) != string(hash.Sum(nil)) {
				t.Fatal("snapshot archive checksum mismatch")
			}
			break
		}
	}
	if _, err := snapshot.Recv(); err != io.EOF {
		t.Fatalf("snapshot terminal receive = %v, want EOF", err)
	}
	clusterSnapshot, err := client.Snapshot(ctx, &v1.SnapshotRequest{ClusterWide: true, ExpectedMetadataEpoch: 1})
	if err != nil {
		t.Fatal(err)
	}
	clusterData, err := clusterSnapshot.Recv()
	if err != nil || string(clusterData.GetData()) != "cluster-archive" || clusterData.GetOffset() != 0 {
		t.Fatalf("cluster snapshot data=%+v err=%v", clusterData, err)
	}
	clusterEOF, err := clusterSnapshot.Recv()
	expectedClusterHash := sha256.Sum256([]byte("cluster-archive"))
	if err != nil || !clusterEOF.GetEof() || clusterEOF.GetOffset() != uint64(len("cluster-archive")) || string(clusterEOF.GetArchiveSha256()) != string(expectedClusterHash[:]) {
		t.Fatalf("cluster snapshot eof=%+v err=%v", clusterEOF, err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	archiveBytes, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	restore, err := client.Restore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restoreHash := sha256.Sum256(archiveBytes)
	for start := 0; start < len(archiveBytes); start += snapshotChunkBytes {
		end := min(start+snapshotChunkBytes, len(archiveBytes))
		if err := restore.Send(&v1.RestoreRequest{OperationId: operationID, Offset: uint64(start), Data: archiveBytes[start:end]}); err != nil {
			t.Fatal(err)
		}
	}
	restoredResponse, err := restore.CloseAndRecv()
	if err == nil {
		t.Fatal("restore without eof unexpectedly succeeded")
	}
	// A failed stream cannot be resumed; send the complete archive again with
	// the checksum on a distinct final marker.
	restore, err = client.Restore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < len(archiveBytes); start += snapshotChunkBytes {
		end := min(start+snapshotChunkBytes, len(archiveBytes))
		if err := restore.Send(&v1.RestoreRequest{OperationId: operationID, Offset: uint64(start), Data: archiveBytes[start:end]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := restore.Send(&v1.RestoreRequest{OperationId: operationID, Offset: uint64(len(archiveBytes)), ArchiveSha256: restoreHash[:], Eof: true}); err != nil {
		t.Fatal(err)
	}
	restoredResponse, err = restore.CloseAndRecv()
	if err != nil {
		t.Fatalf("restore streamed snapshot: %v", err)
	}
	if restoredResponse.GetOperationId() != operationID || restoredResponse.GetVectorCount() != 2 || restoredResponse.GetMetadataEpoch() != 1 {
		t.Fatalf("unexpected Restore response: %+v", restoredResponse)
	}
	restored, err := engine.Open(restorePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := restored.Get("documents", "docs", "b"); err != nil {
		t.Fatalf("streamed snapshot omitted record: %v", err)
	}
}

func TestServerRateLimitReturnsRetryMetadata(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	listener := bufconn.Listen(1 << 20)
	audit := &auditRecorder{}
	server := NewServerWithOptions(db, Options{APIKey: "test-secret", RateLimitPerSecond: 1, RateLimitBurst: 1, AuditRecorder: audit})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	connection, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := v1.NewGideonDBServiceClient(connection)
	deadlineContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx := metadata.AppendToOutgoingContext(deadlineContext, "authorization", "Bearer test-secret")
	if _, err := client.Health(ctx, &v1.HealthRequest{}); err != nil {
		t.Fatal(err)
	}
	var headers metadata.MD
	if _, err := client.Health(ctx, &v1.HealthRequest{}, grpc.Header(&headers)); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("rate-limited Health code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
	if len(headers.Get("retry-after-ms")) != 1 {
		t.Fatalf("retry metadata = %#v", headers)
	}
	if len(audit.events) != 2 || audit.events[0].method != v1.GideonDBService_Health_FullMethodName || audit.events[0].status != 200 || audit.events[1].status != 429 {
		t.Fatalf("audit events = %+v", audit.events)
	}
}

func TestDistributedScrollBridge(t *testing.T) {
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.PathValue("name") != "tenant-a.docs" || request.URL.Query().Get("namespace") != "docs" || request.URL.Query().Get("cursor") != "opaque" || request.URL.Query().Get("include_vector") != "true" {
			t.Errorf("unexpected translated request: path=%q query=%v", request.PathValue("name"), request.URL.Query())
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"records":[{"id":"one","namespace":"docs","vector":[1]}],"next_cursor":"next","vectors_included":true,"metadata_epoch":7,"authoritative_placement":true}`))
	})
	server := &Server{options: Options{DistributedScroll: handler}}
	result, err := server.distributedScroll(context.Background(), &v1.ScrollRequest{Collection: "tenant-a.docs", Namespace: "docs", Limit: 1, Cursor: "opaque", IncludeVector: true, Distributed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GetRecords()) != 1 || result.GetRecords()[0].GetId() != "one" || result.GetNextCursor() != "next" || !result.GetVectorsIncluded() || result.GetMetadataEpoch() != 7 || !result.GetAuthoritativePlacement() {
		t.Fatalf("distributed Scroll response = %+v", result)
	}
	server.options.DistributedScroll = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusConflict) })
	if _, err := server.distributedScroll(context.Background(), &v1.ScrollRequest{Collection: "tenant-a.docs", Limit: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale distributed Scroll code = %v, want %v", status.Code(err), codes.FailedPrecondition)
	}
}

func TestDistributedSearchBridge(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.PathValue("name") != "tenant-a.docs" || request.Method != http.MethodPost {
			t.Errorf("unexpected translated request: method=%s collection=%q", request.Method, request.PathValue("name"))
		}
		var body struct {
			Vector       []float32      `json:"vector"`
			TopK         uint32         `json:"top_k"`
			Namespace    string         `json:"namespace"`
			Filter       map[string]any `json:"filter"`
			AllowPartial bool           `json:"allow_partial"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.TopK != 2 || body.Namespace != "docs" || !body.AllowPartial || body.Filter["topic"] != "database" {
			t.Errorf("translated search body = %+v", body)
		}
		_, _ = response.Write([]byte(`{"results":[{"id":"one","score":0.9,"namespace":"docs"}],"failures":[{"shard_id":2,"node_id":"node-b","error":"unavailable"}],"partial":true,"metadata_epoch":7,"authoritative_placement":true}`))
	})
	filter, err := structpb.NewStruct(map[string]any{"topic": "database"})
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{options: Options{EnableStaticRouting: true, DistributedSearch: handler}}
	request := &v1.SearchRequest{Collection: "tenant-a.docs", Namespace: "docs", Vector: []float32{1, 0}, TopK: 2, Filter: filter, AllowPartial: true}
	result, err := server.Search(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GetResults()) != 1 || result.GetResults()[0].GetId() != "one" || len(result.GetFailures()) != 1 || !result.GetPartial() || result.GetMetadataEpoch() != 7 || !result.GetAuthoritativePlacement() {
		t.Fatalf("distributed Search response = %+v", result)
	}
	batch, err := server.BatchSearch(context.Background(), &v1.BatchSearchRequest{Searches: []*v1.SearchRequest{request}})
	if err != nil || len(batch.GetResponses()) != 1 || calls != 2 {
		t.Fatalf("distributed BatchSearch response=%+v calls=%d err=%v", batch, calls, err)
	}
	server.options.DistributedSearch = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusServiceUnavailable)
	})
	if _, err := server.Search(context.Background(), request); status.Code(err) != codes.Unavailable {
		t.Fatalf("failed distributed Search code = %v, want %v", status.Code(err), codes.Unavailable)
	}
}

func TestDistributedUpsertBridge(t *testing.T) {
	calls := 0
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		if request.PathValue("name") != "tenant-a.docs" {
			t.Errorf("translated collection = %q", request.PathValue("name"))
		}
		var body struct {
			Records         []core.Record `json:"records"`
			Acknowledgement string        `json:"acknowledgement"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Acknowledgement != "all" || len(body.Records) == 0 {
			t.Errorf("translated upsert body = %+v", body)
		}
		for index := range body.Records {
			body.Records[index].Version = uint64(index + 1)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"outcomes": []any{map[string]any{"shard_id": 2, "node_id": "node-b", "status": "committed", "records": body.Records, "replicas_acknowledged": 3, "replication_factor": 3}}, "partial": false, "metadata_epoch": 7, "authoritative_placement": true})
	})
	server := &Server{options: Options{EnableStaticRouting: true, DistributedUpsert: handler}}
	request := &v1.BatchUpsertRequest{Collection: "tenant-a.docs", Acknowledgement: v1.Acknowledgement_ACKNOWLEDGEMENT_ALL, Records: []*v1.Record{{Id: "one", Vector: []float32{1}}}}
	result, err := server.BatchUpsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.GetRecords()) != 1 || result.GetRecords()[0].GetVersion() != 1 || len(result.GetOutcomes()) != 1 || result.GetOutcomes()[0].GetReplicasAcknowledged() != 3 || result.GetMetadataEpoch() != 7 || result.GetPartial() {
		t.Fatalf("distributed BatchUpsert response = %+v", result)
	}
	upsert, err := server.Upsert(context.Background(), &v1.UpsertRequest{Collection: "tenant-a.docs", Acknowledgement: v1.Acknowledgement_ACKNOWLEDGEMENT_ALL, Record: &v1.Record{Id: "two", Vector: []float32{1}}})
	if err != nil || upsert.GetRecord().GetId() != "two" || calls != 2 {
		t.Fatalf("distributed Upsert response=%+v calls=%d err=%v", upsert, calls, err)
	}
	server.options.DistributedUpsert = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusMultiStatus)
		_, _ = response.Write([]byte(`{"outcomes":[{"shard_id":2,"node_id":"node-b","status":"unknown","error":"timeout"}],"partial":true,"metadata_epoch":7,"authoritative_placement":true}`))
	})
	partial, err := server.BatchUpsert(context.Background(), request)
	if err != nil || !partial.GetPartial() || partial.GetOutcomes()[0].GetStatus() != "unknown" {
		t.Fatalf("partial BatchUpsert response=%+v err=%v", partial, err)
	}
	if _, err := server.Upsert(context.Background(), &v1.UpsertRequest{Collection: "tenant-a.docs", Acknowledgement: v1.Acknowledgement_ACKNOWLEDGEMENT_ALL, Record: &v1.Record{Id: "two", Vector: []float32{1}}}); status.Code(err) != codes.Unavailable {
		t.Fatalf("ambiguous Upsert code = %v, want %v", status.Code(err), codes.Unavailable)
	}
}

func TestDistributedDeleteBridge(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		called = true
		if request.Method != http.MethodDelete || request.PathValue("name") != "tenant-a.docs" || request.PathValue("id") != "record/one" || request.URL.Query().Get("namespace") != "space" {
			t.Errorf("translated delete request = %s %s values=%q/%q query=%q", request.Method, request.URL.Path, request.PathValue("name"), request.PathValue("id"), request.URL.RawQuery)
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"status": "committed"})
	})
	server := &Server{options: Options{EnableStaticRouting: true, DistributedDelete: handler}}
	if _, err := server.Delete(context.Background(), &v1.DeleteRequest{Collection: "tenant-a.docs", Namespace: "space", Id: "record/one"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("distributed delete handler was not called")
	}
	server.options.DistributedDelete = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) { response.WriteHeader(http.StatusConflict) })
	if _, err := server.Delete(context.Background(), &v1.DeleteRequest{Collection: "tenant-a.docs", Id: "one"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("failed distributed Delete code = %v, want %v", status.Code(err), codes.FailedPrecondition)
	}
}
