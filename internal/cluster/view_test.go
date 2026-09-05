package cluster

import (
	"testing"

	"github.com/vectordb/vectordb/internal/core"
)

func TestViewDigestsConvergeAcrossNodeVantagePoints(t *testing.T) {
	collections := []core.CollectionConfig{
		{Name: "zeta", Dimension: 2, Metric: core.MetricDot, ShardCount: 3},
		{Name: "alpha", Dimension: 2, Metric: core.MetricCosine, ShardCount: 5},
	}
	fromA, err := ComputeViewDigests(4, localTestNode, "a:6333", []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "b:6333"}, {NodeID: clusterTestID, AdvertiseAddress: "c:6333"}}, collections)
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := ComputeViewDigests(4, remoteTestNode, "b:6333", []Peer{{NodeID: clusterTestID, AdvertiseAddress: "c:6333"}, {NodeID: localTestNode, AdvertiseAddress: "a:6333"}}, []core.CollectionConfig{collections[1], collections[0]})
	if err != nil {
		t.Fatal(err)
	}
	if fromA != fromB {
		t.Fatalf("digests differ by vantage point: %#v != %#v", fromA, fromB)
	}
	changed := append([]core.CollectionConfig(nil), collections...)
	changed[0].ShardCount++
	other, err := ComputeViewDigests(4, localTestNode, "a:6333", []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "b:6333"}, {NodeID: clusterTestID, AdvertiseAddress: "c:6333"}}, changed)
	if err != nil {
		t.Fatal(err)
	}
	if other.Catalog == fromA.Catalog || other.Placement == fromA.Placement {
		t.Fatal("catalog change did not alter dependent digests")
	}
}

func TestViewDigestsRejectDuplicateIdentity(t *testing.T) {
	_, err := ComputeViewDigests(1, localTestNode, "a:6333", []Peer{{NodeID: localTestNode, AdvertiseAddress: "duplicate:6333"}}, nil)
	if err == nil {
		t.Fatal("expected duplicate identity rejection")
	}
}

func TestCapacityAwareViewDigestsConvergeAndFenceChanges(t *testing.T) {
	collections := []core.CollectionConfig{{Name: "weighted", Dimension: 2, Metric: core.MetricDot, ShardCount: 64}}
	fromA, err := ComputeViewDigestsWithCapacity(4, localTestNode, "a:6333", 1, []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "b:6333", PlacementCapacity: 3}}, collections)
	if err != nil {
		t.Fatal(err)
	}
	fromB, err := ComputeViewDigestsWithCapacity(4, remoteTestNode, "b:6333", 3, []Peer{{NodeID: localTestNode, AdvertiseAddress: "a:6333", PlacementCapacity: 1}}, collections)
	if err != nil {
		t.Fatal(err)
	}
	if fromA != fromB {
		t.Fatalf("weighted digests differ by vantage point: %#v != %#v", fromA, fromB)
	}
	changed, err := ComputeViewDigestsWithCapacity(4, localTestNode, "a:6333", 2, []Peer{{NodeID: remoteTestNode, AdvertiseAddress: "b:6333", PlacementCapacity: 3}}, collections)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Placement == fromA.Placement {
		t.Fatal("capacity change did not alter placement digest")
	}
	parsed, err := ParseCapacityManifest(fromA.CapacityManifest)
	if err != nil || len(parsed) != 2 || parsed[0].NodeID != localTestNode || parsed[0].Capacity != 1 || parsed[1].Capacity != 3 {
		t.Fatalf("parsed capacities=%#v err=%v", parsed, err)
	}
	if _, err := ParseCapacityManifest(`[{"node_id":"22222222222222222222222222222222","capacity":1},{"node_id":"11111111111111111111111111111111","capacity":1}]`); err == nil {
		t.Fatal("accepted non-canonical capacity manifest")
	}
}
