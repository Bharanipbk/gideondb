// Package grpcapi exposes the versioned public gRPC API.
package grpcapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcmetadata "google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

const maxMessageBytes = 16 << 20
const snapshotChunkBytes = 1 << 20
const maxRestoreArchiveBytes = 64 << 30

type Server struct {
	v1.UnimplementedGideonDBServiceServer
	engine  *engine.Engine
	options Options
}

type PeerProvider interface{ Peers() []cluster.Peer }

type RaftStatus interface {
	Status() (cluster.RaftRole, string, uint64)
}

type AuditRecorder interface {
	RecordGRPCAudit(method string, statusCode int, duration time.Duration)
}

type ClusterSnapshotter interface {
	CreateClusterBackup(context.Context, string, string) error
}

type Options struct {
	APIKey              string
	NodeID              string
	ClusterID           string
	AdvertiseAddress    string
	MetadataEpoch       uint64
	ReplicationFactor   int
	PlacementCapacity   uint32
	EnableStaticRouting bool
	PeerProvider        PeerProvider
	RaftStore           *cluster.RaftStore
	RaftStatus          RaftStatus
	PrincipalResolver   func(string) (*Principal, error)
	RateLimitPerSecond  int
	RateLimitBurst      int
	AuditRecorder       AuditRecorder
	DistributedSearch   http.Handler
	DistributedScroll   http.Handler
	DistributedUpsert   http.Handler
	DistributedDelete   http.Handler
	RestoreDirectory    string
	ClusterSnapshotter  ClusterSnapshotter
	ServerOptions       []grpc.ServerOption
}

type Principal struct {
	Name               string
	Role               string
	CollectionPrefixes []string
}

type principalContextKey struct{}

func NewServer(e *engine.Engine, apiKey string, extra ...grpc.ServerOption) *grpc.Server {
	return NewServerWithOptions(e, Options{APIKey: apiKey, ServerOptions: extra})
}

func NewServerWithOptions(e *engine.Engine, config Options) *grpc.Server {
	limiter := newRequestRateLimiter(config.RateLimitPerSecond, config.RateLimitBurst)
	options := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxMessageBytes),
		grpc.MaxSendMsgSize(maxMessageBytes),
		grpc.ChainUnaryInterceptor(auditUnaryInterceptor(config.AuditRecorder), deadlineUnaryInterceptor, authUnaryInterceptor(config), rateLimitUnaryInterceptor(limiter)),
		grpc.ChainStreamInterceptor(auditStreamInterceptor(config.AuditRecorder), deadlineStreamInterceptor, authStreamInterceptor(config), rateLimitStreamInterceptor(limiter)),
	}
	options = append(options, config.ServerOptions...)
	server := grpc.NewServer(options...)
	v1.RegisterGideonDBServiceServer(server, &Server{engine: e, options: config})
	return server
}

func (s *Server) CreateCollection(_ context.Context, request *v1.CreateCollectionRequest) (*v1.CreateCollectionResponse, error) {
	config, err := fromProtoCollection(request.GetCollection())
	if err != nil {
		return nil, mapError(err)
	}
	if err := s.engine.CreateCollection(config); err != nil {
		return nil, mapError(err)
	}
	created, _, err := s.engine.DescribeCollection(config.Name)
	if err != nil {
		return nil, mapError(err)
	}
	return &v1.CreateCollectionResponse{Collection: toProtoCollection(created)}, nil
}

func (s *Server) DeleteCollection(_ context.Context, request *v1.DeleteCollectionRequest) (*v1.DeleteCollectionResponse, error) {
	if err := s.engine.DeleteCollection(request.GetName()); err != nil {
		return nil, mapError(err)
	}
	return &v1.DeleteCollectionResponse{}, nil
}

func (s *Server) ListCollections(ctx context.Context, _ *v1.ListCollectionsRequest) (*v1.ListCollectionsResponse, error) {
	configs := s.engine.ListCollections()
	result := make([]*v1.Collection, 0, len(configs))
	principal, _ := ctx.Value(principalContextKey{}).(*Principal)
	for _, config := range configs {
		if allowedCollection(principal, config.Name) {
			result = append(result, toProtoCollection(config))
		}
	}
	return &v1.ListCollectionsResponse{Collections: result}, nil
}

