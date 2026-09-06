package rest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

type fixedRaftStatus struct{ term uint64 }

func (f fixedRaftStatus) Status() (cluster.RaftRole, string, uint64) {
	return cluster.RaftFollower, "", f.term
}
func (f fixedRaftStatus) RequestVote(cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error) {
	return cluster.RequestVoteResponse{}, nil
}
func (f fixedRaftStatus) AppendEntries(cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error) {
	return cluster.AppendEntriesResponse{}, nil
}
func (f fixedRaftStatus) InstallSnapshot(cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error) {
	return cluster.InstallSnapshotResponse{}, nil
}

func TestFollowerReplicaAppendTransportFencesAndAppliesOrderedBatch(t *testing.T) {
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const localID = "11111111111111111111111111111111"
	peers := staticPeerProvider{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	config := core.CollectionConfig{Name: "vectors", Dimension: 2, Metric: core.MetricDot, ShardCount: 16}
	table, err := cluster.PlanReplicaPlacement(1, localID, "node-a:6333", []cluster.Peer(peers), []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var shardID uint32
	var leaderID string
	found := false
	for _, shard := range table.Shards {
		if shard.LeaderID == localID {
			continue
		}
		for _, replica := range shard.Replicas {
			if replica == localID {
				shardID, leaderID, found = shard.ShardID, shard.LeaderID, true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		t.Fatal("no follower shard assigned to local node")
	}
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	id := ""
	for candidate := 0; candidate < 10_000; candidate++ {
		value := fmt.Sprintf("record-%d", candidate)
		routed, routeErr := db.RouteShard(config.Name, "", value)
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		if routed == shardID {
			id = value
			break
		}
	}
	if id == "" {
		t.Fatal("no record ID routed to follower shard")
	}
	handler := NewWithOptions(db, nil, Options{NodeID: localID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, ReplicationFactor: 2, PeerProvider: peers, RaftProtocol: fixedRaftStatus{term: 7}}).Handler()
	payload, err := json.Marshal(replicaAppendRequest{Sequence: 1, ReplicationFactor: 2, Records: []core.Record{{ID: id, Vector: []float32{1, 2}, Version: 9, Timestamp: 123}}})
	if err != nil {
		t.Fatal(err)
	}
	call := func(term string, body []byte) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/v1/internal/replicas/%s/%d/append", config.Name, shardID), bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-GideonDB-Cluster-ID", clusterID)
		request.Header.Set("X-GideonDB-Target-Node-ID", localID)
		request.Header.Set("X-GideonDB-Metadata-Epoch", "1")
		request.Header.Set("X-GideonDB-Leader-Node-ID", leaderID)
		request.Header.Set("X-GideonDB-Leader-Term", term)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call("6", payload); response.Code != http.StatusConflict {
		t.Fatalf("stale leader term status=%d body=%s", response.Code, response.Body.String())
	}
	mismatched, err := json.Marshal(replicaAppendRequest{Sequence: 1, ReplicationFactor: 1, Records: []core.Record{{ID: id, Vector: []float32{1, 2}, Version: 9, Timestamp: 123}}})
	if err != nil {
		t.Fatal(err)
	}
	if response := call("7", mismatched); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "replication_factor_mismatch") {
		t.Fatalf("factor mismatch status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call("7", payload); response.Code != http.StatusOK {
		t.Fatalf("append status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call("7", payload); response.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	record, err := db.Get(config.Name, "", id)
	if err != nil || record.Version != 9 || record.Timestamp != 123 {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}
