package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
)

type ViewDigests struct {
	Membership       string `json:"membership_digest"`
	Catalog          string `json:"catalog_digest"`
	Placement        string `json:"placement_digest"`
	CapacityManifest string `json:"capacity_manifest,omitempty"`
}

type NodeCapacity struct {
	NodeID   string `json:"node_id"`
	Capacity uint32 `json:"capacity"`
}

// ComputeViewDigests produces canonical fingerprints for convergence checks.
// Peer health is intentionally excluded so transient probes cannot alter ownership.
func ComputeViewDigests(epoch uint64, localNodeID, localAddress string, peers []Peer, collections []core.CollectionConfig) (ViewDigests, error) {
	return ComputeViewDigestsWithCapacity(epoch, localNodeID, localAddress, 1, peers, collections)
}

func ComputeViewDigestsWithCapacity(epoch uint64, localNodeID, localAddress string, localCapacity uint32, peers []Peer, collections []core.CollectionConfig) (ViewDigests, error) {
	nodeIDs := []string{localNodeID}
	seen := map[string]struct{}{localNodeID: {}}
	for _, peer := range peers {
		if peer.NodeID == "" {
			continue
		}
		if _, exists := seen[peer.NodeID]; exists {
			return ViewDigests{}, fmt.Errorf("duplicate node ID %s", peer.NodeID)
		}
		seen[peer.NodeID] = struct{}{}
		nodeIDs = append(nodeIDs, peer.NodeID)
	}
	sort.Strings(nodeIDs)
	configs := append([]core.CollectionConfig(nil), collections...)
	for position := range configs {
		configs[position] = configs[position].Normalized()
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	table, err := PlanPlacementWithCapacity(epoch, localNodeID, localAddress, localCapacity, peers, configs)
	if err != nil {
		return ViewDigests{}, err
	}
	membership, err := digestJSON(nodeIDs)
	if err != nil {
		return ViewDigests{}, err
	}
	catalog, err := digestJSON(configs)
	if err != nil {
		return ViewDigests{}, err
	}
	placement, err := digestJSON(table.Shards)
	if err != nil {
		return ViewDigests{}, err
	}
	capacities := make([]NodeCapacity, 0, len(table.Nodes))
	for _, node := range table.Nodes {
		capacities = append(capacities, NodeCapacity{NodeID: node.NodeID, Capacity: node.Capacity})
	}
	manifest, err := json.Marshal(capacities)
	if err != nil {
		return ViewDigests{}, err
	}
	return ViewDigests{Membership: membership, Catalog: catalog, Placement: placement, CapacityManifest: string(manifest)}, nil
}

func ParseCapacityManifest(manifest string) ([]NodeCapacity, error) {
	if manifest == "" {
		return nil, nil
	}
	var capacities []NodeCapacity
	if err := json.Unmarshal([]byte(manifest), &capacities); err != nil {
		return nil, fmt.Errorf("decode capacity manifest: %w", err)
	}
	for index, item := range capacities {
		if !ValidNodeID(item.NodeID) || validatePlacementCapacity(item.Capacity) != nil || (index > 0 && capacities[index-1].NodeID >= item.NodeID) {
			return nil, fmt.Errorf("invalid capacity manifest")
		}
	}
	canonical, _ := json.Marshal(capacities)
	if string(canonical) != manifest {
		return nil, fmt.Errorf("capacity manifest is not canonical")
	}
	return capacities, nil
}

func digestJSON(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
