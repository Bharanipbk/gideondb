package engine

import (
	"fmt"
	"sort"

	"github.com/vectordb/vectordb/internal/cluster"
)

type ShardRecoveryPoint struct {
	Collection string `json:"collection"`
	ShardID    uint32 `json:"shard_id"`
	NodeID     string `json:"node_id"`
	Sequence   uint64 `json:"sequence"`
}

type NodeRecoveryPoint struct {
	MetadataEpoch    uint64               `json:"metadata_epoch"`
	NodeID           string               `json:"node_id"`
	PlacementDigest  string               `json:"placement_digest"`
	CapacityManifest string               `json:"capacity_manifest,omitempty"`
	Shards           []ShardRecoveryPoint `json:"shards"`
}

type ClusterRecoveryPoint struct {
	Format           int                  `json:"format"`
	MetadataEpoch    uint64               `json:"metadata_epoch"`
	PlacementDigest  string               `json:"placement_digest"`
	CapacityManifest string               `json:"capacity_manifest,omitempty"`
	Nodes            []string             `json:"nodes"`
	ShardRecovery    []ShardRecoveryPoint `json:"shard_recovery"`
}

// CaptureRecoveryPoint returns a stable sequence fence for every locally owned
// shard. The engine lock prevents concurrent mutations during capture.
func (e *Engine) CaptureRecoveryPoint(epoch uint64, nodeID, placementDigest, capacityManifest string) (NodeRecoveryPoint, error) {
	if epoch == 0 || !cluster.ValidNodeID(nodeID) || !validRecoveryDigest(placementDigest) {
		return NodeRecoveryPoint{}, fmt.Errorf("valid epoch, node ID, and placement digest are required")
	}
	if _, err := cluster.ParseCapacityManifest(capacityManifest); err != nil {
		return NodeRecoveryPoint{}, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.captureRecoveryPointLocked(epoch, nodeID, placementDigest, capacityManifest), nil
}

func (e *Engine) captureRecoveryPointLocked(epoch uint64, nodeID, placementDigest, capacityManifest string) NodeRecoveryPoint {
	point := NodeRecoveryPoint{MetadataEpoch: epoch, NodeID: nodeID, PlacementDigest: placementDigest, CapacityManifest: capacityManifest, Shards: []ShardRecoveryPoint{}}
	configs := make([]string, 0, len(e.collections))
	for name := range e.collections {
		configs = append(configs, name)
	}
	sort.Strings(configs)
	for _, name := range configs {
		for shardID := range e.collections[name].Config().ShardCount {
			id := uint32(shardID)
			if !e.shardOwned(name, id) {
				continue
			}
			point.Shards = append(point.Shards, ShardRecoveryPoint{Collection: name, ShardID: id, NodeID: nodeID, Sequence: e.logs[name][id].LastLSN()})
		}
	}
	return point
}

// MergeRecoveryPoints creates a canonical cluster-wide recovery manifest.
func MergeRecoveryPoints(points []NodeRecoveryPoint) (ClusterRecoveryPoint, error) {
	if len(points) == 0 {
		return ClusterRecoveryPoint{}, fmt.Errorf("at least one node recovery point is required")
	}
	manifest := ClusterRecoveryPoint{Format: 1, MetadataEpoch: points[0].MetadataEpoch, PlacementDigest: points[0].PlacementDigest, CapacityManifest: points[0].CapacityManifest, Nodes: make([]string, 0, len(points)), ShardRecovery: []ShardRecoveryPoint{}}
	seenNodes := map[string]struct{}{}
	seenShards := map[string]struct{}{}
	for _, point := range points {
		if point.MetadataEpoch != manifest.MetadataEpoch || point.PlacementDigest != manifest.PlacementDigest || point.CapacityManifest != manifest.CapacityManifest || !cluster.ValidNodeID(point.NodeID) {
			return ClusterRecoveryPoint{}, fmt.Errorf("node recovery points do not share one committed view")
		}
		if _, exists := seenNodes[point.NodeID]; exists {
			return ClusterRecoveryPoint{}, fmt.Errorf("duplicate node recovery point %s", point.NodeID)
		}
		seenNodes[point.NodeID] = struct{}{}
		manifest.Nodes = append(manifest.Nodes, point.NodeID)
		for _, shard := range point.Shards {
			if shard.NodeID != point.NodeID || shard.Collection == "" {
				return ClusterRecoveryPoint{}, fmt.Errorf("invalid shard recovery point")
			}
			key := fmt.Sprintf("%s\x00%d\x00%s", shard.Collection, shard.ShardID, shard.NodeID)
			if _, exists := seenShards[key]; exists {
				return ClusterRecoveryPoint{}, fmt.Errorf("duplicate shard recovery point")
			}
			seenShards[key] = struct{}{}
			manifest.ShardRecovery = append(manifest.ShardRecovery, shard)
		}
	}
	sort.Strings(manifest.Nodes)
	sort.Slice(manifest.ShardRecovery, func(i, j int) bool {
		left, right := manifest.ShardRecovery[i], manifest.ShardRecovery[j]
		if left.Collection != right.Collection {
			return left.Collection < right.Collection
		}
		if left.ShardID != right.ShardID {
			return left.ShardID < right.ShardID
		}
		return left.NodeID < right.NodeID
	})
	return manifest, nil
}

func validRecoveryDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
