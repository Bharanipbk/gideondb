package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
)

const RaftStateFile = "CLUSTER_RAFT.json"

type MetadataCommand struct {
	Type             string   `json:"type"`
	Epoch            uint64   `json:"epoch"`
	MembershipDigest string   `json:"membership_digest"`
	CatalogDigest    string   `json:"catalog_digest"`
	PlacementDigest  string   `json:"placement_digest"`
	CapacityManifest string   `json:"capacity_manifest,omitempty"`
	OldVoters        []string `json:"old_voters,omitempty"`
	NewVoters        []string `json:"new_voters,omitempty"`
}

type RaftEntry struct {
	Term    uint64          `json:"term"`
	Command MetadataCommand `json:"command"`
}

type raftPersistentState struct {
	Format         int          `json:"format"`
	CurrentTerm    uint64       `json:"current_term"`
	VotedFor       string       `json:"voted_for,omitempty"`
	CommitIndex    uint64       `json:"commit_index"`
	AppliedEpoch   uint64       `json:"applied_epoch"`
	SnapshotIndex  uint64       `json:"snapshot_index,omitempty"`
	SnapshotTerm   uint64       `json:"snapshot_term,omitempty"`
	SnapshotEpoch  uint64       `json:"snapshot_epoch,omitempty"`
	SnapshotView   *ViewDigests `json:"snapshot_view,omitempty"`
	Voters         []string     `json:"voters,omitempty"`
	JointOldVoters []string     `json:"joint_old_voters,omitempty"`
	JointNewVoters []string     `json:"joint_new_voters,omitempty"`
	CommittedView  *ViewDigests `json:"committed_view,omitempty"`
	Entries        []RaftEntry  `json:"entries"`
}

type RequestVoteRequest struct {
	Term         uint64 `json:"term"`
	CandidateID  string `json:"candidate_id"`
	LastLogIndex uint64 `json:"last_log_index"`
	LastLogTerm  uint64 `json:"last_log_term"`
}

type RequestVoteResponse struct {
	Term        uint64 `json:"term"`
	VoteGranted bool   `json:"vote_granted"`
}

type AppendEntriesRequest struct {
	Term         uint64      `json:"term"`
	LeaderID     string      `json:"leader_id"`
	PrevLogIndex uint64      `json:"prev_log_index"`
	PrevLogTerm  uint64      `json:"prev_log_term"`
	Entries      []RaftEntry `json:"entries"`
	LeaderCommit uint64      `json:"leader_commit"`
}

type AppendEntriesResponse struct {
	Term       uint64 `json:"term"`
	Success    bool   `json:"success"`
	MatchIndex uint64 `json:"match_index"`
}

type RaftSnapshot struct {
	LastIncludedIndex uint64       `json:"last_included_index"`
	LastIncludedTerm  uint64       `json:"last_included_term"`
	AppliedEpoch      uint64       `json:"applied_epoch"`
	Voters            []string     `json:"voters"`
	CommittedView     *ViewDigests `json:"committed_view"`
}

type InstallSnapshotRequest struct {
	Term     uint64       `json:"term"`
	LeaderID string       `json:"leader_id"`
	Snapshot RaftSnapshot `json:"snapshot"`
}

type InstallSnapshotResponse struct {
	Term       uint64 `json:"term"`
	Success    bool   `json:"success"`
	MatchIndex uint64 `json:"match_index"`
}

// RaftStore implements the durable safety core of the metadata Raft group.
// Election timers and RPC transport are deliberately outside this type.
type RaftStore struct {
	mu     sync.Mutex
	path   string
	nodeID string
	state  raftPersistentState
}

func OpenRaftStore(dataPath, nodeID string, bootstrapEpoch uint64) (*RaftStore, error) {
	if !ValidNodeID(nodeID) || bootstrapEpoch == 0 {
		return nil, fmt.Errorf("valid node ID and bootstrap epoch are required")
	}
	store := &RaftStore{path: filepath.Join(dataPath, RaftStateFile), nodeID: nodeID}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		store.state = raftPersistentState{Format: 1, AppliedEpoch: bootstrapEpoch, SnapshotEpoch: bootstrapEpoch, Entries: []RaftEntry{}}
		if err := store.persistLocked(); err != nil {
			return nil, err
		}
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("decode Raft state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("Raft state must contain one JSON value")
	}
	if err := store.validateLocked(bootstrapEpoch); err != nil {
		return nil, err
	}
	return store, nil
}

