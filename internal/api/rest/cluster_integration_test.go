package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

type clusterTestNode struct {
	path    string
	db      *engine.Engine
	handler http.Handler
	peers   staticPeerProvider
}

func TestThreeNodeStaticClusterPartitionsWritesSearchesAndRecovers(t *testing.T) {
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const apiKey = "0123456789abcdef"
	nodeIDs := []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
	}
	hosts := []string{"node-a:6333", "node-b:6333", "node-c:6333"}
	config := core.CollectionConfig{Name: "distributed", Dimension: 2, Metric: core.MetricDot, ShardCount: 24}
	nodes := make([]clusterTestNode, 3)
	for position := range nodes {
		nodes[position].path = t.TempDir()
		db, err := engine.Open(nodes[position].path)
		if err != nil {
			t.Fatal(err)
		}
		nodes[position].db = db
		if err := db.CreateCollection(config); err != nil {
			t.Fatal(err)
		}
		for peerPosition := range nodes {
			if peerPosition == position {
				continue
			}
			nodes[position].peers = append(nodes[position].peers, cluster.Peer{SeedURL: "http://" + hosts[peerPosition], NodeID: nodeIDs[peerPosition], ClusterID: clusterID, AdvertiseAddress: hosts[peerPosition], Healthy: true, State: cluster.PeerHealthy})
		}
	}
	digests, err := cluster.ComputeViewDigests(2, nodeIDs[0], hosts[0], []cluster.Peer(nodes[0].peers), []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	for position := range nodes {
		for peerPosition := range nodes[position].peers {
			peer := &nodes[position].peers[peerPosition]
			peer.MetadataEpoch = 2
			peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = digests.Membership, digests.Catalog, digests.Placement
		}
	}
	handlers := make(map[string]http.Handler)
	transport := restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		handler := handlers[request.URL.Host]
		if handler == nil {
			return nil, fmt.Errorf("unknown cluster host %q", request.URL.Host)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})
	client := &http.Client{Transport: transport}
	for position := range nodes {
		store := committedTestRaftStore(t, nodeIDs[position], nodeIDs, 2, digests)
		nodes[position].handler = NewWithOptions(nodes[position].db, nil, Options{APIKey: apiKey, NodeID: nodeIDs[position], ClusterID: clusterID, AdvertiseAddress: hosts[position], MetadataEpoch: 2, PeerProvider: nodes[position].peers, InternalHTTPClient: client, EnableStaticRouting: true, RaftStore: store}).Handler()
		handlers[hosts[position]] = nodes[position].handler
	}

	placement, err := cluster.PlanPlacement(2, nodeIDs[0], hosts[0], []cluster.Peer(nodes[0].peers), []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	ownerByShard := make(map[uint32]string, config.ShardCount)
	for _, assignment := range placement.Shards {
		ownerByShard[assignment.ShardID] = assignment.NodeID
	}
	records := make([]core.Record, config.ShardCount)
	filled := make(map[uint32]bool)
	for candidate := 0; len(filled) < config.ShardCount; candidate++ {
		id := fmt.Sprintf("record-%d", candidate)
		shardID, routeErr := nodes[0].db.RouteShard(config.Name, "", id)
		if routeErr != nil {
			t.Fatal(routeErr)
		}
		if filled[shardID] {
			continue
		}
		records[shardID] = core.Record{ID: id, Vector: []float32{float32(shardID + 1), 0}}
		filled[shardID] = true
	}
	payload, err := json.Marshal(map[string]any{"records": records})
	if err != nil {
		t.Fatal(err)
	}
	writeRequest := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/distributed/vectors/batch", strings.NewReader(string(payload)))
	writeRequest.Header.Set("Authorization", "Bearer "+apiKey)
	writeRequest.Header.Set("Content-Type", "application/json")
	writeRequest.Header.Set("Idempotency-Key", "cluster:bootstrap-001")
	writeResponse := httptest.NewRecorder()
	nodes[0].handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK || strings.Count(writeResponse.Body.String(), `"status":"committed"`) != config.ShardCount {
		t.Fatalf("write status=%d body=%s", writeResponse.Code, writeResponse.Body.String())
	}

	indexByNode := map[string]int{nodeIDs[0]: 0, nodeIDs[1]: 1, nodeIDs[2]: 2}
	ownedCounts := make([]int, len(nodes))
	for shardID, record := range records {
		ownerIndex := indexByNode[ownerByShard[uint32(shardID)]]
		ownedCounts[ownerIndex]++
		for nodeIndex := range nodes {
			got, getErr := nodes[nodeIndex].db.Get(config.Name, "", record.ID)
			if nodeIndex == ownerIndex {
				if getErr != nil || got.ID != record.ID {
					t.Fatalf("owner %d missing shard %d record: %#v %v", nodeIndex, shardID, got, getErr)
				}
			} else if !errors.Is(getErr, core.ErrShardNotOwned) {
				t.Fatalf("non-owner %d stores shard %d record: %#v %v", nodeIndex, shardID, got, getErr)
			}
		}
	}
	for index, count := range ownedCounts {
		if count == 0 {
			t.Fatalf("node %d owns no records", index)
		}
	}
	assertPhysicalOwnership(t, nodes, config, ownerByShard, indexByNode)

	searchRequest := httptest.NewRequest(http.MethodPost, "/v1/cluster/collections/distributed/search", strings.NewReader(`{"vector":[1,0],"top_k":3}`))
	searchRequest.Header.Set("Authorization", "Bearer "+apiKey)
	searchRequest.Header.Set("Content-Type", "application/json")
	searchResponse := httptest.NewRecorder()
	nodes[2].handler.ServeHTTP(searchResponse, searchRequest)
	if searchResponse.Code != http.StatusOK {
		t.Fatalf("search status=%d body=%s", searchResponse.Code, searchResponse.Body.String())
	}
	var searchBody struct {
		Results       []core.SearchResult `json:"results"`
		Authoritative bool                `json:"authoritative_placement"`
	}
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &searchBody); err != nil {
		t.Fatal(err)
	}
	if !searchBody.Authoritative || len(searchBody.Results) != 3 {
		t.Fatalf("unexpected search response: %s", searchResponse.Body.String())
	}
	for position, wantShard := range []int{23, 22, 21} {
		if searchBody.Results[position].ID != records[wantShard].ID {
			t.Fatalf("result %d=%s, want %s", position, searchBody.Results[position].ID, records[wantShard].ID)
		}
	}

	for position := range nodes {
		if err := nodes[position].db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := engine.Open(nodes[position].path)
		if err != nil {
			t.Fatal(err)
		}
		nodes[position].db = reopened
	}
	defer func() {
		for position := range nodes {
			_ = nodes[position].db.Close()
		}
	}()
	for shardID, record := range records {
		ownerIndex := indexByNode[ownerByShard[uint32(shardID)]]
		if _, err := nodes[ownerIndex].db.Get(config.Name, "", record.ID); err != nil {
			t.Fatalf("recovered owner %d missing shard %d: %v", ownerIndex, shardID, err)
		}
		for nodeIndex := range nodes {
			if nodeIndex != ownerIndex {
				if _, err := nodes[nodeIndex].db.Get(config.Name, "", record.ID); !errors.Is(err, core.ErrShardNotOwned) {
					t.Fatalf("recovered non-owner %d accepted shard %d: %v", nodeIndex, shardID, err)
				}
			}
		}
	}
	assertPhysicalOwnership(t, nodes, config, ownerByShard, indexByNode)
}