func (s *Server) DescribeCollection(_ context.Context, request *v1.DescribeCollectionRequest) (*v1.DescribeCollectionResponse, error) {
	config, count, err := s.engine.DescribeCollection(request.GetName())
	if err != nil {
		return nil, mapError(err)
	}
	return &v1.DescribeCollectionResponse{Collection: toProtoCollection(config), VectorCount: uint64(count)}, nil
}

func (s *Server) Upsert(ctx context.Context, request *v1.UpsertRequest) (*v1.UpsertResponse, error) {
	record, err := fromProtoRecord(request.GetRecord())
	if err != nil {
		return nil, mapError(err)
	}
	if s.options.EnableStaticRouting && s.options.DistributedUpsert != nil {
		response, err := s.distributedBatchUpsert(ctx, request.GetCollection(), []core.Record{record}, request.GetAcknowledgement())
		if err != nil {
			return nil, err
		}
		if response.GetPartial() || len(response.GetRecords()) != 1 {
			return nil, status.Error(codes.Unavailable, "distributed upsert outcome is not conclusively committed")
		}
		return &v1.UpsertResponse{Record: response.GetRecords()[0]}, nil
	}
	stored, err := s.engine.Upsert(request.GetCollection(), record)
	if err != nil {
		return nil, mapError(err)
	}
	converted, err := toProtoRecord(stored)
	if err != nil {
		return nil, mapError(err)
	}
	return &v1.UpsertResponse{Record: converted}, nil
}

func (s *Server) BatchUpsert(ctx context.Context, request *v1.BatchUpsertRequest) (*v1.BatchUpsertResponse, error) {
	if len(request.GetRecords()) < 1 || len(request.GetRecords()) > 10000 {
		return nil, status.Error(codes.InvalidArgument, "records must contain between 1 and 10000 items")
	}
	records := make([]core.Record, len(request.GetRecords()))
	for index, record := range request.GetRecords() {
		converted, err := fromProtoRecord(record)
		if err != nil {
			return nil, mapError(err)
		}
		records[index] = converted
	}
	if s.options.EnableStaticRouting && s.options.DistributedUpsert != nil {
		return s.distributedBatchUpsert(ctx, request.GetCollection(), records, request.GetAcknowledgement())
	}
	stored, err := s.engine.BatchUpsert(request.GetCollection(), records)
	if err != nil {
		return nil, mapError(err)
	}
	result := make([]*v1.Record, len(stored))
	for index, record := range stored {
		converted, err := toProtoRecord(record)
		if err != nil {
			return nil, mapError(err)
		}
		result[index] = converted
	}
	return &v1.BatchUpsertResponse{Records: result}, nil
}

func (s *Server) Delete(ctx context.Context, request *v1.DeleteRequest) (*v1.DeleteResponse, error) {
	if s.options.EnableStaticRouting && s.options.DistributedDelete != nil {
		return s.distributedDelete(ctx, request)
	}
	if err := s.engine.Delete(request.GetCollection(), request.GetNamespace(), request.GetId()); err != nil {
		return nil, mapError(err)
	}
	return &v1.DeleteResponse{}, nil
}

func (s *Server) Get(_ context.Context, request *v1.GetRequest) (*v1.GetResponse, error) {
	record, err := s.engine.Get(request.GetCollection(), request.GetNamespace(), request.GetId())
	if err != nil {
		return nil, mapError(err)
	}
	converted, err := toProtoRecord(record)
	if err != nil {
		return nil, mapError(err)
	}
	return &v1.GetResponse{Record: converted}, nil
}

func (s *Server) Search(ctx context.Context, request *v1.SearchRequest) (*v1.SearchResponse, error) {
	if s.options.EnableStaticRouting && s.options.DistributedSearch != nil {
		return s.distributedSearch(ctx, request)
	}
	filter, err := parseFilter(request.GetFilter())
	if err != nil {
		return nil, mapError(err)
	}
	results, err := s.engine.SearchFiltered(request.GetCollection(), request.GetNamespace(), request.GetVector(), int(request.GetTopK()), filter)
	if err != nil {
		return nil, mapError(err)
	}
	converted, err := toProtoResults(results)
	if err != nil {
		return nil, mapError(err)
	}
	return &v1.SearchResponse{Results: converted}, nil
}

