package cluster

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync"
	"time"
)

var ErrNotRaftLeader = fmt.Errorf("not metadata Raft leader")

type RaftRole string

const (
	RaftFollower  RaftRole = "follower"
	RaftCandidate RaftRole = "candidate"
	RaftLeader    RaftRole = "leader"
)

type RaftPeer struct {
	NodeID  string
	BaseURL string
}

type RaftTransport interface {
	RequestVote(context.Context, RaftPeer, RequestVoteRequest) (RequestVoteResponse, error)
	AppendEntries(context.Context, RaftPeer, AppendEntriesRequest) (AppendEntriesResponse, error)
	InstallSnapshot(context.Context, RaftPeer, InstallSnapshotRequest) (InstallSnapshotResponse, error)
}

type TimeoutNowRequest struct {
	Term     uint64 `json:"term"`
	LeaderID string `json:"leader_id"`
}

type TimeoutNowResponse struct {
	Term     uint64 `json:"term"`
	Accepted bool   `json:"accepted"`
}

type leadershipTransferTransport interface {
	TimeoutNow(context.Context, RaftPeer, TimeoutNowRequest) (TimeoutNowResponse, error)
}

type RaftRuntimeConfig struct {
	ElectionMin       time.Duration
	ElectionMax       time.Duration
	Heartbeat         time.Duration
	SnapshotThreshold uint64
}

type RaftRuntime struct {
	mu         sync.RWMutex
	store      *RaftStore
	nodeID     string
	peers      func() []RaftPeer
	transport  RaftTransport
	config     RaftRuntimeConfig
	role       RaftRole
	leaderID   string
	reset      chan struct{}
	proposalMu sync.Mutex
	campaignMu sync.Mutex
	nextIndex  map[string]uint64
	lastQuorum time.Time
}

func NewRaftRuntime(store *RaftStore, nodeID string, peers func() []RaftPeer, transport RaftTransport, config RaftRuntimeConfig) (*RaftRuntime, error) {
	if store == nil || !ValidNodeID(nodeID) || peers == nil || transport == nil {
		return nil, fmt.Errorf("Raft runtime requires store, node, peers, and transport")
	}
	if config.ElectionMin == 0 {
		config.ElectionMin = 750 * time.Millisecond
	}
	if config.ElectionMax == 0 {
		config.ElectionMax = 1500 * time.Millisecond
	}
	if config.Heartbeat == 0 {
		config.Heartbeat = 200 * time.Millisecond
	}
	if config.SnapshotThreshold == 0 {
		config.SnapshotThreshold = 256
	}
	if config.ElectionMin <= config.Heartbeat || config.ElectionMax < config.ElectionMin {
		return nil, fmt.Errorf("invalid Raft timing configuration")
	}
	return &RaftRuntime{store: store, nodeID: nodeID, peers: peers, transport: transport, config: config, role: RaftFollower, reset: make(chan struct{}, 1), nextIndex: make(map[string]uint64)}, nil
}

func (r *RaftRuntime) Status() (RaftRole, string, uint64) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	term, _, _ := r.store.State()
	return r.role, r.leaderID, term
}

// TransferLeadership catches up a deterministic voter and asks it to campaign
// immediately. It is safe to call on followers and single-voter clusters.
func (r *RaftRuntime) TransferLeadership(ctx context.Context) error {
	role, _, term := r.Status()
	if role != RaftLeader {
		return nil
	}
	transport, ok := r.transport.(leadershipTransferTransport)
	if !ok {
		return fmt.Errorf("Raft transport does not support leadership transfer")
	}
	peers, ready := r.consensusPeers()
	if !ready {
		return fmt.Errorf("metadata voter identities are not fully discovered")
	}
	if len(peers) == 0 {
		return nil
	}
	_, _, lastIndex, _, _ := r.store.LogSnapshot()
	for _, peer := range peers {
		if !r.replicatePeer(ctx, peer, term, lastIndex) {
			continue
		}
		response, err := transport.TimeoutNow(ctx, peer, TimeoutNowRequest{Term: term, LeaderID: r.nodeID})
		if err != nil {
			continue
		}
		if response.Term > term {
			_ = r.store.ObserveTerm(response.Term)
		}
		if response.Accepted {
			r.becomeFollower(peer.NodeID)
			return nil
		}
		if currentRole, _, _ := r.Status(); currentRole != RaftLeader {
			// The forced campaign advanced the term and safely displaced this
			// leader even if a simultaneous election won the replacement race.
			return nil
		}
	}
	return fmt.Errorf("no caught-up voter accepted leadership transfer")
}

