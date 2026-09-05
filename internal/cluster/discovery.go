package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const MaxPeerSeeds = 256

type PeerState string

const (
	PeerUnknown    PeerState = "unknown"
	PeerHealthy    PeerState = "healthy"
	PeerSuspected  PeerState = "suspected"
	PeerUnhealthy  PeerState = "unhealthy"
	PeerRecovering PeerState = "recovering"
)

type Peer struct {
	SeedURL              string    `json:"seed_url"`
	NodeID               string    `json:"node_id,omitempty"`
	ClusterID            string    `json:"cluster_id,omitempty"`
	AdvertiseAddress     string    `json:"advertise_address,omitempty"`
	MetadataEpoch        uint64    `json:"metadata_epoch,omitempty"`
	ReplicationFactor    int       `json:"replication_factor,omitempty"`
	PlacementCapacity    uint32    `json:"placement_capacity,omitempty"`
	MinProtocolVersion   uint32    `json:"min_protocol_version,omitempty"`
	ProtocolVersion      uint32    `json:"protocol_version,omitempty"`
	NegotiatedProtocol   uint32    `json:"negotiated_protocol,omitempty"`
	MembershipDigest     string    `json:"membership_digest,omitempty"`
	CatalogDigest        string    `json:"catalog_digest,omitempty"`
	PlacementDigest      string    `json:"placement_digest,omitempty"`
	Healthy              bool      `json:"healthy"`
	State                PeerState `json:"state"`
	ConsecutiveFailures  uint32    `json:"consecutive_failures"`
	ConsecutiveSuccesses uint32    `json:"consecutive_successes"`
	LastTransition       time.Time `json:"last_transition,omitempty"`
	LastSeen             time.Time `json:"last_seen,omitempty"`
	LastChecked          time.Time `json:"last_checked"`
	Error                string    `json:"error,omitempty"`
}

type Discovery struct {
	mu          sync.RWMutex
	localNodeID string
	clusterID   string
	apiKey      string
	client      *http.Client
	peers       map[string]Peer
}

func NewDiscovery(localNodeID, clusterID string, seeds []string, apiKey string, client *http.Client) (*Discovery, error) {
	if !ValidNodeID(localNodeID) {
		return nil, fmt.Errorf("valid local node ID is required")
	}
	if !ValidNodeID(clusterID) {
		return nil, fmt.Errorf("valid cluster ID is required")
	}
	if len(seeds) > MaxPeerSeeds {
		return nil, fmt.Errorf("peer seed count exceeds limit of %d", MaxPeerSeeds)
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	discovery := &Discovery{localNodeID: localNodeID, clusterID: clusterID, apiKey: apiKey, client: client, peers: make(map[string]Peer, len(seeds))}
	for _, seed := range seeds {
		normalized, err := normalizeSeed(seed)
		if err != nil {
			return nil, err
		}
		if _, exists := discovery.peers[normalized]; exists {
			return nil, fmt.Errorf("duplicate peer seed %q", normalized)
		}
		discovery.peers[normalized] = Peer{SeedURL: normalized, State: PeerUnknown, LastTransition: time.Now().UTC()}
	}
	return discovery, nil
}

func normalizeSeed(seed string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(seed))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid peer seed %q", seed)
	}
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func (d *Discovery) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	d.Poll(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.Poll(ctx)
		}
	}
}

func (d *Discovery) Poll(ctx context.Context) {
	d.mu.RLock()
	seeds := make([]string, 0, len(d.peers))
	for seed := range d.peers {
		seeds = append(seeds, seed)
	}
	d.mu.RUnlock()
	var wait sync.WaitGroup
	for _, seed := range seeds {
		wait.Add(1)
		go func() { defer wait.Done(); d.pollOne(ctx, seed) }()
	}
	wait.Wait()
	d.reconcileDuplicateNodeIDs()
}