func (r *RaftStore) State() (term, commitIndex, appliedEpoch uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state.CurrentTerm, r.state.CommitIndex, r.state.AppliedEpoch
}

func (r *RaftStore) LogSnapshot() (term, commitIndex, lastIndex, lastTerm uint64, entries []RaftEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lastIndex, lastTerm = r.lastLogLocked()
	return r.state.CurrentTerm, r.state.CommitIndex, lastIndex, lastTerm, append([]RaftEntry(nil), r.state.Entries...)
}

func (r *RaftStore) LogBatch(nextIndex uint64, limit int) (term, commitIndex, prevIndex, prevTerm uint64, entries []RaftEntry, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lastIndex, _ := r.lastLogLocked()
	if nextIndex <= r.state.SnapshotIndex || nextIndex > lastIndex+1 || limit < 1 || limit > 1024 {
		return 0, 0, 0, 0, nil, fmt.Errorf("invalid Raft log batch request")
	}
	prevIndex = nextIndex - 1
	prevTerm, err = r.termAtLocked(prevIndex)
	if err != nil {
		return 0, 0, 0, 0, nil, err
	}
	start := int(nextIndex - r.state.SnapshotIndex - 1)
	end := min(len(r.state.Entries), start+limit)
	entries = append([]RaftEntry(nil), r.state.Entries[start:end]...)
	return r.state.CurrentTerm, r.state.CommitIndex, prevIndex, prevTerm, entries, nil
}

func (r *RaftStore) ObserveTerm(term uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if term <= r.state.CurrentTerm {
		return nil
	}
	r.state.CurrentTerm = term
	r.state.VotedFor = ""
	return r.persistLocked()
}

func (r *RaftStore) ConfigureVoters(voters []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	normalized, err := normalizeVoters(voters, r.nodeID)
	if err != nil {
		return err
	}
	if len(r.state.Voters) != 0 {
		if !equalStrings(r.state.Voters, normalized) {
			return fmt.Errorf("configured voters do not match persisted Raft membership")
		}
		return nil
	}
	r.state.Voters = normalized
	return r.persistLocked()
}

func (r *RaftStore) Voters() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.state.Voters...)
}

// VoterConfiguration returns the stable voter set and any active joint
// old/new transition. Returned slices are detached copies.
func (r *RaftStore) VoterConfiguration() (stable, oldVoters, newVoters []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.state.Voters...), append([]string(nil), r.state.JointOldVoters...), append([]string(nil), r.state.JointNewVoters...)
}

func (r *RaftStore) ActiveVoters() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.activeVotersLocked()...)
}

func (r *RaftStore) HasVoterQuorum(acknowledgements []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ackSet := stringSet(acknowledgements)
	if len(r.state.JointOldVoters) != 0 {
		return voterMajority(r.state.JointOldVoters, ackSet) && voterMajority(r.state.JointNewVoters, ackSet)
	}
	return voterMajority(r.state.Voters, ackSet)
}

func (r *RaftStore) CommittedView() (ViewDigests, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.CommittedView == nil {
		return ViewDigests{}, false
	}
	return *r.state.CommittedView, true
}

func (r *RaftStore) Snapshot() (RaftSnapshot, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state.SnapshotIndex == 0 {
		return RaftSnapshot{}, false
	}
	return r.snapshotLocked(), true
}

func (r *RaftStore) snapshotLocked() RaftSnapshot {
	var view *ViewDigests
	if r.state.SnapshotView != nil {
		copy := *r.state.SnapshotView
		view = &copy
	}
	return RaftSnapshot{LastIncludedIndex: r.state.SnapshotIndex, LastIncludedTerm: r.state.SnapshotTerm, AppliedEpoch: r.state.SnapshotEpoch, Voters: append([]string(nil), r.state.Voters...), CommittedView: view}
}

