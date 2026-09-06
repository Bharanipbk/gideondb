package engine

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestClusterRecoveryPointIsFencedAndCanonical(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	config := core.CollectionConfig{Name: "docs", Dimension: 2, Metric: core.MetricDot, ShardCount: 8}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Upsert("docs", core.Record{ID: "one", Vector: []float32{1, 2}}); err != nil {
		t.Fatal(err)
	}
	nodeA := "11111111111111111111111111111111"
	nodeB := "22222222222222222222222222222222"
	digest := strings.Repeat("a", 64)
	pointA, err := db.CaptureRecoveryPoint(9, nodeA, digest, "")
	if err != nil {
		t.Fatal(err)
	}
	pointB := NodeRecoveryPoint{MetadataEpoch: 9, NodeID: nodeB, PlacementDigest: digest, Shards: []ShardRecoveryPoint{{Collection: "docs", ShardID: 0, NodeID: nodeB, Sequence: 4}}}
	first, err := MergeRecoveryPoints([]NodeRecoveryPoint{pointB, pointA})
	if err != nil {
		t.Fatal(err)
	}
	second, err := MergeRecoveryPoints([]NodeRecoveryPoint{pointA, pointB})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || first.Format != 1 || first.MetadataEpoch != 9 || len(first.Nodes) != 2 {
		t.Fatalf("non-canonical recovery manifest: %#v != %#v", first, second)
	}
	conflict := pointB
	conflict.MetadataEpoch = 10
	if _, err := MergeRecoveryPoints([]NodeRecoveryPoint{pointA, conflict}); err == nil {
		t.Fatal("accepted recovery points from different committed epochs")
	}
}