func (d *Discovery) pollOne(ctx context.Context, seed string) {
	checked := time.Now().UTC()
	peer := Peer{SeedURL: seed, LastChecked: checked}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, seed+"/v1/node", nil)
	if err == nil && d.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+d.apiKey)
	}
	if err != nil {
		peer.Error = err.Error()
		d.store(peer)
		return
	}
	response, err := d.client.Do(request)
	if err != nil {
		peer.Error = err.Error()
		d.store(peer)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		peer.Error = fmt.Sprintf("node endpoint returned HTTP %d", response.StatusCode)
		d.store(peer)
		return
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	var body struct {
		NodeID                    string    `json:"node_id"`
		ClusterID                 string    `json:"cluster_id"`
		AdvertiseAddress          string    `json:"advertise_address"`
		StartedAt                 time.Time `json:"started_at"`
		Mode                      string    `json:"mode"`
		MetadataEpoch             uint64    `json:"metadata_epoch"`
		ReplicationFactor         int       `json:"replication_factor"`
		PlacementCapacity         uint32    `json:"placement_capacity"`
		MinProtocolVersion        uint32    `json:"min_protocol_version"`
		ProtocolVersion           uint32    `json:"protocol_version"`
		MembershipDigest          string    `json:"membership_digest"`
		CatalogDigest             string    `json:"catalog_digest"`
		PlacementDigest           string    `json:"placement_digest"`
		RaftRole                  string    `json:"raft_role"`
		RaftLeaderID              string    `json:"raft_leader_id"`
		RaftTerm                  uint64    `json:"raft_term"`
		RaftVoters                []string  `json:"raft_voters"`
		RaftJointOldVoters        []string  `json:"raft_joint_old_voters"`
		RaftJointNewVoters        []string  `json:"raft_joint_new_voters"`
		CommittedMembershipDigest string    `json:"committed_membership_digest"`
		CommittedCatalogDigest    string    `json:"committed_catalog_digest"`
		CommittedPlacementDigest  string    `json:"committed_placement_digest"`
	}
	if err := decoder.Decode(&body); err != nil {
		peer.Error = "decode node response: " + err.Error()
		d.store(peer)
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		peer.Error = "node response must contain one JSON value"
		d.store(peer)
		return
	}
	if !ValidNodeID(body.NodeID) || body.AdvertiseAddress == "" {
		peer.Error = "node response has invalid identity or missing advertised address"
		d.storeTerminal(peer)
		return
	}
	if body.ClusterID != d.clusterID {
		peer.Error = "peer belongs to a different cluster"
		d.storeTerminal(peer)
		return
	}
	if body.NodeID == d.localNodeID {
		// A shared DNS seed list is useful for orchestrators where every pod gets
		// the same configuration. Silently prune the entry that resolves back to
		// this durable node identity; it is not a remote health dependency.
		d.mu.Lock()
		delete(d.peers, seed)
		d.mu.Unlock()
		return
	}
	peer.NodeID, peer.ClusterID, peer.AdvertiseAddress, peer.Healthy, peer.LastSeen = body.NodeID, body.ClusterID, body.AdvertiseAddress, true, checked
	if body.PlacementCapacity == 0 {
		body.PlacementCapacity = 1
	}
	if body.PlacementCapacity > MaxPlacementCapacity {
		peer.Error = "node response has invalid placement capacity"
		d.storeTerminal(peer)
		return
	}
	if body.ProtocolVersion == 0 {
		body.MinProtocolVersion, body.ProtocolVersion = 1, 1
	} else if body.MinProtocolVersion == 0 {
		body.MinProtocolVersion = body.ProtocolVersion
	}
	negotiated, err := NegotiateProtocol(MinClusterProtocolVersion, ClusterProtocolVersion, body.MinProtocolVersion, body.ProtocolVersion)
	if err != nil {
		peer.Error = "incompatible cluster protocol: " + err.Error()
		d.storeTerminal(peer)
		return
	}
	peer.MetadataEpoch, peer.ReplicationFactor, peer.PlacementCapacity, peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = body.MetadataEpoch, body.ReplicationFactor, body.PlacementCapacity, body.MembershipDigest, body.CatalogDigest, body.PlacementDigest
	peer.MinProtocolVersion, peer.ProtocolVersion, peer.NegotiatedProtocol = body.MinProtocolVersion, body.ProtocolVersion, negotiated
	d.store(peer)
}

