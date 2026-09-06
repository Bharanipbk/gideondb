package engine

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/cluster"
	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestMaterializeClusterRestorePublishesRemappedReplicasAtomically(t *testing.T) {
	root := t.TempDir()
	oldNode := "11111111111111111111111111111111"
	newA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	newB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	db, err := Open(filepath.Join(root, "source"))
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 2}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	records := recordsForEveryShard(t, db, config.ShardCount)
	for _, record := range records {
		if _, err := db.Upsert(config.Name, record); err != nil {
			t.Fatal(err)
		}
	}
	point, err := db.CaptureRecoveryPoint(5, oldNode, recoveryDigest('a'), "")
	if err != nil {
		t.Fatal(err)
	}
	nodeArchive := filepath.Join(root, "node.tar.gz")
	if err := db.BackupAtRecoveryPoint(nodeArchive, point); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, err := MergeRecoveryPoints([]NodeRecoveryPoint{point})
	if err != nil {
		t.Fatal(err)
	}
	clusterArchive := filepath.Join(root, "cluster.tar.gz")
	if err := CreateClusterBackup(clusterArchive, manifest, map[string]string{oldNode: nodeArchive}); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "verified")
	if err := RestoreClusterBackup(clusterArchive, restored); err != nil {
		t.Fatal(err)
	}
	target := cluster.ReplicaPlacementTable{Epoch: 9, ReplicationFactor: 2, Shards: []cluster.ShardReplicaPlacement{
		{Collection: config.Name, ShardID: 0, LeaderID: newA, Replicas: []string{newA, newB}},
		{Collection: config.Name, ShardID: 1, LeaderID: newB, Replicas: []string{newB, newA}},
	}}
	plan, err := PlanClusterRestore(manifest, target)
	if err != nil {
		t.Fatal(err)
	}
	materialized := filepath.Join(root, "materialized")
	if err := MaterializeClusterRestore(restored, materialized, plan); err != nil {
		t.Fatal(err)
	}
	for _, nodeID := range []string{newA, newB} {
		replica, err := Open(filepath.Join(materialized, "nodes", nodeID))
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			got, err := replica.Get(config.Name, record.Namespace, record.ID)
			if err != nil || got.ID != record.ID {
				t.Fatalf("node %s missing restored record %s: %#v, %v", nodeID, record.ID, got, err)
			}
		}
		for _, assignment := range plan.Assignments {
			sequence, err := replica.ReplicaSequence(config.Name, assignment.ShardID)
			if err != nil || sequence != assignment.SourceSequence {
				t.Fatalf("node %s shard %d sequence = %d, %v; want %d", nodeID, assignment.ShardID, sequence, err, assignment.SourceSequence)
			}
		}
		if err := replica.Close(); err != nil {
			t.Fatal(err)
		}
	}

	forged := plan
	forged.BackupPlacementDigest = recoveryDigest('b')
	rejected := filepath.Join(root, "rejected")
	if err := MaterializeClusterRestore(restored, rejected, forged); err == nil {
		t.Fatal("accepted forged restore provenance")
	}
	if _, err := os.Stat(rejected); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed materialization published destination: %v", err)
	}
}

func recordsForEveryShard(t *testing.T, db *Engine, shardCount int) []core.Record {
	t.Helper()
	records := make([]core.Record, shardCount)
	found := make([]bool, shardCount)
	for candidate := 0; candidate < 10_000; candidate++ {
		id := string(rune(candidate + 1))
		shardID, err := db.RouteShard("docs", "", id)
		if err != nil {
			t.Fatal(err)
		}
		if !found[shardID] {
			records[shardID] = core.Record{ID: id, Vector: []float32{float32(shardID + 1), 1}}
			found[shardID] = true
		}
		complete := true
		for _, present := range found {
			complete = complete && present
		}
		if complete {
			return records
		}
	}
	t.Fatal("could not generate a record for every shard")
	return nil
}