// TimeoutNow handles a fenced transfer request from the current leader.
func (r *RaftRuntime) TimeoutNow(ctx context.Context, request TimeoutNowRequest) (TimeoutNowResponse, error) {
	role, leaderID, term := r.Status()
	if request.Term != term || request.LeaderID != leaderID || role != RaftFollower || !ValidNodeID(request.LeaderID) {
		return TimeoutNowResponse{Term: term}, fmt.Errorf("leadership transfer is not from the current leader and term")
	}
	r.campaign(ctx)
	newRole, _, newTerm := r.Status()
	return TimeoutNowResponse{Term: newTerm, Accepted: newRole == RaftLeader}, nil
}

func (r *RaftRuntime) Run(ctx context.Context) {
	timer := time.NewTimer(r.electionTimeout())
	heartbeat := time.NewTicker(r.config.Heartbeat)
	defer timer.Stop()
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.reset:
			resetTimer(timer, r.electionTimeout())
		case <-timer.C:
			if !r.isLeader() {
				r.campaign(ctx)
			}
			resetTimer(timer, r.electionTimeout())
		case <-heartbeat.C:
			if r.isLeader() {
				r.broadcastHeartbeat(ctx)
			}
		}
	}
}

func (r *RaftRuntime) RequestVote(request RequestVoteRequest) (RequestVoteResponse, error) {
	if _, ready := r.consensusPeers(); !ready {
		term, _, _ := r.store.State()
		return RequestVoteResponse{Term: term}, nil
	}
	previousTerm, _, _ := r.store.State()
	response, err := r.store.RequestVote(request)
	if err == nil && request.Term > previousTerm {
		r.becomeFollower("")
	}
	if err == nil && response.VoteGranted {
		r.signalReset()
	}
	return response, err
}

func (r *RaftRuntime) AppendEntries(request AppendEntriesRequest) (AppendEntriesResponse, error) {
	if len(r.store.ActiveVoters()) != 0 {
		if _, ready := r.consensusPeers(); !ready {
			term, _, _ := r.store.State()
			return AppendEntriesResponse{Term: term}, nil
		}
	}
	response, err := r.store.AppendEntries(request)
	if err == nil && request.Term == response.Term {
		r.becomeFollower(request.LeaderID)
		r.signalReset()
	}
	return response, err
}

func (r *RaftRuntime) InstallSnapshot(request InstallSnapshotRequest) (InstallSnapshotResponse, error) {
	if _, ready := r.consensusPeers(); !ready {
		term, _, _ := r.store.State()
		return InstallSnapshotResponse{Term: term}, nil
	}
	response, err := r.store.InstallSnapshot(request)
	if err == nil && request.Term == response.Term {
		r.becomeFollower(request.LeaderID)
		r.signalReset()
	}
	return response, err
}

func (r *RaftRuntime) campaign(ctx context.Context) {
	r.campaignMu.Lock()
	defer r.campaignMu.Unlock()
	if r.isLeader() {
		return
	}
	r.mu.Lock()
	r.role, r.leaderID = RaftCandidate, ""
	r.mu.Unlock()
	request, err := r.store.BeginElection()
	if err != nil {
		r.becomeFollower("")
		return
	}
	peers, ready := r.consensusPeers()
	if !ready {
		r.becomeFollower("")
		return
	}
	votes := []string{r.nodeID}
	for _, peer := range peers {
		callCtx, cancel := context.WithTimeout(ctx, r.config.Heartbeat)
		response, callErr := r.transport.RequestVote(callCtx, peer, request)
		cancel()
		if callErr != nil {
			continue
		}
		if response.Term > request.Term {
			_ = r.store.ObserveTerm(response.Term)
			r.becomeFollower("")
			return
		}
		if response.VoteGranted {
			votes = append(votes, peer.NodeID)
		}
	}
	if r.store.HasVoterQuorum(votes) {
		currentTerm, _, lastIndex, _, _ := r.store.LogSnapshot()
		r.mu.Lock()
		if currentTerm != request.Term || r.role != RaftCandidate {
			r.mu.Unlock()
			return
		}
		r.role, r.leaderID = RaftLeader, r.nodeID
		r.lastQuorum = time.Now()
		r.nextIndex = make(map[string]uint64, len(peers))
		for _, peer := range peers {
			r.nextIndex[peer.NodeID] = lastIndex + 1
		}
		r.mu.Unlock()
		r.broadcastHeartbeat(ctx)
	}
}

