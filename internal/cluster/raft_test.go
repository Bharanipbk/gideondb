package cluster

import (
	"strings"
	"testing"
)

func TestRaftElectionReplicationQuorumAndRestart(t *testing.T) {
	ids := []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
	}
	paths := []string{t.TempDir(), t.TempDir(), t.TempDir()}
	stores := make([]*RaftStore, len(ids))
	for index := range stores {
		var err error
		stores[index], err = OpenRaftStore(paths[index], ids[index], 1)
		if err != nil {
			t.Fatal(err)
		}
	}
	vote, err := stores[0].BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	granted := 1
	for _, follower := range stores[1:] {
		response, err := follower.RequestVote(vote)
		if err != nil {
			t.Fatal(err)
		}
		if response.VoteGranted {
			granted++
		}
	}
	if granted != 3 {
		t.Fatalf("votes=%d", granted)
	}
	command := MetadataCommand{
		Type: "advance_epoch", Epoch: 2,
		MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64),
		CapacityManifest: `[{"node_id":"11111111111111111111111111111111","capacity":2},{"node_id":"22222222222222222222222222222222","capacity":1},{"node_id":"33333333333333333333333333333333","capacity":1}]`,
	}
	index, err := stores[0].AppendCommand(vote.Term, command)
	if err != nil {
		t.Fatal(err)
	}
	entry := RaftEntry{Term: vote.Term, Command: command}
	acknowledgements := 1
	for _, follower := range stores[1:] {
		response, err := follower.AppendEntries(AppendEntriesRequest{Term: vote.Term, LeaderID: ids[0], Entries: []RaftEntry{entry}})
		if err != nil {
			t.Fatal(err)
		}
		if response.Success {
			acknowledgements++
		}
	}
	if err := stores[0].CommitWithQuorum(index, vote.Term, acknowledgements, len(stores)); err != nil {
		t.Fatal(err)
	}
	for _, follower := range stores[1:] {
		if _, err := follower.AppendEntries(AppendEntriesRequest{Term: vote.Term, LeaderID: ids[0], PrevLogIndex: 1, PrevLogTerm: vote.Term, LeaderCommit: index}); err != nil {
			t.Fatal(err)
		}
	}
	for node := range stores {
		_, committed, epoch := stores[node].State()
		if committed != 1 || epoch != 2 {
			t.Fatalf("node %d commit=%d epoch=%d", node, committed, epoch)
		}
		reopened, err := OpenRaftStore(paths[node], ids[node], 1)
		if err != nil {
			t.Fatal(err)
		}
		term, committed, epoch := reopened.State()
		if term != vote.Term || committed != 1 || epoch != 2 {
			t.Fatalf("reopened node %d term=%d commit=%d epoch=%d", node, term, committed, epoch)
		}
		view, exists := reopened.CommittedView()
		if !exists || view.Membership != command.MembershipDigest || view.Catalog != command.CatalogDigest || view.Placement != command.PlacementDigest || view.CapacityManifest != command.CapacityManifest {
			t.Fatalf("reopened node %d committed view=%#v exists=%v", node, view, exists)
		}
	}
}