func (r *RaftStore) InstallSnapshot(request InstallSnapshotRequest) (InstallSnapshotResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ValidNodeID(request.LeaderID) || request.Term == 0 {
		return InstallSnapshotResponse{}, fmt.Errorf("invalid snapshot request")
	}
	if len(r.state.Voters) != 0 && !containsString(r.state.Voters, request.LeaderID) {
		return InstallSnapshotResponse{Term: r.state.CurrentTerm}, nil
	}
	snapshot := request.Snapshot
	normalized, err := normalizeVoters(snapshot.Voters, r.nodeID)
	if err != nil || !equalStrings(normalized, snapshot.Voters) || snapshot.LastIncludedIndex == 0 || snapshot.LastIncludedTerm == 0 || snapshot.AppliedEpoch < 2 || snapshot.CommittedView == nil || !validView(*snapshot.CommittedView) {
		return InstallSnapshotResponse{}, fmt.Errorf("invalid Raft snapshot")
	}
	if len(r.state.Voters) != 0 && !equalStrings(r.state.Voters, snapshot.Voters) {
		return InstallSnapshotResponse{}, fmt.Errorf("snapshot voter set does not match persisted membership")
	}
	if request.Term < r.state.CurrentTerm {
		return InstallSnapshotResponse{Term: r.state.CurrentTerm}, nil
	}
	if request.Term > r.state.CurrentTerm {
		r.state.CurrentTerm, r.state.VotedFor = request.Term, ""
	}
	if snapshot.LastIncludedIndex <= r.state.CommitIndex {
		if err := r.persistLocked(); err != nil {
			return InstallSnapshotResponse{}, err
		}
		return InstallSnapshotResponse{Term: r.state.CurrentTerm, Success: true, MatchIndex: r.state.CommitIndex}, nil
	}
	retained := []RaftEntry(nil)
	if term, termErr := r.termAtLocked(snapshot.LastIncludedIndex); termErr == nil && term == snapshot.LastIncludedTerm {
		start := snapshot.LastIncludedIndex - r.state.SnapshotIndex
		retained = append(retained, r.state.Entries[start:]...)
	}
	r.state.SnapshotIndex = snapshot.LastIncludedIndex
	r.state.SnapshotTerm = snapshot.LastIncludedTerm
	r.state.SnapshotEpoch = snapshot.AppliedEpoch
	r.state.SnapshotView = cloneView(snapshot.CommittedView)
	r.state.CommitIndex = snapshot.LastIncludedIndex
	r.state.AppliedEpoch = snapshot.AppliedEpoch
	r.state.CommittedView = cloneView(snapshot.CommittedView)
	r.state.Voters = append([]string(nil), snapshot.Voters...)
	r.state.Entries = retained
	if err := r.persistLocked(); err != nil {
		return InstallSnapshotResponse{}, err
	}
	return InstallSnapshotResponse{Term: r.state.CurrentTerm, Success: true, MatchIndex: snapshot.LastIncludedIndex}, nil
}

func cloneView(view *ViewDigests) *ViewDigests {
	if view == nil {
		return nil
	}
	copy := *view
	return &copy
}

// CompactCommitted snapshots the applied metadata state and removes the fully
// committed log prefix while preserving any uncommitted suffix.
func (r *RaftStore) CompactCommitted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.JointOldVoters) != 0 {
		return fmt.Errorf("cannot compact metadata log during joint consensus")
	}
	if r.state.CommitIndex == r.state.SnapshotIndex {
		return nil
	}
	term, err := r.termAtLocked(r.state.CommitIndex)
	if err != nil {
		return err
	}
	remove := r.state.CommitIndex - r.state.SnapshotIndex
	r.state.Entries = append([]RaftEntry(nil), r.state.Entries[remove:]...)
	r.state.SnapshotIndex = r.state.CommitIndex
	r.state.SnapshotTerm = term
	r.state.SnapshotEpoch = r.state.AppliedEpoch
	if r.state.CommittedView == nil {
		r.state.SnapshotView = nil
	} else {
		view := *r.state.CommittedView
		r.state.SnapshotView = &view
	}
	return r.persistLocked()
}

