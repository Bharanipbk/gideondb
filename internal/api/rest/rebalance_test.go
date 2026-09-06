package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

type recordingVoterChanger struct {
	voters    []string
	digests   cluster.ViewDigests
	calls     int
	viewCalls int
	err       error
}

func (r *recordingVoterChanger) AdvanceView(_ context.Context, digests cluster.ViewDigests) (uint64, error) {
	r.viewCalls++
	r.digests = digests
	return 2, r.err
}

type noopRebalanceMover struct{}

func (noopRebalanceMover) CopySnapshot(context.Context, cluster.ShardMovement, string) error {
	return nil
}
func (noopRebalanceMover) CatchUpWAL(context.Context, cluster.ShardMovement, string) error {
	return nil
}

func (r *recordingVoterChanger) RequestVote(cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error) {
	return cluster.RequestVoteResponse{}, nil
}
func (r *recordingVoterChanger) AppendEntries(cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error) {
	return cluster.AppendEntriesResponse{}, nil
}
func (r *recordingVoterChanger) InstallSnapshot(cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error) {
	return cluster.InstallSnapshotResponse{}, nil
}
func (r *recordingVoterChanger) Status() (cluster.RaftRole, string, uint64) {
	return cluster.RaftLeader, "", 4
}
func (r *recordingVoterChanger) ChangeVoters(_ context.Context, voters []string, digests cluster.ViewDigests) (uint64, error) {
	r.calls++
	r.voters = append([]string(nil), voters...)
	r.digests = digests
	return 2, r.err
}

