package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/engine"
)

type restMemoryRaftTransport struct {
	mu    sync.RWMutex
	nodes map[string]*cluster.RaftRuntime
}

func (m *restMemoryRaftTransport) RequestVote(_ context.Context, peer cluster.RaftPeer, request cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error) {
	m.mu.RLock()
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if node == nil {
		return cluster.RequestVoteResponse{}, errors.New("peer unavailable")
	}
	return node.RequestVote(request)
}
func (m *restMemoryRaftTransport) AppendEntries(_ context.Context, peer cluster.RaftPeer, request cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error) {
	m.mu.RLock()
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if node == nil {
		return cluster.AppendEntriesResponse{}, errors.New("peer unavailable")
	}
	return node.AppendEntries(request)
}
func (m *restMemoryRaftTransport) InstallSnapshot(_ context.Context, peer cluster.RaftPeer, request cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error) {
	m.mu.RLock()
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if node == nil {
		return cluster.InstallSnapshotResponse{}, errors.New("peer unavailable")
	}
	return node.InstallSnapshot(request)
}

type followerRaftStatus struct {
	leader string
	term   uint64
}

func (f followerRaftStatus) Status() (cluster.RaftRole, string, uint64) {
	return cluster.RaftFollower, f.leader, f.term
}
func (f followerRaftStatus) RequestVote(cluster.RequestVoteRequest) (cluster.RequestVoteResponse, error) {
	return cluster.RequestVoteResponse{}, nil
}
func (f followerRaftStatus) AppendEntries(cluster.AppendEntriesRequest) (cluster.AppendEntriesResponse, error) {
	return cluster.AppendEntriesResponse{}, nil
}
func (f followerRaftStatus) InstallSnapshot(cluster.InstallSnapshotRequest) (cluster.InstallSnapshotResponse, error) {
	return cluster.InstallSnapshotResponse{}, nil
}

