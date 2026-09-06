package cluster

import (
	"fmt"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
)

// PlanCapacityRebalance constructs a deterministic same-membership placement
// transition for one node capacity change.
func PlanCapacityRebalance(epoch uint64, localNodeID, localAddress string, localCapacity uint32, peers []Peer, collections []core.CollectionConfig, replicationFactor int, targetNodeID string, targetCapacity uint32) (ReplicaPlacementTable, ReplicaPlacementTable, RebalancePlan, error) {
	if !ValidNodeID(targetNodeID) {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, fmt.Errorf("valid capacity target node ID is required")
	}
	if err := validatePlacementCapacity(targetCapacity); err != nil {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, err
	}
	current, err := PlanReplicaPlacementWithCapacity(epoch, localNodeID, localAddress, localCapacity, peers, collections, replicationFactor)
	if err != nil {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, err
	}
	targetLocalCapacity := localCapacity
	targetPeers := append([]Peer(nil), peers...)
	found := targetNodeID == localNodeID
	if found {
		if targetCapacity == localCapacity {
			return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, fmt.Errorf("target capacity must differ from current capacity")
		}
		targetLocalCapacity = targetCapacity
	} else {
		for index := range targetPeers {
			if targetPeers[index].NodeID != targetNodeID {
				continue
			}
			found = true
			currentCapacity := targetPeers[index].PlacementCapacity
			if currentCapacity == 0 {
				currentCapacity = 1
			}
			if targetCapacity == currentCapacity {
				return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, fmt.Errorf("target capacity must differ from current capacity")
			}
			targetPeers[index].PlacementCapacity = targetCapacity
			break
		}
	}
	if !found {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, fmt.Errorf("capacity target node is not a cluster member")
	}
	target, err := PlanReplicaPlacementWithCapacity(epoch+1, localNodeID, localAddress, targetLocalCapacity, targetPeers, collections, replicationFactor)
	if err != nil {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, err
	}
	plan, err := PlanRebalance(current, target)
	if err != nil {
		return ReplicaPlacementTable{}, ReplicaPlacementTable{}, RebalancePlan{}, err
	}
	return current, target, plan, nil
}

type RebalanceAction struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	SourceID string   `json:"source_id,omitempty"`
	TargetID string   `json:"target_id,omitempty"`
	Requires []string `json:"requires,omitempty"`
}

type ShardMovement struct {
	Collection     string            `json:"collection"`
	ShardID        uint32            `json:"shard_id"`
	SourceLeaderID string            `json:"source_leader_id"`
	TargetLeaderID string            `json:"target_leader_id"`
	SourceReplicas []string          `json:"source_replicas"`
	TargetReplicas []string          `json:"target_replicas"`
	Actions        []RebalanceAction `json:"actions"`
}

type RebalancePlan struct {
	FromEpoch     uint64          `json:"from_epoch"`
	ToEpoch       uint64          `json:"to_epoch"`
	Authoritative bool            `json:"authoritative"`
	Movements     []ShardMovement `json:"movements"`
}

// PlanRebalance produces a deterministic, non-executing transition plan. New
// replicas must be copied and caught up before leadership or placement cutover;
// removals are always last.
func PlanRebalance(current, target ReplicaPlacementTable) (RebalancePlan, error) {
	if current.Epoch == 0 || target.Epoch <= current.Epoch {
		return RebalancePlan{}, fmt.Errorf("target epoch must be newer than current epoch")
	}
	if current.ReplicationFactor != target.ReplicationFactor {
		return RebalancePlan{}, fmt.Errorf("replication factor changes require a separate transition")
	}
	currentByShard := make(map[string]ShardReplicaPlacement, len(current.Shards))
	for _, shard := range current.Shards {
		key := shardKey(shard.Collection, shard.ShardID)
		if _, exists := currentByShard[key]; exists {
			return RebalancePlan{}, fmt.Errorf("current placement contains duplicate shard %s/%d", shard.Collection, shard.ShardID)
		}
		currentByShard[key] = shard
	}
	targets := append([]ShardReplicaPlacement(nil), target.Shards...)
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].Collection < targets[j].Collection || (targets[i].Collection == targets[j].Collection && targets[i].ShardID < targets[j].ShardID)
	})
	plan := RebalancePlan{FromEpoch: current.Epoch, ToEpoch: target.Epoch, Movements: make([]ShardMovement, 0)}
	for _, destination := range targets {
		source, exists := currentByShard[shardKey(destination.Collection, destination.ShardID)]
		if !exists {
			return RebalancePlan{}, fmt.Errorf("target contains unknown shard %s/%d", destination.Collection, destination.ShardID)
		}
		if equalReplicaIDs(source.Replicas, destination.Replicas) && source.LeaderID == destination.LeaderID {
			delete(currentByShard, shardKey(destination.Collection, destination.ShardID))
			continue
		}
		movement := ShardMovement{Collection: destination.Collection, ShardID: destination.ShardID, SourceLeaderID: source.LeaderID, TargetLeaderID: destination.LeaderID, SourceReplicas: append([]string(nil), source.Replicas...), TargetReplicas: append([]string(nil), destination.Replicas...)}
		base := fmt.Sprintf("%s/%d/", destination.Collection, destination.ShardID)
		currentSet, targetSet := stringSet(source.Replicas), stringSet(destination.Replicas)
		var caughtUp []string
		for _, nodeID := range destination.Replicas {
			if _, exists := currentSet[nodeID]; exists {
				continue
			}
			copyID := base + "copy/" + nodeID
			catchUpID := base + "catch-up/" + nodeID
			movement.Actions = append(movement.Actions,
				RebalanceAction{ID: copyID, Type: "copy_snapshot", SourceID: source.LeaderID, TargetID: nodeID},
				RebalanceAction{ID: catchUpID, Type: "catch_up_wal", SourceID: source.LeaderID, TargetID: nodeID, Requires: []string{copyID}},
			)
			caughtUp = append(caughtUp, catchUpID)
		}
		dependencies := append([]string(nil), caughtUp...)
		if source.LeaderID != destination.LeaderID {
			transferID := base + "transfer-leader/" + destination.LeaderID
			movement.Actions = append(movement.Actions, RebalanceAction{ID: transferID, Type: "transfer_leadership", SourceID: source.LeaderID, TargetID: destination.LeaderID, Requires: append([]string(nil), caughtUp...)})
			dependencies = append(dependencies, transferID)
		}
		commitID := base + "commit-placement"
		movement.Actions = append(movement.Actions, RebalanceAction{ID: commitID, Type: "commit_placement", Requires: dependencies})
		for _, nodeID := range source.Replicas {
			if _, exists := targetSet[nodeID]; !exists {
				movement.Actions = append(movement.Actions, RebalanceAction{ID: base + "remove/" + nodeID, Type: "remove_replica", TargetID: nodeID, Requires: []string{commitID}})
			}
		}
		plan.Movements = append(plan.Movements, movement)
		delete(currentByShard, shardKey(destination.Collection, destination.ShardID))
	}
	if len(currentByShard) != 0 {
		return RebalancePlan{}, fmt.Errorf("target omits one or more current shards")
	}
	return plan, nil
}

func shardKey(collection string, shardID uint32) string {
	return fmt.Sprintf("%s\x00%d", collection, shardID)
}
func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
func equalReplicaIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
