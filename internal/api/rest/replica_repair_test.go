package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/engine"
)

func TestReplicaRepairDetectsLagAndInstallsSnapshot(t *testing.T) {
	const leaderID = "11111111111111111111111111111111"
	const followerID = "22222222222222222222222222222222"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	config := core.CollectionConfig{Name: "repair", Dimension: 2, Metric: core.MetricDot, ShardCount: 16}
	leaderDB, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer leaderDB.Close()
	followerDB, err := engine.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer followerDB.Close()
	for _, db := range []*engine.Engine{leaderDB, followerDB} {
		if err := db.CreateCollection(config); err != nil {
			t.Fatal(err)
		}
	}
	digests, err := cluster.ComputeViewDigests(1, leaderID, "node-a:6333", []cluster.Peer{{NodeID: followerID, AdvertiseAddress: "node-b:6333"}}, []core.CollectionConfig{config})
	if err != nil {
		t.Fatal(err)
	}
	leaderPeers := staticPeerProvider{{SeedURL: "http://node-b:6333", NodeID: followerID, AdvertiseAddress: "node-b:6333", Healthy: true, State: cluster.PeerHealthy, MetadataEpoch: 1, ReplicationFactor: 2, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement}}
	followerPeers := staticPeerProvider{{SeedURL: "http://node-a:6333", NodeID: leaderID, AdvertiseAddress: "node-a:6333", Healthy: true, State: cluster.PeerHealthy, MetadataEpoch: 1, ReplicationFactor: 2, MembershipDigest: digests.Membership, CatalogDigest: digests.Catalog, PlacementDigest: digests.Placement}}
	follower := NewWithOptions(followerDB, nil, Options{NodeID: followerID, ClusterID: clusterID, AdvertiseAddress: "node-b:6333", ReplicationFactor: 2, PeerProvider: followerPeers, EnableStaticRouting: true, RaftProtocol: fixedRaftStatus{term: 7}})
	if err := follower.activateLocalOwnership(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		follower.Handler().ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	leader := NewWithOptions(leaderDB, nil, Options{NodeID: leaderID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", ReplicationFactor: 2, PeerProvider: leaderPeers, InternalHTTPClient: client, EnableStaticRouting: true, RaftProtocol: fixedRaftStatus{term: 7}})
	if err := leader.activateLocalOwnership(); err != nil {
		t.Fatal(err)
	}
	table, err := cluster.PlanReplicaPlacement(1, leaderID, "node-a:6333", []cluster.Peer(leaderPeers), []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var shard uint32
	found := false
	for _, placement := range table.Shards {
		if placement.LeaderID == leaderID {
			shard, found = placement.ShardID, true
			break
		}
	}
	if !found {
		t.Fatal("no locally led shard")
	}
	record := recordForTestShard(t, leaderDB, config, shard)
	if _, _, err := leaderDB.BatchUpsertShardWithSequence(config.Name, shard, []core.Record{record}); err != nil {
		t.Fatal(err)
	}
	if sequence, err := followerDB.ReplicaSequence(config.Name, shard); err != nil || sequence != 0 {
		t.Fatalf("follower sequence=%d err=%v", sequence, err)
	}
	if err := leader.RepairReplicasOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	sequence, records, err := followerDB.ExportReplicaSnapshot(config.Name, shard)
	if err != nil || sequence != 1 || len(records) != 1 || records[0].ID != record.ID {
		t.Fatalf("sequence=%d records=%v err=%v", sequence, records, err)
	}
	metrics := httptest.NewRecorder()
	leader.serveMetrics(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `vectordb_replication_operations_total{operation="repair",result="success"} 1`) {
		t.Fatalf("repair metric missing: %s", metrics.Body.String())
	}
}

func TestReplicaRepairBackoffGrowsCapsAndClears(t *testing.T) {
	server := &Server{repairRetries: make(map[string]replicaRepairRetry)}
	key := replicaMetricKey("repair", 3, "follower")
	now := time.Date(2026, time.September, 5, 0, 0, 0, 0, time.UTC)

	if !server.repairAttemptAllowed(key, now) {
		t.Fatal("new repair target should be eligible")
	}
	server.recordRepairFailure(key, now)
	if server.repairAttemptAllowed(key, now.Add(time.Second-time.Nanosecond)) {
		t.Fatal("first failure should delay retry for one second")
	}
	if !server.repairAttemptAllowed(key, now.Add(time.Second)) {
		t.Fatal("repair should be eligible at the retry boundary")
	}

	secondAttempt := now.Add(time.Second)
	server.recordRepairFailure(key, secondAttempt)
	if server.repairAttemptAllowed(key, secondAttempt.Add(2*time.Second-time.Nanosecond)) {
		t.Fatal("second failure should delay retry for two seconds")
	}

	for attempt := 0; attempt < 20; attempt++ {
		server.recordRepairFailure(key, now)
	}
	retry := server.repairRetries[key]
	if delay := retry.NextAttempt.Sub(now); delay != 5*time.Minute {
		t.Fatalf("capped delay=%s, want %s", delay, 5*time.Minute)
	}

	server.clearRepairFailure(key)
	if !server.repairAttemptAllowed(key, now) {
		t.Fatal("successful repair should clear backoff")
	}
}