func TestRebalancePlanEndpointPreviewsJoinWithoutMutation(t *testing.T) {
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 32}); err != nil {
		t.Fatal(err)
	}
	const localID = "11111111111111111111111111111111"
	provider := staticPeerProvider{
		{NodeID: "22222222222222222222222222222222", AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: "33333333333333333333333333333333", AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	handler := NewWithOptions(db, nil, Options{NodeID: localID, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", MetadataEpoch: 7, ReplicationFactor: 2, PeerProvider: provider}).Handler()
	request := httptest.NewRequest(http.MethodGet, "/v1/cluster/rebalance/plan?join_node_id=44444444444444444444444444444444&join_address=node-d%3A6333", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Plan cluster.RebalancePlan `json:"plan"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Plan.Authoritative || body.Plan.FromEpoch != 7 || body.Plan.ToEpoch != 8 || len(body.Plan.Movements) == 0 {
		t.Fatalf("unexpected plan: %#v", body.Plan)
	}
	capacityResponse := httptest.NewRecorder()
	handler.ServeHTTP(capacityResponse, httptest.NewRequest(http.MethodGet, "/v1/cluster/rebalance/plan?capacity_node_id=22222222222222222222222222222222&placement_capacity=3", nil))
	if capacityResponse.Code != http.StatusOK {
		t.Fatalf("capacity plan status=%d body=%s", capacityResponse.Code, capacityResponse.Body.String())
	}
	var capacityBody struct {
		Plan   cluster.RebalancePlan         `json:"plan"`
		Target cluster.ReplicaPlacementTable `json:"target_placement"`
	}
	if err := json.Unmarshal(capacityResponse.Body.Bytes(), &capacityBody); err != nil {
		t.Fatal(err)
	}
	if len(capacityBody.Plan.Movements) == 0 || capacityBody.Target.Nodes[1].Capacity != 3 || len(capacityBody.Target.Nodes) != 3 {
		t.Fatalf("unexpected capacity plan: %#v", capacityBody)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/cluster/rebalance/plan?leave_node_id="+localID, nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_leave_node") {
		t.Fatalf("local leave status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRebalanceApplyBindsDiscoveredLearnerToTargetView(t *testing.T) {
	const apiKey = "0123456789abcdef"
	const localID = "11111111111111111111111111111111"
	const stableID = "22222222222222222222222222222222"
	const learnerID = "33333333333333333333333333333333"
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 16}); err != nil {
		t.Fatal(err)
	}
	store, err := cluster.OpenRaftStore(t.TempDir(), localID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureVoters([]string{localID, stableID}); err != nil {
		t.Fatal(err)
	}
	provider := staticPeerProvider{
		{NodeID: stableID, AdvertiseAddress: "node-b:6333", Healthy: true},
		{NodeID: learnerID, AdvertiseAddress: "node-c:6333", Healthy: true},
	}
	changer := &recordingVoterChanger{}
	executor, err := cluster.OpenRebalanceExecutor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, err := cluster.PlanReplicaPlacement(1, localID, "node-a:6333", []cluster.Peer{provider[0]}, db.ListCollections(), 2)
	if err != nil {
		t.Fatal(err)
	}
	target, err := cluster.PlanReplicaPlacement(2, localID, "node-a:6333", []cluster.Peer(provider), db.ListCollections(), 2)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cluster.PlanRebalance(current, target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Prepare(context.Background(), plan, noopRebalanceMover{}); err != nil {
		t.Fatal(err)
	}
	handler := NewWithOptions(db, nil, Options{APIKey: apiKey, NodeID: localID, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", ReplicationFactor: 2, PeerProvider: provider, RaftStore: store, RaftProtocol: changer, RebalanceExecutor: executor}).Handler()
	payload := rebalanceRequest{ExpectedEpoch: 1, JoinNodeID: learnerID, JoinAddress: "node-c:6333"}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/cluster/rebalance/apply", bytes.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized || changer.calls != 0 {
		t.Fatalf("unauthorized status=%d calls=%d", unauthorized.Code, changer.calls)
	}
	staleRequest := httptest.NewRequest(http.MethodPost, "/v1/cluster/rebalance/apply", bytes.NewReader([]byte(`{"expected_epoch":9,"join_node_id":"`+learnerID+`","join_address":"node-c:6333"}`)))
	staleRequest.Header.Set("Authorization", "Bearer "+apiKey)
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, staleRequest)
	if stale.Code != http.StatusConflict || changer.calls != 0 || !strings.Contains(stale.Body.String(), "epoch_precondition_failed") {
		t.Fatalf("stale status=%d calls=%d body=%s", stale.Code, changer.calls, stale.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/cluster/rebalance/apply", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+apiKey)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || changer.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, changer.calls, response.Body.String())
	}
	wantVoters := []string{localID, stableID, learnerID}
	for index := range wantVoters {
		if changer.voters[index] != wantVoters[index] {
			t.Fatalf("voters=%v", changer.voters)
		}
	}
	wantDigests, err := cluster.ComputeViewDigests(2, localID, "node-a:6333", []cluster.Peer(provider), db.ListCollections())
	if err != nil {
		t.Fatal(err)
	}
	if changer.digests != wantDigests {
		t.Fatalf("digests=%#v want=%#v", changer.digests, wantDigests)
	}
}

func TestCapacityRebalanceApplyUsesSameVoterViewCommit(t *testing.T) {
	const localID = "11111111111111111111111111111111"
	const peerID = "22222222222222222222222222222222"
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateCollection(core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 64}); err != nil {
		t.Fatal(err)
	}
	store, err := cluster.OpenRaftStore(t.TempDir(), localID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureVoters([]string{localID, peerID}); err != nil {
		t.Fatal(err)
	}
	provider := staticPeerProvider{{NodeID: peerID, AdvertiseAddress: "node-b:6333", PlacementCapacity: 1, Healthy: true}}
	_, _, plan, err := cluster.PlanCapacityRebalance(1, localID, "node-a:6333", 1, []cluster.Peer(provider), db.ListCollections(), 1, peerID, 3)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := cluster.OpenRebalanceExecutor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Prepare(context.Background(), plan, noopRebalanceMover{}); err != nil {
		t.Fatal(err)
	}
	protocol := &recordingVoterChanger{}
	handler := NewWithOptions(db, nil, Options{NodeID: localID, ClusterID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AdvertiseAddress: "node-a:6333", ReplicationFactor: 1, PlacementCapacity: 1, PeerProvider: provider, RaftStore: store, RaftProtocol: protocol, RebalanceExecutor: executor}).Handler()
	payload, _ := json.Marshal(rebalanceRequest{ExpectedEpoch: 1, CapacityNodeID: peerID, PlacementCapacity: 3})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/cluster/rebalance/apply", bytes.NewReader(payload)))
	if response.Code != http.StatusOK || protocol.viewCalls != 1 || protocol.calls != 0 {
		t.Fatalf("status=%d view_calls=%d voter_calls=%d body=%s", response.Code, protocol.viewCalls, protocol.calls, response.Body.String())
	}
	capacities, err := cluster.ParseCapacityManifest(protocol.digests.CapacityManifest)
	if err != nil || len(capacities) != 2 || capacities[1].NodeID != peerID || capacities[1].Capacity != 3 {
		t.Fatalf("capacities=%#v err=%v", capacities, err)
	}
}
