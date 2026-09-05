package cluster

import (
	"strings"
	"testing"
)

func TestJointConsensusRequiresBothMajoritiesAndPersistsFinalize(t *testing.T) {
	ids := []string{
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
		"33333333333333333333333333333333",
		"44444444444444444444444444444444",
	}
	path := t.TempDir()
	store, err := OpenRaftStore(path, ids[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	oldVoters := append([]string(nil), ids[:3]...)
	newVoters := append([]string(nil), ids...)
	if err := store.ConfigureVoters(oldVoters); err != nil {
		t.Fatal(err)
	}
	vote, err := store.BeginElection()
	if err != nil {
		t.Fatal(err)
	}
	begin := MetadataCommand{Type: "begin_joint_consensus", Epoch: 1, OldVoters: oldVoters, NewVoters: newVoters}
	index, err := store.AppendCommand(vote.Term, begin)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithVoterAcks(index, vote.Term, ids[:2]); err != nil {
		t.Fatalf("old configuration must commit joint entry: %v", err)
	}
	stable, jointOld, jointNew := store.VoterConfiguration()
	if !equalStrings(stable, oldVoters) || !equalStrings(jointOld, oldVoters) || !equalStrings(jointNew, newVoters) {
		t.Fatalf("unexpected joint state stable=%v old=%v new=%v", stable, jointOld, jointNew)
	}
	if err := store.CompactCommitted(); err == nil {
		t.Fatal("joint configuration must not be compacted")
	}
	store, err = OpenRaftStore(path, ids[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	stable, jointOld, jointNew = store.VoterConfiguration()
	if !equalStrings(stable, oldVoters) || !equalStrings(jointOld, oldVoters) || !equalStrings(jointNew, newVoters) {
		t.Fatalf("restart lost joint state stable=%v old=%v new=%v", stable, jointOld, jointNew)
	}
	finalize := MetadataCommand{Type: "finalize_joint_consensus", Epoch: 2, OldVoters: oldVoters, NewVoters: newVoters, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	finalIndex, err := store.AppendCommand(vote.Term, finalize)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitWithVoterAcks(finalIndex, vote.Term, []string{ids[0], ids[1]}); err == nil {
		t.Fatal("old majority without new majority finalized membership")
	}
	if err := store.CommitWithVoterAcks(finalIndex, vote.Term, []string{ids[0], ids[1], ids[3]}); err != nil {
		t.Fatal(err)
	}
	stable, jointOld, jointNew = store.VoterConfiguration()
	if !equalStrings(stable, newVoters) || len(jointOld) != 0 || len(jointNew) != 0 {
		t.Fatalf("unexpected finalized state stable=%v old=%v new=%v", stable, jointOld, jointNew)
	}
	reopened, err := OpenRaftStore(path, ids[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	stable, jointOld, jointNew = reopened.VoterConfiguration()
	if !equalStrings(stable, newVoters) || len(jointOld) != 0 || len(jointNew) != 0 {
		t.Fatalf("restart lost finalized membership stable=%v old=%v new=%v", stable, jointOld, jointNew)
	}
}

func TestJointConsensusValidationRejectsMultiNodeJump(t *testing.T) {
	entry := RaftEntry{Term: 1, Command: MetadataCommand{Type: "begin_joint_consensus", Epoch: 1, OldVoters: []string{"11111111111111111111111111111111"}, NewVoters: []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}}}
	if err := validateRaftEntry(entry); err == nil {
		t.Fatal("expected multi-node membership jump rejection")
	}
}