func (s *Server) BatchSearch(ctx context.Context, request *v1.BatchSearchRequest) (*v1.BatchSearchResponse, error) {
	if len(request.GetSearches()) < 1 || len(request.GetSearches()) > 256 {
		return nil, status.Error(codes.InvalidArgument, "searches must contain between 1 and 256 items")
	}
	responses := make([]*v1.SearchResponse, len(request.GetSearches()))
	for index, query := range request.GetSearches() {
		if err := ctx.Err(); err != nil {
			return nil, status.FromContextError(err).Err()
		}
		response, err := s.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		responses[index] = response
	}
	return &v1.BatchSearchResponse{Responses: responses}, nil
}

func (s *Server) Scroll(ctx context.Context, request *v1.ScrollRequest) (*v1.ScrollResponse, error) {
	if request.GetDistributed() {
		return s.distributedScroll(ctx, request)
	}
	limit := int(request.GetLimit())
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 200 {
		return nil, status.Error(codes.InvalidArgument, "limit must be between 1 and 200")
	}
	afterNamespace, afterID, err := decodeCursor(request.GetCursor())
	if err != nil {
		return nil, err
	}
	records, more, engineErr := s.engine.Scroll(request.GetCollection(), request.GetNamespace(), afterNamespace, afterID, limit)
	if engineErr != nil {
		return nil, mapError(engineErr)
	}
	converted := make([]*v1.Record, len(records))
	for index, record := range records {
		if !request.GetIncludeVector() {
			record.Vector = nil
		}
		value, conversionErr := toProtoRecord(record)
		if conversionErr != nil {
			return nil, mapError(conversionErr)
		}
		converted[index] = value
	}
	next := ""
	if more && len(records) != 0 {
		last := records[len(records)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.Namespace + "\x00" + last.ID))
	}
	return &v1.ScrollResponse{Records: converted, NextCursor: next, VectorsIncluded: request.GetIncludeVector()}, nil
}

func (s *Server) ClusterStatus(context.Context, *v1.ClusterStatusRequest) (*v1.ClusterStatusResponse, error) {
	ready, authoritative, err := s.clusterState()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	response := &v1.ClusterStatusResponse{
		ClusterId: s.options.ClusterID, LocalNodeId: s.options.NodeID,
		MetadataEpoch: s.metadataEpoch(), Ready: ready, Authoritative: authoritative,
	}
	if s.options.RaftStatus != nil {
		_, response.LeaderNodeId, response.RaftTerm = s.options.RaftStatus.Status()
	}
	return response, nil
}

func (s *Server) ListNodes(context.Context, *v1.ListNodesRequest) (*v1.ListNodesResponse, error) {
	peers, err := s.membershipPeers()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	localRole := ""
	if s.options.RaftStatus != nil {
		role, _, _ := s.options.RaftStatus.Status()
		localRole = string(role)
	}
	nodes := []*v1.Node{{
		NodeId: s.options.NodeID, AdvertiseAddress: s.options.AdvertiseAddress,
		Health: "local", PlacementCapacity: normalizedCapacity(s.options.PlacementCapacity),
		ProtocolVersion: cluster.ClusterProtocolVersion, RaftRole: localRole,
	}}
	for _, peer := range peers {
		health := string(peer.State)
		if health == "" {
			if peer.Healthy {
				health = string(cluster.PeerHealthy)
			} else {
				health = string(cluster.PeerUnknown)
			}
		}
		nodes = append(nodes, &v1.Node{
			NodeId: peer.NodeID, AdvertiseAddress: peer.AdvertiseAddress, Health: health,
			PlacementCapacity: normalizedCapacity(peer.PlacementCapacity), ProtocolVersion: peer.ProtocolVersion,
		})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetNodeId() < nodes[j].GetNodeId() })
	return &v1.ListNodesResponse{Nodes: nodes, MetadataEpoch: s.metadataEpoch()}, nil
}

