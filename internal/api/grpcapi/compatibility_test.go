package grpcapi

import (
	"fmt"
	"testing"

	v1 "github.com/Bharanipbk/gideondb/gen/gideondb/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestV1WireCompatibility is an additive baseline: new declarations may be
// added, but every published field number/type and RPC streaming shape remains
// immutable within gideondb.v1.
func TestV1WireCompatibility(t *testing.T) {
	expectedFields := map[string]string{
		"Collection.name": "1/string", "Collection.dimension": "2/uint32", "Collection.metric": "3/enum", "Collection.shard_count": "4/uint32", "Collection.index": "5/message",
		"IndexConfig.type": "1/enum", "IndexConfig.m": "2/uint32", "IndexConfig.ef_construction": "3/uint32", "IndexConfig.ef_search": "4/uint32",
		"Record.id": "1/string", "Record.vector": "2/float/list", "Record.metadata": "3/message", "Record.payload": "4/message", "Record.timestamp_unix_millis": "5/int64", "Record.version": "6/uint64", "Record.namespace": "7/string",
		"SearchResult.id": "1/string", "SearchResult.score": "2/float", "SearchResult.metadata": "3/message", "SearchResult.payload": "4/message", "SearchResult.namespace": "5/string",
		"CreateCollectionRequest.collection": "1/message", "CreateCollectionResponse.collection": "1/message", "DeleteCollectionRequest.name": "1/string", "ListCollectionsResponse.collections": "1/message/list", "DescribeCollectionRequest.name": "1/string", "DescribeCollectionResponse.collection": "1/message", "DescribeCollectionResponse.vector_count": "2/uint64",
		"UpsertRequest.collection": "1/string", "UpsertRequest.record": "2/message", "UpsertRequest.acknowledgement": "3/enum", "UpsertResponse.record": "1/message",
		"BatchUpsertRequest.collection": "1/string", "BatchUpsertRequest.records": "2/message/list", "BatchUpsertRequest.acknowledgement": "3/enum", "BatchUpsertResponse.records": "1/message/list", "BatchUpsertResponse.outcomes": "2/message/list", "BatchUpsertResponse.partial": "3/bool", "BatchUpsertResponse.metadata_epoch": "4/uint64",
		"DeleteRequest.collection": "1/string", "DeleteRequest.id": "2/string", "DeleteRequest.namespace": "3/string", "DeleteRequest.acknowledgement": "4/enum", "GetRequest.collection": "1/string", "GetRequest.id": "2/string", "GetRequest.namespace": "3/string", "GetResponse.record": "1/message",
		"SearchRequest.collection": "1/string", "SearchRequest.vector": "2/float/list", "SearchRequest.top_k": "3/uint32", "SearchRequest.namespace": "4/string", "SearchRequest.filter": "5/message", "SearchRequest.allow_partial": "6/bool", "SearchResponse.results": "1/message/list", "SearchResponse.failures": "2/message/list", "SearchResponse.partial": "3/bool", "SearchResponse.metadata_epoch": "4/uint64", "SearchResponse.authoritative_placement": "5/bool",
		"BatchSearchRequest.searches": "1/message/list", "BatchSearchResponse.responses": "1/message/list", "ScrollRequest.collection": "1/string", "ScrollRequest.namespace": "2/string", "ScrollRequest.limit": "3/uint32", "ScrollRequest.cursor": "4/string", "ScrollRequest.include_vector": "5/bool", "ScrollRequest.distributed": "6/bool", "ScrollResponse.records": "1/message/list", "ScrollResponse.next_cursor": "2/string", "ScrollResponse.vectors_included": "3/bool", "ScrollResponse.metadata_epoch": "4/uint64", "ScrollResponse.authoritative_placement": "5/bool",
		"ShardFailure.shard_id": "1/uint32", "ShardFailure.node_id": "2/string", "ShardFailure.error": "3/string", "ShardWriteOutcome.shard_id": "1/uint32", "ShardWriteOutcome.node_id": "2/string", "ShardWriteOutcome.status": "3/string", "ShardWriteOutcome.error": "4/string", "ShardWriteOutcome.replicas_acknowledged": "5/uint32", "ShardWriteOutcome.replication_factor": "6/uint32",
		"ClusterStatusResponse.cluster_id": "1/string", "ClusterStatusResponse.local_node_id": "2/string", "ClusterStatusResponse.metadata_epoch": "3/uint64", "ClusterStatusResponse.ready": "4/bool", "ClusterStatusResponse.authoritative": "5/bool", "ClusterStatusResponse.leader_node_id": "6/string", "ClusterStatusResponse.raft_term": "7/uint64",
		"ListNodesResponse.nodes": "1/message/list", "ListNodesResponse.metadata_epoch": "2/uint64", "Node.node_id": "1/string", "Node.advertise_address": "2/string", "Node.health": "3/string", "Node.placement_capacity": "4/uint32", "Node.protocol_version": "5/uint32", "Node.raft_role": "6/string",
		"ListShardsRequest.collection": "1/string", "ListShardsResponse.shards": "1/message/list", "ListShardsResponse.metadata_epoch": "2/uint64", "ListShardsResponse.authoritative": "3/bool", "ShardPlacement.collection": "1/string", "ShardPlacement.shard_id": "2/uint32", "ShardPlacement.leader_node_id": "3/string", "ShardPlacement.replica_node_ids": "4/string/list",
		"HealthRequest.require_ready": "1/bool", "HealthResponse.healthy": "1/bool", "HealthResponse.ready": "2/bool", "StatsRequest.collection": "1/string", "StatsResponse.vector_count": "1/uint64", "StatsResponse.collection_count": "2/uint64", "StatsResponse.logical_shard_count": "3/uint64", "StatsResponse.heap_bytes": "4/uint64", "StatsResponse.mapped_bytes": "5/uint64", "StatsResponse.in_flight_requests": "6/uint64",
		"SnapshotRequest.cluster_wide": "1/bool", "SnapshotRequest.expected_metadata_epoch": "2/uint64", "SnapshotResponse.operation_id": "1/string", "SnapshotResponse.offset": "2/uint64", "SnapshotResponse.data": "3/bytes", "SnapshotResponse.archive_sha256": "4/bytes", "SnapshotResponse.eof": "5/bool", "RestoreRequest.operation_id": "1/string", "RestoreRequest.offset": "2/uint64", "RestoreRequest.data": "3/bytes", "RestoreRequest.archive_sha256": "4/bytes", "RestoreRequest.eof": "5/bool", "RestoreResponse.operation_id": "1/string", "RestoreResponse.metadata_epoch": "2/uint64", "RestoreResponse.vector_count": "3/uint64",
	}
	messages := v1.File_gideondb_v1_gideondb_proto.Messages()
	for key, wanted := range expectedFields {
		messageName, fieldName := splitCompatibilityKey(t, key)
		message := messages.ByName(protoreflect.Name(messageName))
		if message == nil {
			t.Errorf("published message %s was removed", messageName)
			continue
		}
		field := message.Fields().ByName(protoreflect.Name(fieldName))
		if field == nil {
			t.Errorf("published field %s was removed", key)
			continue
		}
		got := fmt.Sprintf("%d/%s", field.Number(), field.Kind())
		if field.Cardinality() == protoreflect.Repeated {
			got += "/list"
		}
		if got != wanted {
			t.Errorf("published field %s changed: got %s, want %s", key, got, wanted)
		}
	}
	expectedEnums := map[string]map[string]protoreflect.EnumNumber{
		"DistanceMetric":  {"DISTANCE_METRIC_UNSPECIFIED": 0, "DISTANCE_METRIC_COSINE": 1, "DISTANCE_METRIC_DOT": 2, "DISTANCE_METRIC_L2": 3},
		"IndexType":       {"INDEX_TYPE_UNSPECIFIED": 0, "INDEX_TYPE_FLAT": 1, "INDEX_TYPE_HNSW": 2},
		"Acknowledgement": {"ACKNOWLEDGEMENT_UNSPECIFIED": 0, "ACKNOWLEDGEMENT_LEADER": 1, "ACKNOWLEDGEMENT_QUORUM": 2, "ACKNOWLEDGEMENT_ALL": 3},
	}
	for enumName, values := range expectedEnums {
		enum := v1.File_gideondb_v1_gideondb_proto.Enums().ByName(protoreflect.Name(enumName))
		if enum == nil {
			t.Errorf("published enum %s was removed", enumName)
			continue
		}
		for valueName, wanted := range values {
			value := enum.Values().ByName(protoreflect.Name(valueName))
			if value == nil || value.Number() != wanted {
				t.Errorf("published enum value %s.%s changed", enumName, valueName)
			}
		}
	}

	expectedMethods := map[string]string{
		"CreateCollection": "CreateCollectionRequest->CreateCollectionResponse", "DeleteCollection": "DeleteCollectionRequest->DeleteCollectionResponse", "ListCollections": "ListCollectionsRequest->ListCollectionsResponse", "DescribeCollection": "DescribeCollectionRequest->DescribeCollectionResponse",
		"Upsert": "UpsertRequest->UpsertResponse", "BatchUpsert": "BatchUpsertRequest->BatchUpsertResponse", "Delete": "DeleteRequest->DeleteResponse", "Get": "GetRequest->GetResponse", "Search": "SearchRequest->SearchResponse", "BatchSearch": "BatchSearchRequest->BatchSearchResponse", "Scroll": "ScrollRequest->ScrollResponse",
		"ClusterStatus": "ClusterStatusRequest->ClusterStatusResponse", "ListNodes": "ListNodesRequest->ListNodesResponse", "ListShards": "ListShardsRequest->ListShardsResponse", "Health": "HealthRequest->HealthResponse", "Stats": "StatsRequest->StatsResponse", "Snapshot": "SnapshotRequest->SnapshotResponse/server-stream", "Restore": "RestoreRequest->RestoreResponse/client-stream",
	}
	service := v1.File_gideondb_v1_gideondb_proto.Services().ByName("GideonDBService")
	for name, wanted := range expectedMethods {
		method := service.Methods().ByName(protoreflect.Name(name))
		if method == nil {
			t.Errorf("published RPC %s was removed", name)
			continue
		}
		got := string(method.Input().Name()) + "->" + string(method.Output().Name())
		if method.IsStreamingClient() {
			got += "/client-stream"
		}
		if method.IsStreamingServer() {
			got += "/server-stream"
		}
		if got != wanted {
			t.Errorf("published RPC %s changed: got %s, want %s", name, got, wanted)
		}
	}
}

func splitCompatibilityKey(t *testing.T, value string) (string, string) {
	t.Helper()
	for index := range value {
		if value[index] == '.' {
			return value[:index], value[index+1:]
		}
	}
	t.Fatalf("invalid compatibility key %q", value)
	return "", ""
}
