package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/engine"
)

func TestLeaderMetadataEpochProposalUsesCanonicalView(t *testing.T) {
	const apiKey = "0123456789abcdef"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const nodeID = "11111111111111111111111111111111"
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := cluster.OpenRaftStore(t.TempDir(), nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := cluster.NewRaftRuntime(store, nodeID, func() []cluster.RaftPeer { return nil }, &cluster.HTTPRaftTransport{ClusterID: clusterID}, cluster.RaftRuntimeConfig{ElectionMin: 40 * time.Millisecond, ElectionMax: 60 * time.Millisecond, Heartbeat: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if role, _, _ := runtime.Status(); role == cluster.RaftLeader {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	handler := NewWithOptions(db, nil, Options{APIKey: apiKey, NodeID: nodeID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, RaftStore: store, RaftProtocol: runtime}).Handler()
	bad := raftRPC(t, handler, "/v1/cluster/metadata/epoch", metadataEpochProposal{ExpectedEpoch: 9}, apiKey, clusterID)
	if bad.Code != http.StatusConflict || !strings.Contains(bad.Body.String(), "epoch_precondition_failed") {
		t.Fatalf("precondition status=%d body=%s", bad.Code, bad.Body.String())
	}
	response := raftRPC(t, handler, "/v1/cluster/metadata/epoch", metadataEpochProposal{ExpectedEpoch: 1}, apiKey, clusterID)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"metadata_epoch":2`) || !strings.Contains(response.Body.String(), `"placement_digest"`) {
		t.Fatalf("proposal status=%d body=%s", response.Code, response.Body.String())
	}
	_, commitIndex, epoch := store.State()
	if commitIndex != 1 || epoch != 2 {
		t.Fatalf("commit=%d epoch=%d", commitIndex, epoch)
	}
}

func TestStaticReadinessRequiresCommittedRaftView(t *testing.T) {
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const nodeID = "11111111111111111111111111111111"
	const address = "node-a:6333"
	for _, test := range []struct {
		name  string
		match bool
		ready bool
	}{{name: "matching", match: true, ready: true}, {name: "mismatch", match: false, ready: false}} {
		t.Run(test.name, func(t *testing.T) {
			db, err := engine.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store, err := cluster.OpenRaftStore(t.TempDir(), nodeID, 1)
			if err != nil {
				t.Fatal(err)
			}
			vote, err := store.BeginElection()
			if err != nil {
				t.Fatal(err)
			}
			digests, err := cluster.ComputeViewDigests(2, nodeID, address, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !test.match {
				digests.Placement = strings.Repeat("f", 64)
			}
			command := cluster.MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement}
			index, err := store.AppendCommand(vote.Term, command)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.CommitWithQuorum(index, vote.Term, 1, 1); err != nil {
				t.Fatal(err)
			}
			handler := NewWithOptions(db, nil, Options{NodeID: nodeID, ClusterID: clusterID, AdvertiseAddress: address, MetadataEpoch: 1, RaftStore: store, EnableStaticRouting: true}).Handler()
			request := httptest.NewRequest(http.MethodGet, "/v1/cluster/readiness", nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"ready":true`) != test.ready {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRaftRPCAuthenticationClusterFenceAndLiveEpoch(t *testing.T) {
	const apiKey = "0123456789abcdef"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const followerID = "11111111111111111111111111111111"
	const leaderID = "22222222222222222222222222222222"
	db, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := cluster.OpenRaftStore(t.TempDir(), followerID, 1)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewWithOptions(db, nil, Options{APIKey: apiKey, NodeID: followerID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, RaftStore: store}).Handler()

	vote := cluster.RequestVoteRequest{Term: 1, CandidateID: leaderID}
	if response := raftRPC(t, handler, "/v1/internal/raft/request-vote", vote, "", clusterID); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated vote status=%d", response.Code)
	}
	if response := raftRPC(t, handler, "/v1/internal/raft/request-vote", vote, apiKey, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "cluster_mismatch") {
		t.Fatalf("cross-cluster vote status=%d body=%s", response.Code, response.Body.String())
	}
	response := raftRPC(t, handler, "/v1/internal/raft/request-vote", vote, apiKey, clusterID)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"vote_granted":true`) {
		t.Fatalf("vote status=%d body=%s", response.Code, response.Body.String())
	}
	command := cluster.MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	appendRequest := cluster.AppendEntriesRequest{Term: 1, LeaderID: leaderID, Entries: []cluster.RaftEntry{{Term: 1, Command: command}}, LeaderCommit: 1}
	response = raftRPC(t, handler, "/v1/internal/raft/append-entries", appendRequest, apiKey, clusterID)
	if response.Code != http.StatusOK || response.Header().Get("X-VectorDB-Metadata-Epoch") != "2" {
		t.Fatalf("append status=%d epoch=%q body=%s", response.Code, response.Header().Get("X-VectorDB-Metadata-Epoch"), response.Body.String())
	}
	nodeRequest := httptest.NewRequest(http.MethodGet, "/v1/node", nil)
	nodeRequest.Header.Set("Authorization", "Bearer "+apiKey)
	nodeResponse := httptest.NewRecorder()
	handler.ServeHTTP(nodeResponse, nodeRequest)
	if nodeResponse.Code != http.StatusOK || !strings.Contains(nodeResponse.Body.String(), `"metadata_epoch":2`) {
		t.Fatalf("node info status=%d body=%s", nodeResponse.Code, nodeResponse.Body.String())
	}
}

func TestThreeNodeRaftRPCReplicationAndQuorumCommit(t *testing.T) {
	const apiKey = "0123456789abcdef"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	stores := make([]*cluster.RaftStore, 3)
	handlers := make([]http.Handler, 3)
	for index := range stores {
		var err error
		stores[index], err = cluster.OpenRaftStore(t.TempDir(), ids[index], 1)
		if err != nil {
			t.Fatal(err)
		}
		db, err := engine.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		handlers[index] = NewWithOptions(db, nil, Options{APIKey: apiKey, NodeID: ids[index], ClusterID: clusterID, AdvertiseAddress: "node:6333", MetadataEpoch: 1, RaftStore: stores[index]}).Handler()
	}
	vote, err := stores[0].BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	votes := 1
	for index := 1; index < 3; index++ {
		response := raftRPC(t, handlers[index], "/v1/internal/raft/request-vote", vote, apiKey, clusterID)
		var result cluster.RequestVoteResponse
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.VoteGranted {
			t.Fatalf("node %d vote status=%d body=%s", index, response.Code, response.Body.String())
		}
		votes++
	}
	command := cluster.MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	logIndex, err := stores[0].AppendCommand(vote.Term, command)
	if err != nil {
		t.Fatal(err)
	}
	acknowledgements := 1
	entry := cluster.RaftEntry{Term: vote.Term, Command: command}
	for index := 1; index < 3; index++ {
		response := raftRPC(t, handlers[index], "/v1/internal/raft/append-entries", cluster.AppendEntriesRequest{Term: vote.Term, LeaderID: ids[0], Entries: []cluster.RaftEntry{entry}}, apiKey, clusterID)
		var result cluster.AppendEntriesResponse
		if response.Code == http.StatusOK && json.Unmarshal(response.Body.Bytes(), &result) == nil && result.Success {
			acknowledgements++
		}
	}
	if votes < 2 || acknowledgements < 2 {
		t.Fatalf("votes=%d acknowledgements=%d", votes, acknowledgements)
	}
	if err := stores[0].CommitWithQuorum(logIndex, vote.Term, acknowledgements, 3); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < 3; index++ {
		response := raftRPC(t, handlers[index], "/v1/internal/raft/append-entries", cluster.AppendEntriesRequest{Term: vote.Term, LeaderID: ids[0], PrevLogIndex: 1, PrevLogTerm: vote.Term, LeaderCommit: 1}, apiKey, clusterID)
		if response.Code != http.StatusOK || response.Header().Get("X-VectorDB-Metadata-Epoch") != "2" {
			t.Fatalf("node %d commit status=%d epoch=%q", index, response.Code, response.Header().Get("X-VectorDB-Metadata-Epoch"))
		}
	}
	for index := range stores {
		_, commitIndex, epoch := stores[index].State()
		if commitIndex != 1 || epoch != 2 {
			t.Fatalf("node %d commit=%d epoch=%d", index, commitIndex, epoch)
		}
	}
}

func raftRPC(t *testing.T, handler http.Handler, path string, body any, apiKey, clusterID string) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-VectorDB-Cluster-ID", clusterID)
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
