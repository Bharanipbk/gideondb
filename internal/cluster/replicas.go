package cluster

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/vectordb/vectordb/internal/core"
)

type ShardReplicaPlacement struct {
	Collection string   `json:"collection"`
	ShardID    uint32   `json:"shard_id"`
	LeaderID   string   `json:"leader_id"`
	Replicas   []string `json:"replicas"`
}

type ReplicaPlacementTable struct {
	Epoch             uint64                  `json:"epoch"`
	Authoritative     bool                    `json:"authoritative"`
	ReplicationFactor int                     `json:"replication_factor"`
	Nodes             []PlacementNode         `json:"nodes"`
	Shards            []ShardReplicaPlacement `json:"shards"`
}

// PlanReplicaPlacement deterministically chooses distinct replica sets. The
// highest rendezvous score is the initial leader; later leader changes belong
// to committed replication metadata rather than health-driven recalculation.
func PlanReplicaPlacement(epoch uint64, localNodeID, localAddress string, peers []Peer, collections []core.CollectionConfig, replicationFactor int) (ReplicaPlacementTable, error) {
	return PlanReplicaPlacementWithCapacity(epoch, localNodeID, localAddress, 1, peers, collections, replicationFactor)
}

func PlanReplicaPlacementWithCapacity(epoch uint64, localNodeID, localAddress string, localCapacity uint32, peers []Peer, collections []core.CollectionConfig, replicationFactor int) (ReplicaPlacementTable, error) {
	base, err := PlanPlacementWithCapacity(epoch, localNodeID, localAddress, localCapacity, peers, collections)
	if err != nil {
		return ReplicaPlacementTable{}, err
	}
	if replicationFactor < 1 || replicationFactor > len(base.Nodes) {
		return ReplicaPlacementTable{}, fmt.Errorf("replication factor must be between 1 and %d", len(base.Nodes))
	}
	configs := append([]core.CollectionConfig(nil), collections...)
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	table := ReplicaPlacementTable{Epoch: epoch, ReplicationFactor: replicationFactor, Nodes: base.Nodes, Shards: make([]ShardReplicaPlacement, 0)}
	type candidate struct {
		nodeID string
		score  [32]byte
	}
	for _, config := range configs {
		for shardID := range config.ShardCount {
			candidates := make([]candidate, 0, len(base.Nodes))
			for _, node := range base.Nodes {
				candidates = append(candidates, candidate{nodeID: node.NodeID, score: placementNodeScore(config.Name, uint32(shardID), node)})
			}
			sort.Slice(candidates, func(i, j int) bool {
				comparison := bytes.Compare(candidates[i].score[:], candidates[j].score[:])
				if comparison == 0 {
					return candidates[i].nodeID < candidates[j].nodeID
				}
				return comparison > 0
			})
			replicas := make([]string, replicationFactor)
			for index := range replicationFactor {
				replicas[index] = candidates[index].nodeID
			}
			table.Shards = append(table.Shards, ShardReplicaPlacement{Collection: config.Name, ShardID: uint32(shardID), LeaderID: replicas[0], Replicas: replicas})
		}
	}
	return table, nil
}