func assertPhysicalOwnership(t *testing.T, nodes []clusterTestNode, config core.CollectionConfig, ownerByShard map[uint32]string, indexByNode map[string]int) {
	t.Helper()
	for nodeIndex := range nodes {
		if _, err := os.Stat(filepath.Join(nodes[nodeIndex].path, engine.OwnershipFile)); err != nil {
			t.Fatalf("node %d ownership manifest: %v", nodeIndex, err)
		}
		for shardID := 0; shardID < config.ShardCount; shardID++ {
			walPath := filepath.Join(nodes[nodeIndex].path, "wal", config.Name, fmt.Sprintf("shard-%06d.wal", shardID))
			_, err := os.Stat(walPath)
			ownerIndex := indexByNode[ownerByShard[uint32(shardID)]]
			if nodeIndex == ownerIndex && err != nil {
				t.Fatalf("owner %d shard %d WAL missing: %v", nodeIndex, shardID, err)
			}
			if nodeIndex != ownerIndex && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("non-owner %d shard %d WAL still exists: %v", nodeIndex, shardID, err)
			}
			ledgerPath := filepath.Join(nodes[nodeIndex].path, "idempotency", config.Name, fmt.Sprintf("shard-%06d.json", shardID))
			_, ledgerErr := os.Stat(ledgerPath)
			if nodeIndex == ownerIndex && ledgerErr != nil {
				t.Fatalf("owner %d shard %d idempotency ledger missing: %v", nodeIndex, shardID, ledgerErr)
			}
			if nodeIndex != ownerIndex && !errors.Is(ledgerErr, os.ErrNotExist) {
				t.Fatalf("non-owner %d shard %d idempotency ledger exists: %v", nodeIndex, shardID, ledgerErr)
			}
		}
	}
}