func (r *RaftStore) BeginElection() (RequestVoteRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.Voters) != 0 && !containsString(r.activeVotersLocked(), r.nodeID) {
		return RequestVoteRequest{}, fmt.Errorf("local node is not an active Raft voter")
	}
	r.state.CurrentTerm++
	r.state.VotedFor = r.nodeID
	if err := r.persistLocked(); err != nil {
		return RequestVoteRequest{}, err
	}
	lastIndex, lastTerm := r.lastLogLocked()
	return RequestVoteRequest{Term: r.state.CurrentTerm, CandidateID: r.nodeID, LastLogIndex: lastIndex, LastLogTerm: lastTerm}, nil
}

func (r *RaftStore) RequestVote(request RequestVoteRequest) (RequestVoteResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ValidNodeID(request.CandidateID) || request.Term == 0 {
		return RequestVoteResponse{}, fmt.Errorf("invalid vote request")
	}
	if len(r.state.Voters) != 0 && !containsString(r.activeVotersLocked(), request.CandidateID) {
		return RequestVoteResponse{Term: r.state.CurrentTerm}, nil
	}
	if request.Term < r.state.CurrentTerm {
		return RequestVoteResponse{Term: r.state.CurrentTerm}, nil
	}
	changed := false
	if request.Term > r.state.CurrentTerm {
		r.state.CurrentTerm, r.state.VotedFor, changed = request.Term, "", true
	}
	lastIndex, lastTerm := r.lastLogLocked()
	upToDate := request.LastLogTerm > lastTerm || request.LastLogTerm == lastTerm && request.LastLogIndex >= lastIndex
	granted := upToDate && (r.state.VotedFor == "" || r.state.VotedFor == request.CandidateID)
	if granted && r.state.VotedFor != request.CandidateID {
		r.state.VotedFor, changed = request.CandidateID, true
	}
	if changed {
		if err := r.persistLocked(); err != nil {
			return RequestVoteResponse{}, err
		}
	}
	return RequestVoteResponse{Term: r.state.CurrentTerm, VoteGranted: granted}, nil
}

func (r *RaftStore) AppendEntries(request AppendEntriesRequest) (AppendEntriesResponse, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !ValidNodeID(request.LeaderID) || request.Term == 0 {
		return AppendEntriesResponse{}, fmt.Errorf("invalid append request")
	}
	if len(r.state.Voters) != 0 && !containsString(r.activeVotersLocked(), request.LeaderID) {
		return AppendEntriesResponse{Term: r.state.CurrentTerm}, nil
	}
	if len(request.Entries) > 1024 {
		return AppendEntriesResponse{}, fmt.Errorf("append request exceeds entry limit")
	}
	if request.Term < r.state.CurrentTerm {
		return AppendEntriesResponse{Term: r.state.CurrentTerm}, nil
	}
	if request.Term > r.state.CurrentTerm {
		r.state.CurrentTerm, r.state.VotedFor = request.Term, ""
	}
	lastIndex, _ := r.lastLogLocked()
	previousTerm, previousErr := r.termAtLocked(request.PrevLogIndex)
	if request.PrevLogIndex < r.state.SnapshotIndex || request.PrevLogIndex > lastIndex || previousErr != nil || previousTerm != request.PrevLogTerm {
		if err := r.persistLocked(); err != nil {
			return AppendEntriesResponse{}, err
		}
		return AppendEntriesResponse{Term: r.state.CurrentTerm}, nil
	}
	for offset, entry := range request.Entries {
		index := request.PrevLogIndex + uint64(offset) + 1
		if index <= r.state.SnapshotIndex {
			continue
		}
		entryOffset := index - r.state.SnapshotIndex - 1
		if entryOffset < uint64(len(r.state.Entries)) {
			if r.state.Entries[entryOffset].Term == entry.Term {
				continue
			}
			if index <= r.state.CommitIndex {
				return AppendEntriesResponse{}, fmt.Errorf("refusing to overwrite committed entry")
			}
			r.state.Entries = r.state.Entries[:entryOffset]
		}
		if err := validateRaftEntry(entry); err != nil {
			return AppendEntriesResponse{}, err
		}
		r.state.Entries = append(r.state.Entries, entry)
	}
	if request.LeaderCommit > r.state.CommitIndex {
		lastIndex, _ := r.lastLogLocked()
		target := min(request.LeaderCommit, lastIndex)
		if err := r.applyThroughLocked(target); err != nil {
			return AppendEntriesResponse{}, err
		}
	}
	if err := r.persistLocked(); err != nil {
		return AppendEntriesResponse{}, err
	}
	return AppendEntriesResponse{Term: r.state.CurrentTerm, Success: true, MatchIndex: max(request.PrevLogIndex, r.state.SnapshotIndex) + uint64(len(request.Entries))}, nil
}

