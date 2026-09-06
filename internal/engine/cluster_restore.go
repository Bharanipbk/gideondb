package engine

import (
	"fmt"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/cluster"
)

type ShardRestoreAssignment struct {
	Collection     string   `json:"collection"`
	ShardID        uint32   `json:"shard_id"`
	SourceNodeID   string   `json:"source_node_id"`
	SourceSequence uint64   `json:"source_sequence"`
	TargetLeaderID string   `json:"target_leader_id"`
	TargetReplicas []string `json:"target_replicas"`
}

type ClusterRestorePlan struct {
	BackupEpoch           uint64                   `json:"backup_epoch"`
	BackupPlacementDigest string                   `json:"backup_placement_digest"`
	CapacityManifest      string                   `json:"capacity_manifest,omitempty"`
	TargetEpoch           uint64                   `json:"target_epoch"`
	Assignments           []ShardRestoreAssignment `json:"assignments"`
}

// PlanClusterRestore selects one canonical source replica for every shard in a
// target placement. Selection prefers the greatest durable sequence and then
// the lexicographically smallest source node ID.
func PlanClusterRestore(manifest ClusterRecoveryPoint, target cluster.ReplicaPlacementTable) (ClusterRestorePlan, error) {
	if err := validateClusterRecoveryManifest(manifest); err != nil {
		return ClusterRestorePlan{}, err
	}
	if target.Epoch == 0 || target.ReplicationFactor < 1 {
		return ClusterRestorePlan{}, fmt.Errorf("valid target placement is required")
	}
	sources := make(map[string][]ShardRecoveryPoint)
	for _, shard := range manifest.ShardRecovery {
		key := fmt.Sprintf("%s\x00%d", shard.Collection, shard.ShardID)
		sources[key] = append(sources[key], shard)
	}
	targets := append([]cluster.ShardReplicaPlacement(nil), target.Shards...)
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Collection < targets[j].Collection || (targets[i].Collection == targets[j].Collection && targets[i].ShardID < targets[j].ShardID)
	})
	plan := ClusterRestorePlan{
		BackupEpoch:           manifest.MetadataEpoch,
		BackupPlacementDigest: manifest.PlacementDigest,
		CapacityManifest:      manifest.CapacityManifest,
		TargetEpoch:           target.Epoch,
		Assignments:           make([]ShardRestoreAssignment, 0, len(targets)),
	}
	seen := map[string]struct{}{}
	for _, destination := range targets {
		key := fmt.Sprintf("%s\x00%d", destination.Collection, destination.ShardID)
		if _, duplicate := seen[key]; duplicate {
			return ClusterRestorePlan{}, fmt.Errorf("target placement contains duplicate shard %s/%d", destination.Collection, destination.ShardID)
		}
		seen[key] = struct{}{}
		candidates := sources[key]
		if len(candidates) == 0 {
			return ClusterRestorePlan{}, fmt.Errorf("backup has no replica for target shard %s/%d", destination.Collection, destination.ShardID)
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].Sequence != candidates[j].Sequence {
				return candidates[i].Sequence > candidates[j].Sequence
			}
			return candidates[i].NodeID < candidates[j].NodeID
		})
		if destination.LeaderID == "" || len(destination.Replicas) != target.ReplicationFactor || destination.Replicas[0] != destination.LeaderID {
			return ClusterRestorePlan{}, fmt.Errorf("invalid target replica set for %s/%d", destination.Collection, destination.ShardID)
		}
		replicaSet := map[string]struct{}{}
		for _, nodeID := range destination.Replicas {
			if !cluster.ValidNodeID(nodeID) {
				return ClusterRestorePlan{}, fmt.Errorf("invalid target replica identity for %s/%d", destination.Collection, destination.ShardID)
			}
			if _, duplicate := replicaSet[nodeID]; duplicate {
				return ClusterRestorePlan{}, fmt.Errorf("duplicate target replica for %s/%d", destination.Collection, destination.ShardID)
			}
			replicaSet[nodeID] = struct{}{}
		}
		selected := candidates[0]
		plan.Assignments = append(plan.Assignments, ShardRestoreAssignment{Collection: destination.Collection, ShardID: destination.ShardID, SourceNodeID: selected.NodeID, SourceSequence: selected.Sequence, TargetLeaderID: destination.LeaderID, TargetReplicas: append([]string(nil), destination.Replicas...)})
	}
	return plan, nil
}