func (r *RaftRuntime) broadcastHeartbeat(ctx context.Context) {
	term, _, lastIndex, _, _ := r.store.LogSnapshot()
	peers, ready := r.consensusPeers()
	if !ready {
		r.expireLeaderWithoutQuorum()
		return
	}
	acknowledgements := []string{r.nodeID}
	for _, peer := range peers {
		if r.replicatePeer(ctx, peer, term, lastIndex) {
			acknowledgements = append(acknowledgements, peer.NodeID)
		}
	}
	if r.store.HasVoterQuorum(acknowledgements) {
		r.mu.Lock()
		if r.role == RaftLeader {
			r.lastQuorum = time.Now()
		}
		r.mu.Unlock()
	} else {
		r.expireLeaderWithoutQuorum()
	}
}

func (r *RaftRuntime) Propose(ctx context.Context, command MetadataCommand) (uint64, error) {
	r.proposalMu.Lock()
	defer r.proposalMu.Unlock()
	role, leaderID, term := r.Status()
	if role != RaftLeader {
		return 0, fmt.Errorf("%w: leader=%s", ErrNotRaftLeader, leaderID)
	}
	_, _, appliedEpoch := r.store.State()
	if command.Epoch != appliedEpoch+1 {
		return 0, fmt.Errorf("metadata epoch must advance exactly once")
	}
	index, err := r.store.AppendCommand(term, command)
	if err != nil {
		return 0, err
	}
	peers, ready := r.consensusPeers()
	if !ready {
		return 0, fmt.Errorf("metadata voter identities are not fully discovered")
	}
	acknowledgements := []string{r.nodeID}
	type result struct {
		nodeID       string
		acknowledged bool
	}
	results := make(chan result, len(peers))
	for _, peer := range peers {
		go func(peer RaftPeer) {
			results <- result{nodeID: peer.NodeID, acknowledged: r.replicatePeer(ctx, peer, term, index)}
		}(peer)
	}
	for range peers {
		result := <-results
		if result.acknowledged {
			acknowledgements = append(acknowledgements, result.nodeID)
		}
	}
	if err := r.store.CommitWithVoterAcks(index, term, acknowledgements); err != nil {
		return 0, err
	}
	r.mu.Lock()
	if r.role == RaftLeader {
		r.lastQuorum = time.Now()
	}
	r.mu.Unlock()
	snapshotIndex := uint64(0)
	if snapshot, exists := r.store.Snapshot(); exists {
		snapshotIndex = snapshot.LastIncludedIndex
	}
	if index-snapshotIndex >= r.config.SnapshotThreshold {
		if err := r.store.CompactCommitted(); err != nil {
			return 0, err
		}
	}
	r.broadcastHeartbeat(ctx)
	return command.Epoch, nil
}

// AdvanceView commits a metadata-only epoch transition under the current
// stable voter set. Unlike ChangeVoters it never enters joint consensus and is
// suitable for placement changes that leave membership unchanged.
func (r *RaftRuntime) AdvanceView(ctx context.Context, view ViewDigests) (uint64, error) {
	if !validView(view) {
		return 0, fmt.Errorf("valid target view digests are required")
	}
	_, _, epoch := r.store.State()
	return r.Propose(ctx, MetadataCommand{
		Type:             "advance_epoch",
		Epoch:            epoch + 1,
		MembershipDigest: view.Membership,
		CatalogDigest:    view.Catalog,
		PlacementDigest:  view.Placement,
		CapacityManifest: view.CapacityManifest,
	})
}