func (r *RaftStore) AppendCommand(term uint64, command MetadataCommand) (uint64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if term == 0 || term != r.state.CurrentTerm || r.state.VotedFor != r.nodeID {
		return 0, fmt.Errorf("node is not leader for term %d", term)
	}
	entry := RaftEntry{Term: term, Command: command}
	if err := validateRaftEntry(entry); err != nil {
		return 0, err
	}
	lastIndex, _ := r.lastLogLocked()
	if lastIndex > r.state.CommitIndex {
		last := r.state.Entries[len(r.state.Entries)-1]
		if last.Term == term && metadataCommandsEqual(last.Command, command) {
			lastIndex, _ := r.lastLogLocked()
			return lastIndex, nil
		}
	}
	r.state.Entries = append(r.state.Entries, entry)
	if err := r.persistLocked(); err != nil {
		return 0, err
	}
	lastIndex, _ = r.lastLogLocked()
	return lastIndex, nil
}

func (r *RaftStore) CommitWithQuorum(index, term uint64, acknowledgements, voters int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.state.JointOldVoters) != 0 {
		return fmt.Errorf("joint consensus requires voter-identity acknowledgements")
	}
	if voters < 1 || acknowledgements < voters/2+1 {
		return fmt.Errorf("metadata quorum not reached")
	}
	entryTerm, entryErr := r.termAtLocked(index)
	if term != r.state.CurrentTerm || index <= r.state.SnapshotIndex || entryErr != nil || entryTerm != term {
		return fmt.Errorf("entry is not committable in current term")
	}
	if err := r.applyThroughLocked(index); err != nil {
		return err
	}
	return r.persistLocked()
}

// CommitWithVoterAcks enforces a majority of both old and new voter sets while
// joint consensus is active, and a majority of the stable set otherwise.
func (r *RaftStore) CommitWithVoterAcks(index, term uint64, acknowledgements []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ackSet := stringSet(acknowledgements)
	if len(r.state.JointOldVoters) != 0 {
		if !voterMajority(r.state.JointOldVoters, ackSet) || !voterMajority(r.state.JointNewVoters, ackSet) {
			return fmt.Errorf("metadata joint quorum not reached")
		}
	} else if !voterMajority(r.state.Voters, ackSet) {
		return fmt.Errorf("metadata quorum not reached")
	}
	entryTerm, entryErr := r.termAtLocked(index)
	if term != r.state.CurrentTerm || index <= r.state.SnapshotIndex || entryErr != nil || entryTerm != term {
		return fmt.Errorf("entry is not committable in current term")
	}
	if err := r.applyThroughLocked(index); err != nil {
		return err
	}
	return r.persistLocked()
}