func (s *Server) ListShards(ctx context.Context, request *v1.ListShardsRequest) (*v1.ListShardsResponse, error) {
	collections := s.engine.ListCollections()
	principal, _ := ctx.Value(principalContextKey{}).(*Principal)
	if request.GetCollection() != "" {
		found := false
		filtered := collections[:0]
		for _, collection := range collections {
			if collection.Name == request.GetCollection() {
				filtered, found = append(filtered, collection), true
			}
		}
		if !found {
			return nil, status.Error(codes.NotFound, "collection not found")
		}
		collections = filtered
	} else if principal != nil && principal.Role != "admin" {
		filtered := collections[:0]
		for _, collection := range collections {
			if allowedCollection(principal, collection.Name) {
				filtered = append(filtered, collection)
			}
		}
		collections = filtered
	}
	peers, err := s.membershipPeers()
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	factor := s.options.ReplicationFactor
	if factor == 0 {
		factor = 1
	}
	table, err := cluster.PlanReplicaPlacementWithCapacity(s.metadataEpoch(), s.options.NodeID, s.options.AdvertiseAddress, normalizedCapacity(s.options.PlacementCapacity), peers, collections, factor)
	if err != nil {
		return nil, status.Error(codes.Unavailable, err.Error())
	}
	_, authoritative, stateErr := s.clusterState()
	if stateErr != nil {
		return nil, status.Error(codes.Unavailable, stateErr.Error())
	}
	placements := make([]*v1.ShardPlacement, len(table.Shards))
	for index, shard := range table.Shards {
		placements[index] = &v1.ShardPlacement{Collection: shard.Collection, ShardId: shard.ShardID, LeaderNodeId: shard.LeaderID, ReplicaNodeIds: append([]string(nil), shard.Replicas...)}
	}
	return &v1.ListShardsResponse{Shards: placements, MetadataEpoch: table.Epoch, Authoritative: authoritative}, nil
}

func (s *Server) Health(_ context.Context, request *v1.HealthRequest) (*v1.HealthResponse, error) {
	ready, _, err := s.clusterState()
	if err != nil {
		ready = false
	}
	if request.GetRequireReady() && !ready {
		return nil, status.Error(codes.Unavailable, "node is not ready")
	}
	return &v1.HealthResponse{Healthy: true, Ready: ready}, nil
}