func TestRaftRejectsStaleCandidatesMinorityCommitAndCommittedOverwrite(t *testing.T) {
	path := t.TempDir()
	nodeID := "11111111111111111111111111111111"
	store, err := OpenRaftStore(path, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	vote, err := store.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	index, err := store.AppendCommand(vote.Term, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithQuorum(index, vote.Term, 1, 3); err == nil {
		t.Fatal("minority committed metadata")
	}
	retryIndex, err := store.AppendCommand(vote.Term, command)
	if err != nil || retryIndex != index {
		t.Fatalf("proposal retry index=%d want=%d err=%v", retryIndex, index, err)
	}
	if err := store.CommitWithQuorum(index, vote.Term, 2, 3); err != nil {
		t.Fatal(err)
	}
	conflictingCommand := command
	conflictingCommand.PlacementDigest = strings.Repeat("d", 64)
	conflictingIndex, err := store.AppendCommand(vote.Term, conflictingCommand)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithQuorum(conflictingIndex, vote.Term, 2, 3); err == nil {
		t.Fatal("committed conflicting digest tuple for duplicate epoch")
	}
	if _, committed, epoch := store.State(); committed != index || epoch != 2 {
		t.Fatalf("failed commit changed state: commit=%d epoch=%d", committed, epoch)
	}
	response, err := store.RequestVote(RequestVoteRequest{Term: vote.Term, CandidateID: "22222222222222222222222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	if response.VoteGranted {
		t.Fatal("granted vote to stale term")
	}
	conflict := RaftEntry{Term: vote.Term + 1, Command: MetadataCommand{Type: "advance_epoch", Epoch: 3, MembershipDigest: strings.Repeat("d", 64), CatalogDigest: strings.Repeat("e", 64), PlacementDigest: strings.Repeat("f", 64)}}
	if _, err := store.AppendEntries(AppendEntriesRequest{Term: vote.Term + 1, LeaderID: "22222222222222222222222222222222", Entries: []RaftEntry{conflict}}); err == nil {
		t.Fatal("overwrote committed metadata")
	}
}

func TestRaftRejectsCandidateWithStaleLog(t *testing.T) {
	store, err := OpenRaftStore(t.TempDir(), "11111111111111111111111111111111", 1)
	if err != nil {
		t.Fatal(err)
	}
	vote, err := store.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	if _, err := store.AppendCommand(vote.Term, command); err != nil {
		t.Fatal(err)
	}
	response, err := store.RequestVote(RequestVoteRequest{Term: vote.Term + 1, CandidateID: "22222222222222222222222222222222"})
	if err != nil {
		t.Fatal(err)
	}
	if response.VoteGranted {
		t.Fatal("granted vote to candidate with stale log")
	}
}

func TestRaftPersistsImmutableFixedVoterSet(t *testing.T) {
	path := t.TempDir()
	local := "11111111111111111111111111111111"
	peer := "22222222222222222222222222222222"
	store, err := OpenRaftStore(path, local, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureVoters([]string{peer, local}); err != nil {
		t.Fatal(err)
	}
	if got := store.Voters(); len(got) != 2 || got[0] != local || got[1] != peer {
		t.Fatalf("voters=%v", got)
	}
	if err := store.ConfigureVoters([]string{local}); err == nil {
		t.Fatal("changed fixed voter set")
	}
	reopened, err := OpenRaftStore(path, local, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Voters(); len(got) != 2 || got[0] != local || got[1] != peer {
		t.Fatalf("reopened voters=%v", got)
	}
	outsider := "33333333333333333333333333333333"
	vote, err := reopened.RequestVote(RequestVoteRequest{Term: 4, CandidateID: outsider})
	if err != nil {
		t.Fatal(err)
	}
	if vote.VoteGranted {
		t.Fatal("granted vote to non-voter")
	}
	appendResponse, err := reopened.AppendEntries(AppendEntriesRequest{Term: 4, LeaderID: outsider})
	if err != nil {
		t.Fatal(err)
	}
	if appendResponse.Success {
		t.Fatal("accepted leader outside persisted voter set")
	}
}

func TestRaftCompactsCommittedPrefixWithAbsoluteIndexes(t *testing.T) {
	path := t.TempDir()
	nodeID := "11111111111111111111111111111111"
	store, err := OpenRaftStore(path, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	vote, err := store.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	index, err := store.AppendCommand(vote.Term, command)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithQuorum(index, vote.Term, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CompactCommitted(); err != nil {
		t.Fatal(err)
	}
	_, commitIndex, lastIndex, lastTerm, entries := store.LogSnapshot()
	if commitIndex != 1 || lastIndex != 1 || lastTerm != vote.Term || len(entries) != 0 {
		t.Fatalf("compacted commit=%d last=%d/%d entries=%d", commitIndex, lastIndex, lastTerm, len(entries))
	}
	reopened, err := OpenRaftStore(path, nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	vote, err = reopened.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	command = MetadataCommand{Type: "advance_epoch", Epoch: 3, MembershipDigest: strings.Repeat("d", 64), CatalogDigest: strings.Repeat("e", 64), PlacementDigest: strings.Repeat("f", 64)}
	index, err = reopened.AppendCommand(vote.Term, command)
	if err != nil || index != 2 {
		t.Fatalf("post-snapshot index=%d err=%v", index, err)
	}
	if err := reopened.CommitWithQuorum(index, vote.Term, 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, committed, epoch := reopened.State(); committed != 2 || epoch != 3 {
		t.Fatalf("post-snapshot commit=%d epoch=%d", committed, epoch)
	}
}