func (r *RaftStore) applyThroughLocked(target uint64) error {
	commitIndex := r.state.CommitIndex
	appliedEpoch := r.state.AppliedEpoch
	var committedView *ViewDigests
	if r.state.CommittedView != nil {
		view := *r.state.CommittedView
		committedView = &view
	}
	voters := append([]string(nil), r.state.Voters...)
	jointOld := append([]string(nil), r.state.JointOldVoters...)
	jointNew := append([]string(nil), r.state.JointNewVoters...)
	for commitIndex < target {
		entry := r.state.Entries[commitIndex-r.state.SnapshotIndex]
		switch entry.Command.Type {
		case "advance_epoch":
			if len(jointOld) != 0 || entry.Command.Epoch > appliedEpoch+1 || entry.Command.Epoch < appliedEpoch {
				return fmt.Errorf("metadata epoch must advance exactly once")
			}
			if entry.Command.Epoch == appliedEpoch+1 {
				appliedEpoch = entry.Command.Epoch
				view := commandView(entry.Command)
				committedView = &view
			} else if committedView == nil || *committedView != commandView(entry.Command) {
				return fmt.Errorf("duplicate metadata epoch has conflicting view digests")
			}
		case "begin_joint_consensus":
			if len(voters) == 0 {
				voters = append([]string(nil), entry.Command.OldVoters...)
			}
			if entry.Command.Epoch != appliedEpoch || len(jointOld) != 0 || !equalStrings(entry.Command.OldVoters, voters) {
				return fmt.Errorf("invalid joint-consensus begin")
			}
			jointOld = append([]string(nil), entry.Command.OldVoters...)
			jointNew = append([]string(nil), entry.Command.NewVoters...)
		case "finalize_joint_consensus":
			if entry.Command.Epoch != appliedEpoch+1 || !equalStrings(entry.Command.OldVoters, jointOld) || !equalStrings(entry.Command.NewVoters, jointNew) {
				return fmt.Errorf("invalid joint-consensus finalize")
			}
			appliedEpoch = entry.Command.Epoch
			view := commandView(entry.Command)
			committedView = &view
			voters = append([]string(nil), entry.Command.NewVoters...)
			jointOld, jointNew = nil, nil
		default:
			return fmt.Errorf("unknown metadata command")
		}
		commitIndex++
	}
	r.state.CommitIndex = commitIndex
	r.state.AppliedEpoch = appliedEpoch
	r.state.CommittedView = committedView
	r.state.Voters, r.state.JointOldVoters, r.state.JointNewVoters = voters, jointOld, jointNew
	return nil
}

func (r *RaftStore) lastLogLocked() (uint64, uint64) {
	if len(r.state.Entries) == 0 {
		return r.state.SnapshotIndex, r.state.SnapshotTerm
	}
	return r.state.SnapshotIndex + uint64(len(r.state.Entries)), r.state.Entries[len(r.state.Entries)-1].Term
}

func (r *RaftStore) termAtLocked(index uint64) (uint64, error) {
	if index == r.state.SnapshotIndex {
		return r.state.SnapshotTerm, nil
	}
	if index < r.state.SnapshotIndex || index > r.state.SnapshotIndex+uint64(len(r.state.Entries)) {
		return 0, fmt.Errorf("Raft log index is outside retained range")
	}
	return r.state.Entries[index-r.state.SnapshotIndex-1].Term, nil
}