func (d *Discovery) store(peer Peer) {
	d.mu.Lock()
	previous := d.peers[peer.SeedURL]
	if !peer.Healthy {
		peer.NodeID, peer.ClusterID, peer.AdvertiseAddress, peer.LastSeen = previous.NodeID, previous.ClusterID, previous.AdvertiseAddress, previous.LastSeen
		peer.MetadataEpoch, peer.ReplicationFactor, peer.PlacementCapacity, peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = previous.MetadataEpoch, previous.ReplicationFactor, previous.PlacementCapacity, previous.MembershipDigest, previous.CatalogDigest, previous.PlacementDigest
		peer.MinProtocolVersion, peer.ProtocolVersion, peer.NegotiatedProtocol = previous.MinProtocolVersion, previous.ProtocolVersion, previous.NegotiatedProtocol
		peer.ConsecutiveFailures = min(previous.ConsecutiveFailures+1, uint32(3))
		peer.ConsecutiveSuccesses = 0
		if previous.NodeID == "" {
			peer.State = PeerUnknown
		} else if peer.ConsecutiveFailures >= 3 {
			peer.State = PeerUnhealthy
		} else {
			peer.State = PeerSuspected
		}
	} else {
		peer.ConsecutiveFailures = 0
		peer.ConsecutiveSuccesses = min(previous.ConsecutiveSuccesses+1, uint32(2))
		if previous.State == PeerSuspected || previous.State == PeerUnhealthy || previous.State == PeerRecovering {
			if peer.ConsecutiveSuccesses < 2 {
				peer.State, peer.Healthy = PeerRecovering, false
			} else {
				peer.State = PeerHealthy
			}
		} else {
			peer.State = PeerHealthy
		}
	}
	peer.LastTransition = previous.LastTransition
	if peer.State != previous.State {
		peer.LastTransition = peer.LastChecked
	}
	d.peers[peer.SeedURL] = peer
	d.mu.Unlock()
}

func (d *Discovery) storeTerminal(peer Peer) {
	d.mu.Lock()
	previous := d.peers[peer.SeedURL]
	peer.NodeID, peer.ClusterID, peer.AdvertiseAddress, peer.LastSeen = previous.NodeID, previous.ClusterID, previous.AdvertiseAddress, previous.LastSeen
	peer.MetadataEpoch, peer.ReplicationFactor, peer.PlacementCapacity, peer.MembershipDigest, peer.CatalogDigest, peer.PlacementDigest = previous.MetadataEpoch, previous.ReplicationFactor, previous.PlacementCapacity, previous.MembershipDigest, previous.CatalogDigest, previous.PlacementDigest
	peer.MinProtocolVersion, peer.ProtocolVersion, peer.NegotiatedProtocol = previous.MinProtocolVersion, previous.ProtocolVersion, previous.NegotiatedProtocol
	peer.Healthy, peer.State = false, PeerUnhealthy
	peer.ConsecutiveFailures, peer.ConsecutiveSuccesses = min(previous.ConsecutiveFailures+1, uint32(3)), 0
	peer.LastTransition = previous.LastTransition
	if previous.State != PeerUnhealthy {
		peer.LastTransition = peer.LastChecked
	}
	d.peers[peer.SeedURL] = peer
	d.mu.Unlock()
}

func (d *Discovery) reconcileDuplicateNodeIDs() {
	d.mu.Lock()
	defer d.mu.Unlock()
	byID := make(map[string][]string)
	for seed, peer := range d.peers {
		if peer.Error == "" && peer.NodeID != "" {
			byID[peer.NodeID] = append(byID[peer.NodeID], seed)
		}
	}
	for nodeID, seeds := range byID {
		if len(seeds) < 2 {
			continue
		}
		sort.Strings(seeds)
		for _, seed := range seeds {
			peer := d.peers[seed]
			previousState := peer.State
			peer.Healthy = false
			peer.State = PeerUnhealthy
			peer.ConsecutiveFailures, peer.ConsecutiveSuccesses = 3, 0
			if peer.LastTransition.IsZero() || previousState != PeerUnhealthy {
				peer.LastTransition = peer.LastChecked
			}
			peer.Error = fmt.Sprintf("duplicate node ID %s reported by %d peer seeds", nodeID, len(seeds))
			d.peers[seed] = peer
		}
	}
}

func (d *Discovery) Peers() []Peer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]Peer, 0, len(d.peers))
	for _, peer := range d.peers {
		result = append(result, peer)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SeedURL < result[j].SeedURL })
	return result
}