func (s *Server) Stats(ctx context.Context, request *v1.StatsRequest) (*v1.StatsResponse, error) {
	configs := s.engine.ListCollections()
	principal, _ := ctx.Value(principalContextKey{}).(*Principal)
	var vectors, shards uint64
	var collections uint64
	for _, config := range configs {
		if request.GetCollection() != "" && config.Name != request.GetCollection() {
			continue
		}
		if !allowedCollection(principal, config.Name) {
			continue
		}
		_, count, err := s.engine.DescribeCollection(config.Name)
		if err != nil {
			return nil, mapError(err)
		}
		vectors += uint64(count)
		shards += uint64(config.ShardCount)
		collections++
	}
	if request.GetCollection() != "" {
		if _, _, err := s.engine.DescribeCollection(request.GetCollection()); err != nil {
			return nil, mapError(err)
		}
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	return &v1.StatsResponse{VectorCount: vectors, CollectionCount: collections, LogicalShardCount: shards, HeapBytes: memory.HeapAlloc}, nil
}

func (s *Server) Snapshot(request *v1.SnapshotRequest, stream grpc.ServerStreamingServer[v1.SnapshotResponse]) error {
	if expected := request.GetExpectedMetadataEpoch(); expected != 0 && expected != s.metadataEpoch() {
		return status.Error(codes.FailedPrecondition, "metadata epoch does not match")
	}
	operationID, err := newOperationID()
	if err != nil {
		return status.Error(codes.Internal, "create snapshot operation")
	}
	temporaryDirectory, err := os.MkdirTemp("", "gideondb-grpc-snapshot-*")
	if err != nil {
		return status.Error(codes.Internal, "create snapshot staging directory")
	}
	defer os.RemoveAll(temporaryDirectory)
	archivePath := filepath.Join(temporaryDirectory, "snapshot.tar.gz")
	if request.GetClusterWide() {
		if s.options.ClusterSnapshotter == nil {
			return status.Error(codes.FailedPrecondition, "cluster snapshot coordinator is not configured")
		}
		if err := s.options.ClusterSnapshotter.CreateClusterBackup(stream.Context(), operationID, archivePath); err != nil {
			return status.Error(codes.Unavailable, "create coordinated cluster snapshot")
		}
	} else if err := s.engine.Backup(archivePath); err != nil {
		return status.Error(codes.Internal, "create consistent snapshot")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return status.Error(codes.Internal, "open staged snapshot")
	}
	defer archive.Close()

	hash := sha256.New()
	buffer := make([]byte, snapshotChunkBytes)
	var offset uint64
	for {
		count, readErr := archive.Read(buffer)
		if count > 0 {
			data := append([]byte(nil), buffer[:count]...)
			if _, err := hash.Write(data); err != nil {
				return status.Error(codes.Internal, "hash staged snapshot")
			}
			if err := stream.Send(&v1.SnapshotResponse{OperationId: operationID, Offset: offset, Data: data}); err != nil {
				return err
			}
			offset += uint64(count)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return status.Error(codes.Internal, "read staged snapshot")
		}
	}
	return stream.Send(&v1.SnapshotResponse{OperationId: operationID, Offset: offset, ArchiveSha256: hash.Sum(nil), Eof: true})
}

// Restore accepts one checksum-addressed node archive and atomically publishes
// it into the configured, nonexistent restore directory. It never mutates the
// running engine's data path; an operator can inspect and promote the restored
// directory through the normal offline workflow.
func (s *Server) Restore(stream grpc.ClientStreamingServer[v1.RestoreRequest, v1.RestoreResponse]) error {
	if s.options.RestoreDirectory == "" {
		return status.Error(codes.FailedPrecondition, "gRPC restore directory is not configured")
	}
	if _, err := os.Lstat(s.options.RestoreDirectory); err == nil {
		return status.Error(codes.AlreadyExists, "gRPC restore directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return status.Error(codes.Internal, "inspect gRPC restore directory")
	}
	temporaryDirectory, err := os.MkdirTemp("", "gideondb-grpc-restore-*")
	if err != nil {
		return status.Error(codes.Internal, "create restore staging directory")
	}
	defer os.RemoveAll(temporaryDirectory)
	archivePath := filepath.Join(temporaryDirectory, "restore.tar.gz")
	archive, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return status.Error(codes.Internal, "create staged restore archive")
	}
	closed := false
	defer func() {
		if !closed {
			_ = archive.Close()
		}
	}()
	hash := sha256.New()
	var operationID string
	var offset uint64
	for {
		chunk, receiveErr := stream.Recv()
		if receiveErr == io.EOF {
			return status.Error(codes.InvalidArgument, "restore stream ended before eof chunk")
		}
		if receiveErr != nil {
			return receiveErr
		}
		if operationID == "" {
			operationID = chunk.GetOperationId()
			if !validOperationID(operationID) {
				return status.Error(codes.InvalidArgument, "restore operation_id must be 32 lowercase hexadecimal characters")
			}
		}
		if chunk.GetOperationId() != operationID || chunk.GetOffset() != offset {
			return status.Error(codes.InvalidArgument, "restore operation or offset mismatch")
		}
		if len(chunk.GetData()) > snapshotChunkBytes || offset+uint64(len(chunk.GetData())) > maxRestoreArchiveBytes {
			return status.Error(codes.ResourceExhausted, "restore archive exceeds chunk or archive limit")
		}
		if len(chunk.GetData()) != 0 {
			if _, err := archive.Write(chunk.GetData()); err != nil {
				return status.Error(codes.Internal, "write staged restore archive")
			}
			_, _ = hash.Write(chunk.GetData())
			offset += uint64(len(chunk.GetData()))
		}
		if !chunk.GetEof() {
			if len(chunk.GetArchiveSha256()) != 0 {
				return status.Error(codes.InvalidArgument, "restore checksum is allowed only on eof chunk")
			}
			continue
		}
		if len(chunk.GetArchiveSha256()) != sha256.Size || subtle.ConstantTimeCompare(chunk.GetArchiveSha256(), hash.Sum(nil)) != 1 {
			return status.Error(codes.InvalidArgument, "restore archive checksum mismatch")
		}
		if err := archive.Sync(); err != nil {
			return status.Error(codes.Internal, "sync staged restore archive")
		}
		if err := archive.Close(); err != nil {
			return status.Error(codes.Internal, "close staged restore archive")
		}
		closed = true
		if err := engine.RestoreBackup(archivePath, s.options.RestoreDirectory); err != nil {
			return status.Error(codes.InvalidArgument, "restore archive validation failed")
		}
		restored, err := engine.Open(s.options.RestoreDirectory)
		if err != nil {
			return status.Error(codes.Internal, "open restored view")
		}
		var vectorCount uint64
		for _, collection := range restored.ListCollections() {
			var afterNamespace, afterID string
			for {
				records, more, scrollErr := restored.Scroll(collection.Name, "", afterNamespace, afterID, 200)
				if scrollErr != nil {
					_ = restored.Close()
					return status.Error(codes.Internal, "inspect restored view")
				}
				vectorCount += uint64(len(records))
				if !more || len(records) == 0 {
					break
				}
				last := records[len(records)-1]
				afterNamespace, afterID = last.Namespace, last.ID
			}
		}
		if err := restored.Close(); err != nil {
			return status.Error(codes.Internal, "close restored view")
		}
		return stream.SendAndClose(&v1.RestoreResponse{OperationId: operationID, MetadataEpoch: s.metadataEpoch(), VectorCount: vectorCount})
	}
}

func validOperationID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16 && value == strings.ToLower(value)
}

func newOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (s *Server) metadataEpoch() uint64 {
	if s.options.RaftStore != nil {
		_, _, epoch := s.options.RaftStore.State()
		if epoch != 0 {
			return epoch
		}
	}
	if s.options.MetadataEpoch != 0 {
		return s.options.MetadataEpoch
	}
	return 1
}

func normalizedCapacity(value uint32) uint32 {
	if value == 0 {
		return 1
	}
	return value
}

func (s *Server) membershipPeers() ([]cluster.Peer, error) {
	var peers []cluster.Peer
	if s.options.PeerProvider != nil {
		peers = append(peers, s.options.PeerProvider.Peers()...)
	}
	if s.options.RaftStore == nil || len(s.options.RaftStore.Voters()) == 0 {
		return peers, nil
	}
	wanted := make(map[string]struct{})
	for _, voter := range s.options.RaftStore.Voters() {
		if voter != s.options.NodeID {
			wanted[voter] = struct{}{}
		}
	}
	filtered := make([]cluster.Peer, 0, len(wanted))
	for _, peer := range peers {
		if _, exists := wanted[peer.NodeID]; exists {
			filtered = append(filtered, peer)
			delete(wanted, peer.NodeID)
		}
	}
	if len(wanted) != 0 {
		return nil, errors.New("one or more configured Raft voters are not discoverable")
	}
	return filtered, nil
}

func (s *Server) clusterState() (ready, authoritative bool, err error) {
	peers, err := s.membershipPeers()
	if err != nil {
		return false, false, err
	}
	factor := s.options.ReplicationFactor
	if factor == 0 {
		factor = 1
	}
	if len(peers) == 0 && factor == 1 {
		return true, true, nil
	}
	if !s.options.EnableStaticRouting || s.options.RaftStore == nil || len(s.options.RaftStore.Voters()) == 0 {
		return false, false, nil
	}
	digests, err := cluster.ComputeViewDigestsWithCapacity(s.metadataEpoch(), s.options.NodeID, s.options.AdvertiseAddress, normalizedCapacity(s.options.PlacementCapacity), peers, s.engine.ListCollections())
	if err != nil {
		return false, false, err
	}
	for _, peer := range peers {
		peerFactor := peer.ReplicationFactor
		if peerFactor == 0 {
			peerFactor = 1
		}
		if peer.State != cluster.PeerHealthy || peer.MetadataEpoch != s.metadataEpoch() || peerFactor != factor || peer.MembershipDigest != digests.Membership || peer.CatalogDigest != digests.Catalog || peer.PlacementDigest != digests.Placement {
			return false, false, nil
		}
	}
	committed, exists := s.options.RaftStore.CommittedView()
	if !exists || committed.Membership != digests.Membership || committed.Catalog != digests.Catalog || committed.Placement != digests.Placement {
		return false, false, nil
	}
	return true, true, nil
}

func fromProtoCollection(value *v1.Collection) (core.CollectionConfig, error) {
	if value == nil {
		return core.CollectionConfig{}, fmtInvalid("collection is required")
	}
	metric := map[v1.DistanceMetric]core.Metric{
		v1.DistanceMetric_DISTANCE_METRIC_COSINE: core.MetricCosine,
		v1.DistanceMetric_DISTANCE_METRIC_DOT:    core.MetricDot,
		v1.DistanceMetric_DISTANCE_METRIC_L2:     core.MetricL2,
	}[value.GetMetric()]
	indexType := core.IndexFlat
	if value.GetIndex().GetType() == v1.IndexType_INDEX_TYPE_HNSW {
		indexType = core.IndexHNSW
	}
	config := core.CollectionConfig{Name: value.GetName(), Dimension: int(value.GetDimension()), Metric: metric, ShardCount: int(value.GetShardCount()), Index: core.IndexConfig{Type: indexType, M: int(value.GetIndex().GetM()), EFConstruction: int(value.GetIndex().GetEfConstruction()), EFSearch: int(value.GetIndex().GetEfSearch())}}
	if err := config.Validate(); err != nil {
		return core.CollectionConfig{}, err
	}
	return config.Normalized(), nil
}