func (r *RaftStore) validateLocked(bootstrapEpoch uint64) error {
	if r.state.SnapshotEpoch == 0 && r.state.SnapshotIndex == 0 {
		r.state.SnapshotEpoch = bootstrapEpoch
	}
	lastIndex, _ := r.lastLogLocked()
	if r.state.Format != 1 || r.state.AppliedEpoch < bootstrapEpoch || r.state.CommitIndex < r.state.SnapshotIndex || r.state.CommitIndex > lastIndex || r.state.SnapshotEpoch < bootstrapEpoch {
		return fmt.Errorf("invalid Raft state")
	}
	if r.state.SnapshotIndex == 0 && r.state.SnapshotTerm != 0 || r.state.SnapshotIndex > 0 && r.state.SnapshotTerm == 0 {
		return fmt.Errorf("invalid Raft snapshot boundary")
	}
	if r.state.SnapshotEpoch == bootstrapEpoch && r.state.SnapshotView != nil || r.state.SnapshotEpoch > bootstrapEpoch && r.state.SnapshotView == nil {
		return fmt.Errorf("invalid Raft snapshot metadata")
	}
	if r.state.SnapshotView != nil && !validView(*r.state.SnapshotView) {
		return fmt.Errorf("invalid Raft snapshot view digests")
	}
	if len(r.state.Voters) != 0 {
		normalized, err := normalizeVoterSet(r.state.Voters)
		if err != nil || !equalStrings(normalized, r.state.Voters) {
			return fmt.Errorf("invalid persisted Raft voters")
		}
	}
	if len(r.state.JointOldVoters) != 0 || len(r.state.JointNewVoters) != 0 {
		oldNormalized, oldErr := normalizeVoterSet(r.state.JointOldVoters)
		newNormalized, newErr := normalizeVoterSet(r.state.JointNewVoters)
		if oldErr != nil || newErr != nil || !equalStrings(oldNormalized, r.state.JointOldVoters) || !equalStrings(newNormalized, r.state.JointNewVoters) || !equalStrings(r.state.Voters, r.state.JointOldVoters) {
			return fmt.Errorf("invalid persisted joint voter configuration")
		}
	}
	appliedEpoch := r.state.SnapshotEpoch
	var replayedVoters, replayedJointOld, replayedJointNew []string
	var committedView *ViewDigests
	if r.state.SnapshotView != nil {
		view := *r.state.SnapshotView
		committedView = &view
	}
	for index, entry := range r.state.Entries {
		if err := validateRaftEntry(entry); err != nil {
			return err
		}
		absoluteIndex := r.state.SnapshotIndex + uint64(index) + 1
		if absoluteIndex <= r.state.CommitIndex {
			switch entry.Command.Type {
			case "advance_epoch":
				if len(replayedJointOld) != 0 || entry.Command.Epoch > appliedEpoch+1 || entry.Command.Epoch < appliedEpoch {
					return fmt.Errorf("invalid committed metadata epoch sequence")
				}
				if entry.Command.Epoch == appliedEpoch+1 {
					appliedEpoch = entry.Command.Epoch
					view := commandView(entry.Command)
					committedView = &view
				} else if committedView == nil || *committedView != commandView(entry.Command) {
					return fmt.Errorf("conflicting committed metadata view")
				}
			case "begin_joint_consensus":
				if entry.Command.Epoch != appliedEpoch || len(replayedJointOld) != 0 {
					return fmt.Errorf("invalid committed joint-consensus begin")
				}
				replayedVoters = append([]string(nil), entry.Command.OldVoters...)
				replayedJointOld = append([]string(nil), entry.Command.OldVoters...)
				replayedJointNew = append([]string(nil), entry.Command.NewVoters...)
			case "finalize_joint_consensus":
				if entry.Command.Epoch != appliedEpoch+1 || !equalStrings(entry.Command.OldVoters, replayedJointOld) || !equalStrings(entry.Command.NewVoters, replayedJointNew) {
					return fmt.Errorf("invalid committed joint-consensus finalize")
				}
				appliedEpoch = entry.Command.Epoch
				view := commandView(entry.Command)
				committedView = &view
				replayedVoters = append([]string(nil), entry.Command.NewVoters...)
				replayedJointOld, replayedJointNew = nil, nil
			}
		}
	}
	if appliedEpoch != r.state.AppliedEpoch {
		return fmt.Errorf("applied metadata epoch does not match committed log")
	}
	if len(replayedVoters) != 0 && !equalStrings(replayedVoters, r.state.Voters) || !equalStrings(replayedJointOld, r.state.JointOldVoters) || !equalStrings(replayedJointNew, r.state.JointNewVoters) {
		return fmt.Errorf("persisted voter configuration does not match committed log")
	}
	if committedView == nil {
		if r.state.CommittedView != nil {
			return fmt.Errorf("unexpected committed metadata view")
		}
	} else if r.state.CommittedView == nil {
		r.state.CommittedView = committedView
	} else if *committedView != *r.state.CommittedView {
		return fmt.Errorf("committed metadata view does not match log")
	}
	return nil
}

func commandView(command MetadataCommand) ViewDigests {
	return ViewDigests{Membership: command.MembershipDigest, Catalog: command.CatalogDigest, Placement: command.PlacementDigest, CapacityManifest: command.CapacityManifest}
}

func normalizeVoters(voters []string, localNodeID string) ([]string, error) {
	normalized, err := normalizeVoterSet(voters)
	if err != nil {
		return nil, err
	}
	if !containsString(normalized, localNodeID) {
		return nil, fmt.Errorf("Raft voter set excludes local node")
	}
	return normalized, nil
}

