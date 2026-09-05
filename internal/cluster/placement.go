package cluster

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"

	"github.com/vectordb/vectordb/internal/core"
)

const MaxPlacementCapacity uint32 = 256

// PlacementNode is a stable candidate in an observational placement table.
type PlacementNode struct {
	NodeID           string `json:"node_id"`
	AdvertiseAddress string `json:"advertise_address"`
	Capacity         uint32 `json:"capacity"`
	Healthy          bool   `json:"healthy"`
	Local            bool   `json:"local"`
}

type ShardPlacement struct {
	Collection string `json:"collection"`
	ShardID    uint32 `json:"shard_id"`
	NodeID     string `json:"node_id"`
}

// PlacementTable is deterministic but non-authoritative until its epoch is
// committed by cluster metadata consensus.
type PlacementTable struct {
	Epoch         uint64           `json:"epoch"`
	Authoritative bool             `json:"authoritative"`
	Nodes         []PlacementNode  `json:"nodes"`
	Shards        []ShardPlacement `json:"shards"`
}

// PlanPlacement assigns every logical shard to one node using rendezvous
// hashing. Previously discovered unhealthy nodes remain candidates, preventing
// a transient health check from silently changing ownership.
func PlanPlacement(epoch uint64, localNodeID, localAddress string, peers []Peer, collections []core.CollectionConfig) (PlacementTable, error) {
	return PlanPlacementWithCapacity(epoch, localNodeID, localAddress, 1, peers, collections)
}

// PlanPlacementWithCapacity uses deterministic virtual rendezvous tickets to
// bias ownership toward nodes with more configured resources. Capacity is an
// integer relative weight; omitted peer capacity defaults to one.
func PlanPlacementWithCapacity(epoch uint64, localNodeID, localAddress string, localCapacity uint32, peers []Peer, collections []core.CollectionConfig) (PlacementTable, error) {
	if epoch == 0 || !ValidNodeID(localNodeID) || localAddress == "" {
		return PlacementTable{}, fmt.Errorf("valid epoch, local node ID, and local address are required")
	}
	if err := validatePlacementCapacity(localCapacity); err != nil {
		return PlacementTable{}, fmt.Errorf("local node: %w", err)
	}
	nodes := []PlacementNode{{NodeID: localNodeID, AdvertiseAddress: localAddress, Capacity: localCapacity, Healthy: true, Local: true}}
	seen := map[string]struct{}{localNodeID: {}}
	for _, peer := range peers {
		if peer.NodeID == "" {
			continue
		}
		if !ValidNodeID(peer.NodeID) || peer.AdvertiseAddress == "" {
			return PlacementTable{}, fmt.Errorf("invalid discovered peer %q", peer.SeedURL)
		}
		if _, exists := seen[peer.NodeID]; exists {
			return PlacementTable{}, fmt.Errorf("duplicate placement node ID %s", peer.NodeID)
		}
		seen[peer.NodeID] = struct{}{}
		capacity := peer.PlacementCapacity
		if capacity == 0 {
			capacity = 1
		}
		if err := validatePlacementCapacity(capacity); err != nil {
			return PlacementTable{}, fmt.Errorf("peer %s: %w", peer.NodeID, err)
		}
		nodes = append(nodes, PlacementNode{NodeID: peer.NodeID, AdvertiseAddress: peer.AdvertiseAddress, Capacity: capacity, Healthy: peer.Healthy})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	configs := append([]core.CollectionConfig(nil), collections...)
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	table := PlacementTable{Epoch: epoch, Nodes: nodes, Shards: make([]ShardPlacement, 0)}
	for _, config := range configs {
		if err := config.Validate(); err != nil {
			return PlacementTable{}, err
		}
		for shardID := range config.ShardCount {
			owner := nodes[0].NodeID
			best := placementNodeScore(config.Name, uint32(shardID), nodes[0])
			for _, node := range nodes[1:] {
				score := placementNodeScore(config.Name, uint32(shardID), node)
				if bytes.Compare(score[:], best[:]) > 0 {
					best, owner = score, node.NodeID
				}
			}
			table.Shards = append(table.Shards, ShardPlacement{Collection: config.Name, ShardID: uint32(shardID), NodeID: owner})
		}
	}
	return table, nil
}

func validatePlacementCapacity(capacity uint32) error {
	if capacity < 1 || capacity > MaxPlacementCapacity {
		return fmt.Errorf("placement capacity must be between 1 and %d", MaxPlacementCapacity)
	}
	return nil
}

func placementNodeScore(collection string, shardID uint32, node PlacementNode) [32]byte {
	best := placementScore(collection, shardID, node.NodeID)
	for ticket := uint32(1); ticket < node.Capacity; ticket++ {
		score := placementTicketScore(collection, shardID, node.NodeID, ticket)
		if bytes.Compare(score[:], best[:]) > 0 {
			best = score
		}
	}
	return best
}

func placementScore(collection string, shardID uint32, nodeID string) [32]byte {
	payload := make([]byte, 0, len(collection)+1+4+len(nodeID))
	payload = append(payload, collection...)
	payload = append(payload, 0)
	var shard [4]byte
	binary.BigEndian.PutUint32(shard[:], shardID)
	payload = append(payload, shard[:]...)
	payload = append(payload, nodeID...)
	return sha256.Sum256(payload)
}

func placementTicketScore(collection string, shardID uint32, nodeID string, ticket uint32) [32]byte {
	payload := make([]byte, 0, len(collection)+1+4+len(nodeID)+4)
	payload = append(payload, collection...)
	payload = append(payload, 0)
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], shardID)
	payload = append(payload, encoded[:]...)
	payload = append(payload, nodeID...)
	binary.BigEndian.PutUint32(encoded[:], ticket)
	payload = append(payload, encoded[:]...)
	return sha256.Sum256(payload)
}