func toProtoCollection(value core.CollectionConfig) *v1.Collection {
	metrics := map[core.Metric]v1.DistanceMetric{core.MetricCosine: v1.DistanceMetric_DISTANCE_METRIC_COSINE, core.MetricDot: v1.DistanceMetric_DISTANCE_METRIC_DOT, core.MetricL2: v1.DistanceMetric_DISTANCE_METRIC_L2}
	indexTypes := map[core.IndexType]v1.IndexType{core.IndexFlat: v1.IndexType_INDEX_TYPE_FLAT, core.IndexHNSW: v1.IndexType_INDEX_TYPE_HNSW}
	value = value.Normalized()
	return &v1.Collection{Name: value.Name, Dimension: uint32(value.Dimension), Metric: metrics[value.Metric], ShardCount: uint32(value.ShardCount), Index: &v1.IndexConfig{Type: indexTypes[value.Index.Type], M: uint32(value.Index.M), EfConstruction: uint32(value.Index.EFConstruction), EfSearch: uint32(value.Index.EFSearch)}}
}

func fromProtoRecord(value *v1.Record) (core.Record, error) {
	if value == nil {
		return core.Record{}, fmtInvalid("record is required")
	}
	return core.Record{ID: value.GetId(), Vector: append([]float32(nil), value.GetVector()...), Metadata: structMap(value.GetMetadata()), Payload: structMap(value.GetPayload()), Timestamp: value.GetTimestampUnixMillis(), Version: value.GetVersion(), Namespace: value.GetNamespace()}, nil
}

func toProtoRecord(value core.Record) (*v1.Record, error) {
	metadataValue, err := structpb.NewStruct(value.Metadata)
	if err != nil {
		return nil, err
	}
	payloadValue, err := structpb.NewStruct(value.Payload)
	if err != nil {
		return nil, err
	}
	return &v1.Record{Id: value.ID, Vector: append([]float32(nil), value.Vector...), Metadata: metadataValue, Payload: payloadValue, TimestampUnixMillis: value.Timestamp, Version: value.Version, Namespace: value.Namespace}, nil
}

func toProtoResults(values []core.SearchResult) ([]*v1.SearchResult, error) {
	result := make([]*v1.SearchResult, len(values))
	for index, value := range values {
		metadataValue, err := structpb.NewStruct(value.Metadata)
		if err != nil {
			return nil, err
		}
		payloadValue, err := structpb.NewStruct(value.Payload)
		if err != nil {
			return nil, err
		}
		result[index] = &v1.SearchResult{Id: value.ID, Score: value.Score, Metadata: metadataValue, Payload: payloadValue, Namespace: value.Namespace}
	}
	return result, nil
}

func parseFilter(value *structpb.Struct) (*metadata.Expr, error) {
	if value == nil {
		return nil, nil
	}
	return metadata.Parse(value.AsMap())
}

func structMap(value *structpb.Struct) map[string]any {
	if value == nil {
		return nil
	}
	return value.AsMap()
}

func decodeCursor(value string) (string, string, error) {
	if value == "" {
		return "", "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	parts := strings.SplitN(string(decoded), "\x00", 2)
	if err != nil || len(decoded) > 4096 || len(parts) != 2 || parts[1] == "" {
		return "", "", status.Error(codes.InvalidArgument, "cursor is invalid")
	}
	return parts[0], parts[1], nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, core.ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, core.ErrAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, core.ErrShardNotOwned), errors.Is(err, core.ErrIdempotencyConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, core.ErrInvalidArgument), errors.Is(err, core.ErrDimensionMismatch):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}

func fmtInvalid(message string) error {
	return errors.Join(core.ErrInvalidArgument, errors.New(message))
}

func deadlineUnaryInterceptor(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if _, exists := ctx.Deadline(); !exists {
		return nil, status.Error(codes.InvalidArgument, "request deadline is required")
	}
	return handler(ctx, request)
}