func normalizeVoterSet(voters []string) ([]string, error) {
	if len(voters) == 0 {
		return nil, fmt.Errorf("Raft voter set must not be empty")
	}
	normalized := append([]string(nil), voters...)
	sort.Strings(normalized)
	for index, voter := range normalized {
		if !ValidNodeID(voter) {
			return nil, fmt.Errorf("invalid Raft voter ID")
		}
		if index > 0 && voter == normalized[index-1] {
			return nil, fmt.Errorf("duplicate Raft voter ID")
		}
	}
	return normalized, nil
}

func containsString(values []string, wanted string) bool {
	index := sort.SearchStrings(values, wanted)
	return index < len(values) && values[index] == wanted
}

func (r *RaftStore) activeVotersLocked() []string {
	if len(r.state.JointOldVoters) == 0 {
		return r.state.Voters
	}
	union := stringSet(r.state.JointOldVoters)
	for _, voter := range r.state.JointNewVoters {
		union[voter] = struct{}{}
	}
	result := make([]string, 0, len(union))
	for voter := range union {
		result = append(result, voter)
	}
	sort.Strings(result)
	return result
}

func equalStrings(left, right []string) bool {
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

func validateRaftEntry(entry RaftEntry) error {
	command := entry.Command
	if entry.Term == 0 {
		return fmt.Errorf("invalid metadata log entry")
	}
	switch command.Type {
	case "advance_epoch":
		if command.Epoch < 2 || len(command.OldVoters) != 0 || len(command.NewVoters) != 0 || !validView(commandView(command)) {
			return fmt.Errorf("invalid metadata log entry")
		}
	case "begin_joint_consensus":
		oldVoters, oldErr := normalizeVoterSet(command.OldVoters)
		newVoters, newErr := normalizeVoterSet(command.NewVoters)
		if command.Epoch == 0 || oldErr != nil || newErr != nil || !equalStrings(oldVoters, command.OldVoters) || !equalStrings(newVoters, command.NewVoters) || voterSetDifference(command.OldVoters, command.NewVoters) != 1 {
			return fmt.Errorf("invalid joint-consensus begin entry")
		}
	case "finalize_joint_consensus":
		oldVoters, oldErr := normalizeVoterSet(command.OldVoters)
		newVoters, newErr := normalizeVoterSet(command.NewVoters)
		if command.Epoch < 2 || oldErr != nil || newErr != nil || !equalStrings(oldVoters, command.OldVoters) || !equalStrings(newVoters, command.NewVoters) || voterSetDifference(command.OldVoters, command.NewVoters) != 1 || !validView(commandView(command)) {
			return fmt.Errorf("invalid joint-consensus finalize entry")
		}
	default:
		return fmt.Errorf("invalid metadata log entry")
	}
	return nil
}

func metadataCommandsEqual(left, right MetadataCommand) bool {
	return left.Type == right.Type && left.Epoch == right.Epoch && left.MembershipDigest == right.MembershipDigest && left.CatalogDigest == right.CatalogDigest && left.PlacementDigest == right.PlacementDigest && left.CapacityManifest == right.CapacityManifest && equalStrings(left.OldVoters, right.OldVoters) && equalStrings(left.NewVoters, right.NewVoters)
}

func voterMajority(voters []string, acknowledgements map[string]struct{}) bool {
	count := 0
	for _, voter := range voters {
		if _, exists := acknowledgements[voter]; exists {
			count++
		}
	}
	return len(voters) > 0 && count >= len(voters)/2+1
}

func voterSetDifference(oldVoters, newVoters []string) int {
	oldSet, newSet := stringSet(oldVoters), stringSet(newVoters)
	difference := 0
	for voter := range oldSet {
		if _, exists := newSet[voter]; !exists {
			difference++
		}
	}
	for voter := range newSet {
		if _, exists := oldSet[voter]; !exists {
			difference++
		}
	}
	return difference
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func validView(view ViewDigests) bool {
	if !validDigest(view.Membership) || !validDigest(view.Catalog) || !validDigest(view.Placement) {
		return false
	}
	_, err := ParseCapacityManifest(view.CapacityManifest)
	return err == nil
}

func (r *RaftStore) persistLocked() error {
	payload, err := json.Marshal(r.state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(r.path), ".cluster-raft-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, r.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(r.path))
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		_ = directory.Close()
		return syncErr
	}
	return nil
}
