package rest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
)

func TestLeaderFanoutRequiresQuorumAndPreservesPreparedRecord(t *testing.T) {
	const leaderID = "11111111111111111111111111111111"
	const followerID = "22222222222222222222222222222222"
	const clusterID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	config := core.CollectionConfig{Name: "replicated", Dimension: 2, Metric: core.MetricDot, ShardCount: 16}

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

	leaderPeers := staticPeerProvider{{SeedURL: "http://node-b", NodeID: followerID, AdvertiseAddress: "node-b:6333", Healthy: true}}
	followerPeers := staticPeerProvider{{SeedURL: "http://node-a", NodeID: leaderID, AdvertiseAddress: "node-a:6333", Healthy: true}}
	follower := NewWithOptions(followerDB, nil, Options{NodeID: followerID, ClusterID: clusterID, AdvertiseAddress: "node-b:6333", MetadataEpoch: 1, ReplicationFactor: 2, PeerProvider: followerPeers, RaftProtocol: fixedRaftStatus{term: 7}})
	if err := follower.activateLocalOwnership(); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	client := &http.Client{Transport: restRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if fail.Load() {
			return nil, errors.New("follower unavailable")
		}
		recorder := httptest.NewRecorder()
		follower.Handler().ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	leader := NewWithOptions(leaderDB, nil, Options{NodeID: leaderID, ClusterID: clusterID, AdvertiseAddress: "node-a:6333", MetadataEpoch: 1, ReplicationFactor: 2, PeerProvider: leaderPeers, InternalHTTPClient: client, RaftProtocol: fixedRaftStatus{term: 7}})
	if err := leader.activateLocalOwnership(); err != nil {
		t.Fatal(err)
	}

	plan, err := cluster.PlanReplicaPlacement(1, leaderID, "node-a:6333", []cluster.Peer(leaderPeers), []core.CollectionConfig{config}, 2)
	if err != nil {
		t.Fatal(err)
	}
	var leaderShard uint32
	found := false
	for _, shard := range plan.Shards {
		if shard.LeaderID == leaderID {
			leaderShard, found = shard.ShardID, true
			break
		}
	}
	if !found {
		t.Fatal("test placement has no leader shard")
	}
	recordForShard := func(prefix string) core.Record {
		for candidate := 0; candidate < 10_000; candidate++ {
			record := core.Record{ID: fmt.Sprintf("%s-%d", prefix, candidate), Vector: []float32{1, 2}}
			shardID, routeErr := leaderDB.RouteShard(config.Name, "", record.ID)
			if routeErr != nil {
				t.Fatal(routeErr)
			}
			if shardID == leaderShard {
				return record
			}
		}
		t.Fatal("could not route test record")
		return core.Record{}
	}

	record := recordForShard("committed")
	prepared, acknowledged, ambiguous, err := leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{record}, "quorum", "")
	if err != nil || ambiguous || acknowledged != 2 || len(prepared) != 1 {
		t.Fatalf("prepared=%#v acknowledged=%d ambiguous=%v err=%v", prepared, acknowledged, ambiguous, err)
	}
	replicated, err := followerDB.Get(config.Name, "", record.ID)
	if err != nil || replicated.Version != prepared[0].Version || replicated.Timestamp != prepared[0].Timestamp {
		t.Fatalf("replicated=%#v err=%v prepared=%#v", replicated, err, prepared[0])
	}

	fail.Store(true)
	unavailable := recordForShard("unavailable")
	prepared, acknowledged, ambiguous, err = leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{unavailable}, "quorum", "")
	if err == nil || !ambiguous || acknowledged != 1 || len(prepared) != 1 {
		t.Fatalf("prepared=%#v acknowledged=%d ambiguous=%v err=%v", prepared, acknowledged, ambiguous, err)
	}
	if _, err := leaderDB.Get(config.Name, "", unavailable.ID); err != nil {
		t.Fatalf("leader must retain ambiguous durable write: %v", err)
	}
	if _, err := followerDB.Get(config.Name, "", unavailable.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("follower unexpectedly contains failed-fanout record: %v", err)
	}
	var metrics bytes.Buffer
	leader.metrics.writeTo(&metrics, leader)
	lagLabel := fmt.Sprintf(`gideondb_replication_lag_sequences{collection="replicated",shard="%d",node_id="%s"}`, leaderShard, followerID)
	if !strings.Contains(metrics.String(), lagLabel+" 1") {
		t.Fatalf("missing failed-fanout lag metric:\n%s", metrics.String())
	}

	fail.Store(false)
	recovered := recordForShard("recovered")
	prepared, acknowledged, ambiguous, err = leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{recovered}, "quorum", "")
	if err != nil || ambiguous || acknowledged != 2 || len(prepared) != 1 {
		t.Fatalf("recovery prepared=%#v acknowledged=%d ambiguous=%v err=%v", prepared, acknowledged, ambiguous, err)
	}
	for _, id := range []string{unavailable.ID, recovered.ID} {
		if _, err := followerDB.Get(config.Name, "", id); err != nil {
			t.Fatalf("snapshot recovery did not restore %s: %v", id, err)
		}
	}
	metrics.Reset()
	leader.metrics.writeTo(&metrics, leader)
	if !strings.Contains(metrics.String(), lagLabel+" 0") || !strings.Contains(metrics.String(), `gideondb_replication_operations_total{operation="snapshot",result="success"} 1`) {
		t.Fatalf("missing recovery metrics:\n%s", metrics.String())
	}

	const concurrentWrites = 24
	errCh := make(chan error, concurrentWrites)
	var wait sync.WaitGroup
	for position := range concurrentWrites {
		wait.Add(1)
		go func() {
			defer wait.Done()
			record := recordForShard(fmt.Sprintf("parallel-%d", position))
			_, acknowledged, ambiguous, writeErr := leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{record}, "quorum", "")
			if writeErr != nil || ambiguous || acknowledged != 2 {
				errCh <- fmt.Errorf("acknowledged=%d ambiguous=%v err=%v", acknowledged, ambiguous, writeErr)
			}
		}()
	}
	wait.Wait()
	close(errCh)
	for writeErr := range errCh {
		t.Fatal(writeErr)
	}
	_, leaderRecords, err := leaderDB.ExportReplicaSnapshot(config.Name, leaderShard)
	if err != nil {
		t.Fatal(err)
	}
	_, followerRecords, err := followerDB.ExportReplicaSnapshot(config.Name, leaderShard)
	if err != nil {
		t.Fatal(err)
	}
	versions := make(map[string]uint64, len(leaderRecords))
	for _, item := range leaderRecords {
		versions[item.ID] = item.Version
	}
	if len(followerRecords) != len(leaderRecords) {
		t.Fatalf("follower records=%d leader records=%d", len(followerRecords), len(leaderRecords))
	}
	for _, item := range followerRecords {
		if versions[item.ID] != item.Version {
			t.Fatalf("replica version divergence for %s: follower=%d leader=%d", item.ID, item.Version, versions[item.ID])
		}
	}

	fail.Store(true)
	leaderOnly := recordForShard("leader-ack")
	_, acknowledged, ambiguous, err = leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{leaderOnly}, "leader", "")
	if err != nil || ambiguous || acknowledged != 1 {
		t.Fatalf("leader acknowledgement acknowledged=%d ambiguous=%v err=%v", acknowledged, ambiguous, err)
	}
	allRequired := recordForShard("all-ack")
	_, acknowledged, ambiguous, err = leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{allRequired}, "all", "")
	if err == nil || !ambiguous || acknowledged != 1 {
		t.Fatalf("all acknowledgement acknowledged=%d ambiguous=%v err=%v", acknowledged, ambiguous, err)
	}
	fail.Store(false)
	catchUp := recordForShard("ack-catch-up")
	if _, acknowledged, ambiguous, err = leader.commitLeaderShardBatch(context.Background(), config.Name, leaderShard, []core.Record{catchUp}, "quorum", ""); err != nil || ambiguous || acknowledged != 2 {
		t.Fatalf("ack recovery acknowledged=%d ambiguous=%v err=%v", acknowledged, ambiguous, err)
	}
}

func TestAcknowledgementValidationAndThresholds(t *testing.T) {
	for value, expected := range map[string]string{"": "quorum", "leader": "leader", "quorum": "quorum", "all": "all"} {
		actual, err := parseAcknowledgement(value)
		if err != nil || actual != expected {
			t.Fatalf("parse %q=%q err=%v", value, actual, err)
		}
	}
	if _, err := parseAcknowledgement("eventual"); err == nil {
		t.Fatal("expected invalid acknowledgement rejection")
	}
	if requiredAcknowledgements("leader", 3) != 1 || requiredAcknowledgements("quorum", 3) != 2 || requiredAcknowledgements("all", 3) != 3 {
		t.Fatal("unexpected acknowledgement thresholds")
	}
}