func deadlineStreamInterceptor(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if _, exists := stream.Context().Deadline(); !exists {
		return status.Error(codes.InvalidArgument, "request deadline is required")
	}
	return handler(server, stream)
}

func authUnaryInterceptor(config Options) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		authenticated, principal, err := authenticate(ctx, config)
		if err != nil {
			return nil, err
		}
		if err := authorize(principal, info.FullMethod, request); err != nil {
			return nil, err
		}
		if principal != nil {
			authenticated = context.WithValue(authenticated, principalContextKey{}, principal)
		}
		return handler(authenticated, request)
	}
}

func authStreamInterceptor(config Options) grpc.StreamServerInterceptor {
	return func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		authenticated, principal, err := authenticate(stream.Context(), config)
		if err != nil {
			return err
		}
		if principal != nil && principal.Role != "admin" {
			return status.Error(codes.PermissionDenied, "principal is not authorized for this operation")
		}
		if principal != nil {
			authenticated = context.WithValue(authenticated, principalContextKey{}, principal)
		}
		return handler(server, &contextServerStream{ServerStream: stream, ctx: authenticated})
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *contextServerStream) Context() context.Context { return s.ctx }

func authenticate(ctx context.Context, config Options) (context.Context, *Principal, error) {
	if config.APIKey == "" && config.PrincipalResolver == nil {
		return ctx, nil, nil
	}
	values := grpcmetadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return ctx, nil, status.Error(codes.Unauthenticated, "valid bearer token required")
	}
	token := strings.TrimPrefix(values[0], "Bearer ")
	if config.APIKey != "" {
		wanted := sha256.Sum256([]byte(config.APIKey))
		received := sha256.Sum256([]byte(token))
		if subtle.ConstantTimeCompare(wanted[:], received[:]) == 1 {
			return ctx, nil, nil
		}
	}
	if config.PrincipalResolver != nil {
		principal, err := config.PrincipalResolver(token)
		if err == nil && principal != nil {
			return ctx, principal, nil
		}
	}
	return ctx, nil, status.Error(codes.Unauthenticated, "valid bearer token required")
}

func authorize(principal *Principal, method string, request any) error {
	if principal == nil || principal.Role == "admin" {
		return nil
	}
	writeAllowed := principal.Role == "writer"
	switch method {
	case v1.GideonDBService_CreateCollection_FullMethodName,
		v1.GideonDBService_DeleteCollection_FullMethodName:
		return status.Error(codes.PermissionDenied, "principal is not authorized for this operation")
	case v1.GideonDBService_Upsert_FullMethodName,
		v1.GideonDBService_BatchUpsert_FullMethodName,
		v1.GideonDBService_Delete_FullMethodName:
		if !writeAllowed {
			return status.Error(codes.PermissionDenied, "principal is not authorized for this operation")
		}
	}
	for _, collection := range requestCollections(request) {
		if !allowedCollection(principal, collection) {
			return status.Error(codes.PermissionDenied, "principal is not authorized for this collection")
		}
	}
	return nil
}

func requestCollections(request any) []string {
	switch value := request.(type) {
	case *v1.DescribeCollectionRequest:
		return []string{value.GetName()}
	case *v1.UpsertRequest:
		return []string{value.GetCollection()}
	case *v1.BatchUpsertRequest:
		return []string{value.GetCollection()}
	case *v1.DeleteRequest:
		return []string{value.GetCollection()}
	case *v1.GetRequest:
		return []string{value.GetCollection()}
	case *v1.SearchRequest:
		return []string{value.GetCollection()}
	case *v1.BatchSearchRequest:
		result := make([]string, 0, len(value.GetSearches()))
		for _, search := range value.GetSearches() {
			result = append(result, search.GetCollection())
		}
		return result
	case *v1.ScrollRequest:
		return []string{value.GetCollection()}
	case *v1.ListShardsRequest:
		return []string{value.GetCollection()}
	case *v1.StatsRequest:
		return []string{value.GetCollection()}
	default:
		return nil
	}
}

func allowedCollection(principal *Principal, collection string) bool {
	if principal == nil || principal.Role == "admin" || collection == "" {
		return true
	}
	for _, prefix := range principal.CollectionPrefixes {
		if strings.HasPrefix(collection, prefix) {
			return true
		}
	}
	return false
}