// ChangeVoters commits a one-node membership transition through joint
// consensus. The candidate node must already be discoverable as a learner.
func (r *RaftRuntime) ChangeVoters(ctx context.Context, newVoters []string, view ViewDigests) (uint64, error) {
	r.proposalMu.Lock()
	defer r.proposalMu.Unlock()
	role, leaderID, term := r.Status()
	if role != RaftLeader {
		return 0, fmt.Errorf("%w: leader=%s", ErrNotRaftLeader, leaderID)
	}
	if !validView(view) {
		return 0, fmt.Errorf("valid target view digests are required")
	}
	oldVoters, jointOld, jointNew := r.store.VoterConfiguration()
	if len(jointOld) != 0 || len(jointNew) != 0 {
		return 0, fmt.Errorf("membership transition already active")
	}
	normalized, err := normalizeVoterSet(newVoters)
	if err != nil || !equalStrings(normalized, newVoters) || voterSetDifference(oldVoters, newVoters) != 1 {
		return 0, fmt.Errorf("membership transition must contain one canonical node join or leave")
	}
	unionSet := stringSet(oldVoters)
	for _, voter := range newVoters {
		unionSet[voter] = struct{}{}
	}
	union := make([]string, 0, len(unionSet))
	for voter := range unionSet {
		union = append(union, voter)
	}
	sort.Strings(union)
	peers, ready := r.peersForVoters(union)
	if !ready {
		return 0, fmt.Errorf("old and new voter identities must be discoverable")
	}
	_, _, appliedEpoch := r.store.State()
	begin := MetadataCommand{Type: "begin_joint_consensus", Epoch: appliedEpoch, OldVoters: oldVoters, NewVoters: newVoters}
	beginIndex, err := r.store.AppendCommand(term, begin)
	if err != nil {
		return 0, err
	}
	acknowledged := r.replicateVoters(ctx, peers, term, beginIndex)
	ackSet := stringSet(acknowledged)
	for _, voter := range newVoters {
		if !containsString(oldVoters, voter) {
			if _, caughtUp := ackSet[voter]; !caughtUp {
				return 0, fmt.Errorf("joining voter did not catch up to joint-consensus entry")
			}
		}
	}
	if err := r.store.CommitWithVoterAcks(beginIndex, term, acknowledged); err != nil {
		return 0, err
	}
	// Publish the committed joint entry before proposing finalization.
	_ = r.replicateVoters(ctx, peers, term, beginIndex)
	finalize := MetadataCommand{Type: "finalize_joint_consensus", Epoch: appliedEpoch + 1, OldVoters: oldVoters, NewVoters: newVoters, MembershipDigest: view.Membership, CatalogDigest: view.Catalog, PlacementDigest: view.Placement, CapacityManifest: view.CapacityManifest}
	finalIndex, err := r.store.AppendCommand(term, finalize)
	if err != nil {
		return 0, err
	}
	acknowledged = r.replicateVoters(ctx, peers, term, finalIndex)
	if err := r.store.CommitWithVoterAcks(finalIndex, term, acknowledged); err != nil {
		return 0, err
	}
	_ = r.replicateVoters(ctx, peers, term, finalIndex)
	if !containsString(newVoters, r.nodeID) {
		r.becomeFollower("")
	}
	return appliedEpoch + 1, nil
}

func (r *RaftRuntime) replicateVoters(ctx context.Context, peers []RaftPeer, term, target uint64) []string {
	type result struct {
		nodeID string
		ok     bool
	}
	results := make(chan result, len(peers))
	for _, peer := range peers {
		go func(peer RaftPeer) {
			results <- result{nodeID: peer.NodeID, ok: r.replicatePeer(ctx, peer, term, target)}
		}(peer)
	}
	acknowledged := []string{r.nodeID}
	for range peers {
		result := <-results
		if result.ok {
			acknowledged = append(acknowledged, result.nodeID)
		}
	}
	return acknowledged
}

func (r *RaftRuntime) peersForVoters(voters []string) ([]RaftPeer, bool) {
	available := make(map[string]RaftPeer)
	for _, peer := range r.peers() {
		if !ValidNodeID(peer.NodeID) || peer.BaseURL == "" || peer.NodeID == r.nodeID {
			return nil, false
		}
		if _, duplicate := available[peer.NodeID]; duplicate {
			return nil, false
		}
		available[peer.NodeID] = peer
	}
	result := make([]RaftPeer, 0, len(voters)-1)
	for _, voter := range voters {
		if voter == r.nodeID {
			continue
		}
		peer, exists := available[voter]
		if !exists {
			return nil, false
		}
		result = append(result, peer)
	}
	return result, true
}