func TestRebalancePreparationResumesAcrossCoordinatorRestartAndAborts(t *testing.T) {
	const apiKey = "0123456789abcdef"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	hosts := []string{"node-a:6333", "node-b:6333", "node-c:6333"}
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 48}
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	dbs := make([]*engine.Engine, 3)
	for i := range dbs {
		var err error
		dbs[i], err = engine.Open(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		defer dbs[i].Close()
		if err := dbs[i].CreateCollection(config); err != nil {
			t.Fatal(err)
		}
	}
	stores := make([]*cluster.RaftStore, 2)
	barriers := make([]*cluster.RebalanceBarrierStore, 2)
	for i := range stores {
		var err error
		stores[i], err = cluster.OpenRaftStore(paths[i], ids[i], 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := stores[i].ConfigureVoters(ids[:2]); err != nil {
			t.Fatal(err)
		}
		barriers[i], err = cluster.OpenRebalanceBarrierStore(paths[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	peers := []staticPeerProvider{
		{{SeedURL: "http://" + hosts[1], NodeID: ids[1], AdvertiseAddress: hosts[1], Healthy: true}, {SeedURL: "http://" + hosts[2], NodeID: ids[2], AdvertiseAddress: hosts[2], Healthy: true}},
		{{SeedURL: "http://" + hosts[0], NodeID: ids[0], AdvertiseAddress: hosts[0], Healthy: true}, {SeedURL: "http://" + hosts[2], NodeID: ids[2], AdvertiseAddress: hosts[2], Healthy: true}},
		{{SeedURL: "http://" + hosts[0], NodeID: ids[0], AdvertiseAddress: hosts[0], Healthy: true}, {SeedURL: "http://" + hosts[1], NodeID: ids[1], AdvertiseAddress: hosts[1], Healthy: true}},
	}
	current, err := cluster.PlanReplicaPlacement(1, ids[0], hosts[0], []cluster.Peer{peers[0][0]}, []core.CollectionConfig{config}, 1)
	if err != nil {
		t.Fatal(err)
	}
	target, err := cluster.PlanReplicaPlacement(2, ids[0], hosts[0], []cluster.Peer(peers[0]), []core.CollectionConfig{config}, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cluster.PlanRebalance(current, target)
	if err != nil {
		t.Fatal(err)
	}
	var selected cluster.ShardMovement
	for _, movement := range plan.Movements {
		if movement.SourceLeaderID == ids[1] && containsTestString(movement.TargetReplicas, ids[2]) {
			selected = movement
			break
		}
	}
	if selected.Collection == "" {
		t.Fatal("test plan has no remote-source learner movement")
	}
	record := recordForTestShard(t, dbs[1], config, selected.ShardID)
	if _, _, err := dbs[1].BatchUpsertShardWithSequence(config.Name, selected.ShardID, []core.Record{record}); err != nil {
		t.Fatal(err)
	}
	handlers := map[string]http.Handler{}
	var snapshotCalls atomic.Int64
	var failOnce atomic.Bool
	failOnce.Store(true)
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.Contains(request.URL.Path, "/internal/rebalance/") && strings.HasSuffix(request.URL.Path, "/snapshot") && snapshotCalls.Add(1) == 2 && failOnce.Swap(false) {
			return nil, errors.New("injected learner outage")
		}
		handler := handlers[request.URL.Host]
		if handler == nil {
			return nil, fmt.Errorf("unknown host %s", request.URL.Host)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	executor, err := cluster.OpenRebalanceExecutor(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	changer := &recordingVoterChanger{}
	buildCoordinator := func(executor *cluster.RebalanceExecutor) http.Handler {
		return NewWithOptions(dbs[0], nil, Options{APIKey: apiKey, NodeID: ids[0], ClusterID: clusterID, AdvertiseAddress: hosts[0], ReplicationFactor: 1, PeerProvider: peers[0], InternalHTTPClient: client, RaftStore: stores[0], RaftProtocol: changer, RebalanceBarriers: barriers[0], RebalanceExecutor: executor}).Handler()
	}
	handlers[hosts[0]] = buildCoordinator(executor)
	handlers[hosts[1]] = NewWithOptions(dbs[1], nil, Options{APIKey: apiKey, NodeID: ids[1], ClusterID: clusterID, AdvertiseAddress: hosts[1], ReplicationFactor: 1, PeerProvider: peers[1], InternalHTTPClient: client, RaftStore: stores[1], RaftProtocol: followerRaftStatus{leader: ids[0], term: 4}, RebalanceBarriers: barriers[1]}).Handler()
	handlers[hosts[2]] = NewWithOptions(dbs[2], nil, Options{APIKey: apiKey, NodeID: ids[2], ClusterID: clusterID, AdvertiseAddress: hosts[2], ReplicationFactor: 1, PeerProvider: peers[2], InternalHTTPClient: client, RaftProtocol: followerRaftStatus{leader: ids[0], term: 4}}).Handler()
	payload, _ := json.Marshal(rebalanceRequest{ExpectedEpoch: 1, JoinNodeID: ids[2], JoinAddress: hosts[2]})
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handlers[hosts[0]].ServeHTTP(response, req)
		return response
	}
	if response := call("/v1/cluster/rebalance/prepare"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("injected failure status=%d body=%s", response.Code, response.Body.String())
	}
	restarted, err := cluster.OpenRebalanceExecutor(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	handlers[hosts[0]] = buildCoordinator(restarted)
	if response := call("/v1/cluster/rebalance/prepare"); response.Code != http.StatusOK {
		t.Fatalf("resume status=%d body=%s", response.Code, response.Body.String())
	}
	_, records, err := dbs[2].ExportReplicaSnapshot(config.Name, selected.ShardID)
	if err != nil || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("staged records=%v err=%v", records, err)
	}
	if _, frozen := barriers[1].IsFrozen(config.Name, selected.ShardID); !frozen {
		t.Fatal("remote source was not frozen after final catch-up")
	}
	if response := call("/v1/cluster/rebalance/abort"); response.Code != http.StatusOK {
		t.Fatalf("abort status=%d body=%s", response.Code, response.Body.String())
	}
	if _, frozen := barriers[1].IsFrozen(config.Name, selected.ShardID); frozen {
		t.Fatal("abort did not release remote source barrier")
	}
	if _, exists := restarted.Status(); exists {
		t.Fatal("abort did not clear coordinator journal")
	}
}

func TestPreparedJoinAppliesThroughRealJointConsensus(t *testing.T) {
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const apiKey = "0123456789abcdef"
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	hosts := []string{"node-a:6333", "node-b:6333", "node-c:6333"}
	transport := &restMemoryRaftTransport{nodes: map[string]*cluster.RaftRuntime{}}
	stores := make([]*cluster.RaftStore, 3)
	runtimes := make([]*cluster.RaftRuntime, 3)
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	for i := range ids {
		var err error
		stores[i], err = cluster.OpenRaftStore(paths[i], ids[i], 1)
		if err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if err := stores[i].ConfigureVoters(ids[:2]); err != nil {
				t.Fatal(err)
			}
		}
		local := ids[i]
		runtimes[i], err = cluster.NewRaftRuntime(stores[i], local, func() []cluster.RaftPeer {
			result := []cluster.RaftPeer{}
			for index, id := range ids {
				if id != local {
					result = append(result, cluster.RaftPeer{NodeID: id, BaseURL: "memory://" + hosts[index]})
				}
			}
			return result
		}, transport, cluster.RaftRuntimeConfig{ElectionMin: 40 * time.Millisecond, ElectionMax: 80 * time.Millisecond, Heartbeat: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		transport.nodes[ids[i]] = runtimes[i]
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() { runtimes[0].Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done })
	leader := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if role, _, _ := runtimes[leader].Status(); role == cluster.RaftLeader {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if role, _, _ := runtimes[leader].Status(); role != cluster.RaftLeader {
		t.Fatal("old voter set did not elect a leader")
	}
	db, err := engine.Open(paths[leader])
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	executor, err := cluster.OpenRebalanceExecutor(paths[leader])
	if err != nil {
		t.Fatal(err)
	}
	barriers, err := cluster.OpenRebalanceBarrierStore(paths[leader])
	if err != nil {
		t.Fatal(err)
	}
	peers := staticPeerProvider{}
	for i := range ids {
		if i != leader {
			peers = append(peers, cluster.Peer{SeedURL: "http://" + hosts[i], NodeID: ids[i], AdvertiseAddress: hosts[i], Healthy: true})
		}
	}
	handler := NewWithOptions(db, nil, Options{APIKey: apiKey, NodeID: ids[leader], ClusterID: clusterID, AdvertiseAddress: hosts[leader], ReplicationFactor: 1, PeerProvider: peers, RaftStore: stores[leader], RaftProtocol: runtimes[leader], RebalanceExecutor: executor, RebalanceBarriers: barriers}).Handler()
	payload, _ := json.Marshal(rebalanceRequest{ExpectedEpoch: 1, JoinNodeID: ids[2], JoinAddress: hosts[2]})
	call := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call("/v1/cluster/rebalance/prepare"); response.Code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
	}
	if response := call("/v1/cluster/rebalance/apply"); response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	for i, store := range stores {
		_, _, epoch := store.State()
		voters := store.Voters()
		if epoch != 2 || len(voters) != 3 {
			t.Fatalf("node %d epoch=%d voters=%v", i, epoch, voters)
		}
	}
	if _, exists := executor.Status(); exists {
		t.Fatal("committed preparation journal was not cleared")
	}
}

func TestPreparedLeaveAppliesAndFencesRemovedVoter(t *testing.T) {
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const apiKey = "0123456789abcdef"
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	hosts := []string{"node-a:6333", "node-b:6333", "node-c:6333"}
	transport := &restMemoryRaftTransport{nodes: map[string]*cluster.RaftRuntime{}}
	stores := make([]*cluster.RaftStore, 3)
	runtimes := make([]*cluster.RaftRuntime, 3)
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	for i := range ids {
		var err error
		stores[i], err = cluster.OpenRaftStore(paths[i], ids[i], 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := stores[i].ConfigureVoters(ids); err != nil {
			t.Fatal(err)
		}
		local := ids[i]
		runtimes[i], err = cluster.NewRaftRuntime(stores[i], local, func() []cluster.RaftPeer {
			result := []cluster.RaftPeer{}
			for index, id := range ids {
				if id != local {
					result = append(result, cluster.RaftPeer{NodeID: id, BaseURL: "memory://" + hosts[index]})
				}
			}
			return result
		}, transport, cluster.RaftRuntimeConfig{ElectionMin: 40 * time.Millisecond, ElectionMax: 80 * time.Millisecond, Heartbeat: 10 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		transport.nodes[ids[i]] = runtimes[i]
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 1)
	go func() { runtimes[0].Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if role, _, _ := runtimes[0].Status(); role == cluster.RaftLeader {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if role, _, _ := runtimes[0].Status(); role != cluster.RaftLeader {
		t.Fatal("three-voter configuration did not elect the designated leader")
	}
	config := core.CollectionConfig{Name: "leave-data", Dimension: 2, Metric: core.MetricDot, ShardCount: 48}
	dbs := make([]*engine.Engine, 3)
	barriers := make([]*cluster.RebalanceBarrierStore, 3)
	peerViews := make([]staticPeerProvider, 3)
	for i := range ids {
		var err error
		dbs[i], err = engine.Open(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		defer dbs[i].Close()
		if err := dbs[i].CreateCollection(config); err != nil {
			t.Fatal(err)
		}
		barriers[i], err = cluster.OpenRebalanceBarrierStore(paths[i])
		if err != nil {
			t.Fatal(err)
		}
		for peerIndex := range ids {
			if peerIndex != i {
				peerViews[i] = append(peerViews[i], cluster.Peer{SeedURL: "http://" + hosts[peerIndex], NodeID: ids[peerIndex], AdvertiseAddress: hosts[peerIndex], Healthy: true})
			}
		}
	}
	current, err := cluster.PlanReplicaPlacement(1, ids[0], hosts[0], []cluster.Peer(peerViews[0]), []core.CollectionConfig{config}, 1)
	if err != nil {
		t.Fatal(err)
	}
	target, err := cluster.PlanReplicaPlacement(2, ids[0], hosts[0], []cluster.Peer{peerViews[0][0]}, []core.CollectionConfig{config}, 1)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := cluster.PlanRebalance(current, target)
	if err != nil {
		t.Fatal(err)
	}
	var selected cluster.ShardMovement
	for _, movement := range plan.Movements {
		if movement.SourceLeaderID == ids[2] {
			selected = movement
			break
		}
	}
	if selected.Collection == "" {
		t.Fatal("leave plan has no shard sourced from removed node")
	}
	record := recordForTestShard(t, dbs[2], config, selected.ShardID)
	if _, _, err := dbs[2].BatchUpsertShardWithSequence(config.Name, selected.ShardID, []core.Record{record}); err != nil {
		t.Fatal(err)
	}
	executor, err := cluster.OpenRebalanceExecutor(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	handlers := map[string]http.Handler{}
	var failTransfer atomic.Bool
	failTransfer.Store(true)
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		selectedSuffix := fmt.Sprintf("/internal/rebalance/%s/%d/snapshot", config.Name, selected.ShardID)
		if strings.HasSuffix(request.URL.Path, selectedSuffix) && failTransfer.Swap(false) {
			return nil, errors.New("injected leave target outage")
		}
		handler := handlers[request.URL.Host]
		if handler == nil {
			return nil, fmt.Errorf("unknown host %s", request.URL.Host)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	buildHandler := func(i int, coordinatorExecutor *cluster.RebalanceExecutor) http.Handler {
		options := Options{APIKey: apiKey, NodeID: ids[i], ClusterID: clusterID, AdvertiseAddress: hosts[i], ReplicationFactor: 1, PeerProvider: peerViews[i], InternalHTTPClient: client, RaftStore: stores[i], RaftProtocol: runtimes[i], RebalanceBarriers: barriers[i]}
		if i == 0 {
			options.RebalanceExecutor = coordinatorExecutor
		}
		return NewWithOptions(dbs[i], nil, options).Handler()
	}
	for i := range ids {
		handlers[hosts[i]] = buildHandler(i, executor)
	}
	handler := handlers[hosts[0]]
	payload, _ := json.Marshal(rebalanceRequest{ExpectedEpoch: 1, LeaveNodeID: ids[2]})
	call := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		request.Header.Set("Authorization", "Bearer "+apiKey)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	if response := call("/v1/cluster/rebalance/prepare"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("injected prepare failure status=%d body=%s", response.Code, response.Body.String())
	}
	executor, err = cluster.OpenRebalanceExecutor(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	handlers[hosts[0]] = buildHandler(0, executor)
	handler = handlers[hosts[0]]
	if response := call("/v1/cluster/rebalance/prepare"); response.Code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
	}
	targetIndex := 0
	if selected.TargetLeaderID == ids[1] {
		targetIndex = 1
	}
	_, staged, err := dbs[targetIndex].ExportReplicaSnapshot(config.Name, selected.ShardID)
	if err != nil || len(staged) != 1 || staged[0].ID != record.ID {
		t.Fatalf("staged=%v err=%v target=%d", staged, err, targetIndex)
	}
	if _, frozen := barriers[2].IsFrozen(config.Name, selected.ShardID); !frozen {
		t.Fatal("removed source shard was not frozen before cutover")
	}
	if response := call("/v1/cluster/rebalance/apply"); response.Code != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body.String())
	}
	if _, frozen := barriers[2].IsFrozen(config.Name, selected.ShardID); frozen {
		t.Fatal("removed source barrier was not released after cutover")
	}
	wantVoters := ids[:2]
	for i, store := range stores {
		_, _, epoch := store.State()
		voters := store.Voters()
		if epoch != 2 || len(voters) != len(wantVoters) || voters[0] != wantVoters[0] || voters[1] != wantVoters[1] {
			t.Fatalf("node %d epoch=%d voters=%v", i, epoch, voters)
		}
	}
	if _, err := stores[2].BeginElection(); err == nil {
		t.Fatal("removed voter was allowed to campaign")
	}
	if _, exists := executor.Status(); exists {
		t.Fatal("committed leave preparation journal was not cleared")
	}
}

func containsTestString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func recordForTestShard(t *testing.T, db *engine.Engine, config core.CollectionConfig, shard uint32) core.Record {
	t.Helper()
	for candidate := 0; candidate < 100000; candidate++ {
		record := core.Record{ID: fmt.Sprintf("rebalance-%d", candidate), Vector: []float32{1, 2}}
		got, err := db.RouteShard(config.Name, "", record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got == shard {
			return record
		}
	}
	t.Fatal("could not find record for shard")
	return core.Record{}
}