func (r *RaftRuntime) replicatePeer(ctx context.Context, peer RaftPeer, term, target uint64) bool {
	for {
		if ctx.Err() != nil || !r.isLeader() {
			return false
		}
		r.mu.RLock()
		next := r.nextIndex[peer.NodeID]
		r.mu.RUnlock()
		if next == 0 {
			next = target + 1
		}
		if snapshot, exists := r.store.Snapshot(); exists && next <= snapshot.LastIncludedIndex {
			callCtx, cancel := context.WithTimeout(ctx, r.config.Heartbeat)
			response, callErr := r.transport.InstallSnapshot(callCtx, peer, InstallSnapshotRequest{Term: term, LeaderID: r.nodeID, Snapshot: snapshot})
			cancel()
			if callErr != nil {
				return false
			}
			if response.Term > term {
				_ = r.store.ObserveTerm(response.Term)
				r.becomeFollower("")
				return false
			}
			if !response.Success {
				return false
			}
			r.mu.Lock()
			r.nextIndex[peer.NodeID] = response.MatchIndex + 1
			r.mu.Unlock()
			if response.MatchIndex >= target {
				return true
			}
			continue
		}
		currentTerm, commitIndex, prevIndex, prevTerm, entries, err := r.store.LogBatch(next, 128)
		if err != nil || currentTerm != term {
			return false
		}
		callCtx, cancel := context.WithTimeout(ctx, r.config.Heartbeat)
		response, callErr := r.transport.AppendEntries(callCtx, peer, AppendEntriesRequest{Term: term, LeaderID: r.nodeID, PrevLogIndex: prevIndex, PrevLogTerm: prevTerm, Entries: entries, LeaderCommit: commitIndex})
		cancel()
		if callErr != nil {
			return false
		}
		if response.Term > term {
			_ = r.store.ObserveTerm(response.Term)
			r.becomeFollower("")
			return false
		}
		if !response.Success {
			if next == 1 {
				return false
			}
			r.mu.Lock()
			r.nextIndex[peer.NodeID] = next - 1
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		r.nextIndex[peer.NodeID] = response.MatchIndex + 1
		r.mu.Unlock()
		if response.MatchIndex >= target {
			return true
		}
	}
}

func (r *RaftRuntime) consensusPeers() ([]RaftPeer, bool) {
	peers := r.peers()
	peerByID := make(map[string]RaftPeer, len(peers))
	seen := map[string]struct{}{r.nodeID: {}}
	for _, peer := range peers {
		if !ValidNodeID(peer.NodeID) || peer.BaseURL == "" {
			return nil, false
		}
		if _, duplicate := seen[peer.NodeID]; duplicate {
			return nil, false
		}
		seen[peer.NodeID] = struct{}{}
		peerByID[peer.NodeID] = peer
	}
	active := r.store.ActiveVoters()
	if len(active) == 0 {
		voters := make([]string, 0, len(peerByID)+1)
		voters = append(voters, r.nodeID)
		for nodeID := range peerByID {
			voters = append(voters, nodeID)
		}
		if err := r.store.ConfigureVoters(voters); err != nil {
			return nil, false
		}
		active = r.store.ActiveVoters()
	}
	result := make([]RaftPeer, 0, len(active)-1)
	for _, voter := range active {
		if voter == r.nodeID {
			continue
		}
		peer, exists := peerByID[voter]
		if !exists {
			return nil, false
		}
		result = append(result, peer)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result, true
}

func (r *RaftRuntime) electionTimeout() time.Duration {
	span := r.config.ElectionMax - r.config.ElectionMin
	if span == 0 {
		return r.config.ElectionMin
	}
	return r.config.ElectionMin + time.Duration(rand.Int64N(int64(span)))
}

func (r *RaftRuntime) isLeader() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.role == RaftLeader
}

func (r *RaftRuntime) becomeFollower(leader string) {
	r.mu.Lock()
	r.role, r.leaderID = RaftFollower, leader
	r.nextIndex = make(map[string]uint64)
	r.lastQuorum = time.Time{}
	r.mu.Unlock()
}

func (r *RaftRuntime) expireLeaderWithoutQuorum() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.role == RaftLeader && !r.lastQuorum.IsZero() && time.Since(r.lastQuorum) >= r.config.ElectionMin {
		r.role, r.leaderID = RaftFollower, ""
		r.nextIndex = make(map[string]uint64)
		r.lastQuorum = time.Time{}
	}
}

func (r *RaftRuntime) signalReset() {
	select {
	case r.reset <- struct{}{}:
	default:
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
