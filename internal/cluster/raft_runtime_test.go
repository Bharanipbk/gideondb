package cluster

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryRaftTransport struct {
	mu      sync.RWMutex
	nodes   map[string]*RaftRuntime
	blocked map[string]bool
	links   map[string]bool
}

func (m *memoryRaftTransport) RequestVote(_ context.Context, peer RaftPeer, request RequestVoteRequest) (RequestVoteResponse, error) {
	m.mu.RLock()
	blocked := m.blocked[peer.NodeID]
	linkBlocked := m.links[request.CandidateID+"->"+peer.NodeID]
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if blocked || linkBlocked {
		return RequestVoteResponse{}, fmt.Errorf("peer unavailable")
	}
	if node == nil {
		return RequestVoteResponse{}, fmt.Errorf("peer unavailable")
	}
	return node.RequestVote(request)
}

func (m *memoryRaftTransport) AppendEntries(_ context.Context, peer RaftPeer, request AppendEntriesRequest) (AppendEntriesResponse, error) {
	m.mu.RLock()
	blocked := m.blocked[peer.NodeID]
	linkBlocked := m.links[request.LeaderID+"->"+peer.NodeID]
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if blocked || linkBlocked {
		return AppendEntriesResponse{}, fmt.Errorf("peer unavailable")
	}
	if node == nil {
		return AppendEntriesResponse{}, fmt.Errorf("peer unavailable")
	}
	return node.AppendEntries(request)
}

func (m *memoryRaftTransport) InstallSnapshot(_ context.Context, peer RaftPeer, request InstallSnapshotRequest) (InstallSnapshotResponse, error) {
	m.mu.RLock()
	blocked := m.blocked[peer.NodeID]
	linkBlocked := m.links[request.LeaderID+"->"+peer.NodeID]
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if blocked || linkBlocked || node == nil {
		return InstallSnapshotResponse{}, fmt.Errorf("peer unavailable")
	}
	return node.InstallSnapshot(request)
}

func (m *memoryRaftTransport) TimeoutNow(ctx context.Context, peer RaftPeer, request TimeoutNowRequest) (TimeoutNowResponse, error) {
	m.mu.RLock()
	blocked := m.blocked[peer.NodeID]
	node := m.nodes[peer.NodeID]
	m.mu.RUnlock()
	if blocked || node == nil {
		return TimeoutNowResponse{}, fmt.Errorf("peer unavailable")
	}
	return node.TimeoutNow(ctx, request)
}

func TestRaftRuntimeElectsLeaderSendsHeartbeatsAndStepsDown(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, len(ids))
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		localID := id
		peers := func() []RaftPeer {
			result := make([]RaftPeer, 0, 2)
			for _, candidate := range ids {
				if candidate != localID {
					result = append(result, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return result
		}
		runtime, err := NewRaftRuntime(store, id, peers, transport, RaftRuntimeConfig{ElectionMin: 300 * time.Millisecond, ElectionMax: 500 * time.Millisecond, Heartbeat: 20 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[index] = runtime
		transport.nodes[id] = runtime
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, len(runtimes))
	for _, runtime := range runtimes {
		go func(runtime *RaftRuntime) {
			runtime.Run(ctx)
			done <- struct{}{}
		}(runtime)
	}
	t.Cleanup(func() {
		cancel()
		for range runtimes {
			<-done
		}
	})
	var leader *RaftRuntime
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		leaders := 0
		for _, runtime := range runtimes {
			role, _, _ := runtime.Status()
			if role == RaftLeader {
				leader = runtime
				leaders++
			}
		}
		if leaders == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if leader == nil {
		t.Fatal("cluster did not elect a leader")
	}
	time.Sleep(3 * 20 * time.Millisecond)
	leader = nil
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		leaders := 0
		for _, runtime := range runtimes {
			role, leaderID, _ := runtime.Status()
			if role == RaftLeader {
				leader, leaders = runtime, leaders+1
				if leaderID == "" {
					t.Fatal("leader does not identify itself")
				}
			}
		}
		if leaders == 1 {
			break
		}
		leader = nil
		time.Sleep(5 * time.Millisecond)
	}
	if leader == nil {
		t.Fatal("cluster did not retain a leader after heartbeats")
	}
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	proposalCtx, proposalCancel := context.WithTimeout(ctx, time.Second)
	if epoch, err := leader.Propose(proposalCtx, command); err != nil || epoch != 2 {
		proposalCancel()
		t.Fatalf("proposal epoch=%d err=%v", epoch, err)
	}
	proposalCancel()
	for index, runtime := range runtimes {
		_, _, epoch := runtime.store.State()
		if epoch != 2 {
			t.Fatalf("node %d epoch=%d after catch-up", index, epoch)
		}
	}
	view := ViewDigests{Membership: strings.Repeat("d", 64), Catalog: strings.Repeat("e", 64), Placement: strings.Repeat("f", 64), CapacityManifest: `[{"node_id":"11111111111111111111111111111111","capacity":2}]`}
	proposalCtx, proposalCancel = context.WithTimeout(ctx, time.Second)
	if epoch, err := leader.AdvanceView(proposalCtx, view); err != nil || epoch != 3 {
		proposalCancel()
		t.Fatalf("view advance epoch=%d err=%v", epoch, err)
	}
	proposalCancel()
	for index, runtime := range runtimes {
		_, _, epoch := runtime.store.State()
		committed, exists := runtime.store.CommittedView()
		if epoch != 3 || !exists || committed != view {
			t.Fatalf("node %d epoch=%d view=%#v exists=%v after same-voter advance", index, epoch, committed, exists)
		}
	}
	_, _, term := leader.Status()
	response, err := leader.AppendEntries(AppendEntriesRequest{Term: term + 1, LeaderID: ids[1]})
	if err != nil || !response.Success {
		t.Fatalf("higher-term append response=%#v err=%v", response, err)
	}
	if role, _, observedTerm := leader.Status(); role != RaftFollower || observedTerm != term+1 {
		t.Fatalf("role=%s term=%d", role, observedTerm)
	}
}

func TestRaftRuntimeTransfersLeadershipToCaughtUpVoter(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, len(ids))
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ConfigureVoters(ids); err != nil {
			t.Fatal(err)
		}
		localID := id
		runtime, err := NewRaftRuntime(store, id, func() []RaftPeer {
			result := make([]RaftPeer, 0, 2)
			for _, candidate := range ids {
				if candidate != localID {
					result = append(result, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return result
		}, transport, RaftRuntimeConfig{})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[index], transport.nodes[id] = runtime, runtime
	}
	runtimes[0].campaign(context.Background())
	if role, _, _ := runtimes[0].Status(); role != RaftLeader {
		t.Fatalf("initial role=%s", role)
	}
	if err := runtimes[0].TransferLeadership(context.Background()); err != nil {
		t.Fatal(err)
	}
	if role, _, _ := runtimes[0].Status(); role != RaftFollower {
		t.Fatalf("old leader role=%s", role)
	}
	if role, _, _ := runtimes[1].Status(); role != RaftLeader {
		t.Fatalf("canonical target role=%s", role)
	}
}

func TestRaftRuntimeCommitsJointConsensusJoin(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333", "44444444444444444444444444444444"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, len(ids))
	stores := make([]*RaftStore, len(ids))
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if index < 3 {
			if err := store.ConfigureVoters(ids[:3]); err != nil {
				t.Fatal(err)
			}
		}
		localID := id
		runtime, err := NewRaftRuntime(store, id, func() []RaftPeer {
			result := make([]RaftPeer, 0, len(ids)-1)
			for _, candidate := range ids {
				if candidate != localID {
					result = append(result, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return result
		}, transport, RaftRuntimeConfig{ElectionMin: 300 * time.Millisecond, ElectionMax: 500 * time.Millisecond, Heartbeat: 50 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		stores[index], runtimes[index], transport.nodes[id] = store, runtime, runtime
	}
	runtimes[0].campaign(context.Background())
	if role, _, _ := runtimes[0].Status(); role != RaftLeader {
		t.Fatalf("role=%s", role)
	}
	view := ViewDigests{Membership: strings.Repeat("a", 64), Catalog: strings.Repeat("b", 64), Placement: strings.Repeat("c", 64)}
	epoch, err := runtimes[0].ChangeVoters(context.Background(), ids, view)
	if err != nil || epoch != 2 {
		t.Fatalf("epoch=%d err=%v", epoch, err)
	}
	for index, store := range stores {
		_, _, appliedEpoch := store.State()
		stable, oldVoters, newVoters := store.VoterConfiguration()
		if appliedEpoch != 2 || !equalStrings(stable, ids) || len(oldVoters) != 0 || len(newVoters) != 0 {
			t.Fatalf("node %d epoch=%d stable=%v old=%v new=%v", index, appliedEpoch, stable, oldVoters, newVoters)
		}
	}
}

func TestRaftRuntimeDoesNotShrinkUndiscoveredVoterSet(t *testing.T) {
	nodeID := "11111111111111111111111111111111"
	store, err := OpenRaftStore(t.TempDir(), nodeID, 1)
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtime, err := NewRaftRuntime(store, nodeID, func() []RaftPeer {
		return []RaftPeer{{BaseURL: "http://not-discovered:6333"}, {BaseURL: "http://also-not-discovered:6333"}}
	}, transport, RaftRuntimeConfig{ElectionMin: 40 * time.Millisecond, ElectionMax: 60 * time.Millisecond, Heartbeat: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runtime.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	time.Sleep(200 * time.Millisecond)
	if role, _, _ := runtime.Status(); role == RaftLeader {
		t.Fatal("node elected itself by dropping undiscovered configured voters")
	}
}

func TestRaftRuntimeDemotesLeaderAndPreservesMajorityDuringPartition(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, 3)
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		localID := id
		runtime, err := NewRaftRuntime(store, id, func() []RaftPeer {
			peers := make([]RaftPeer, 0, 2)
			for _, candidate := range ids {
				if candidate != localID {
					peers = append(peers, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return peers
		}, transport, RaftRuntimeConfig{ElectionMin: 100 * time.Millisecond, ElectionMax: 220 * time.Millisecond, Heartbeat: 25 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[index] = runtime
		transport.nodes[id] = runtime
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 3)
	for _, runtime := range runtimes {
		go func(runtime *RaftRuntime) { runtime.Run(ctx); done <- struct{}{} }(runtime)
	}
	t.Cleanup(func() {
		cancel()
		for range runtimes {
			<-done
		}
	})
	leaderIndex := waitForSingleLeader(t, runtimes, 3*time.Second, -1)
	transport.mu.Lock()
	for index := range ids {
		if index == leaderIndex {
			continue
		}
		transport.links[ids[leaderIndex]+"->"+ids[index]] = true
		transport.links[ids[index]+"->"+ids[leaderIndex]] = true
	}
	transport.mu.Unlock()
	majorityLeader := waitForSingleLeader(t, runtimes, 3*time.Second, leaderIndex)
	if majorityLeader == leaderIndex {
		t.Fatal("isolated node remained the only leader")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		role, _, _ := runtimes[leaderIndex].Status()
		if role != RaftLeader {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if role, _, _ := runtimes[leaderIndex].Status(); role == RaftLeader {
		t.Fatal("leader did not demote after losing voter majority")
	}
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	if _, err := runtimes[leaderIndex].Propose(ctx, command); err == nil {
		t.Fatal("isolated former leader accepted proposal")
	}
	proposalDeadline := time.Now().Add(2 * time.Second)
	var proposalErr error
	for time.Now().Before(proposalDeadline) {
		majorityLeader = waitForSingleLeader(t, runtimes, time.Second, leaderIndex)
		proposalCtx, proposalCancel := context.WithTimeout(ctx, 300*time.Millisecond)
		epoch, err := runtimes[majorityLeader].Propose(proposalCtx, command)
		proposalCancel()
		if err == nil && epoch == 2 {
			proposalErr = nil
			break
		}
		committed := 0
		for index, runtime := range runtimes {
			if index != leaderIndex {
				_, _, applied := runtime.store.State()
				if applied == 2 {
					committed++
				}
			}
		}
		if committed == 2 {
			proposalErr = nil
			break
		}
		proposalErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if proposalErr != nil {
		t.Fatalf("majority proposal failed: %v", proposalErr)
	}
}

func TestRaftRuntimeRepeatedPartitionHealConvergesWithoutEpochRegression(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, len(ids))
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ConfigureVoters(ids); err != nil {
			t.Fatal(err)
		}
		localID := id
		runtime, err := NewRaftRuntime(store, id, func() []RaftPeer {
			peers := make([]RaftPeer, 0, 2)
			for _, candidate := range ids {
				if candidate != localID {
					peers = append(peers, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return peers
		}, transport, RaftRuntimeConfig{})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[index] = runtime
		transport.nodes[id] = runtime
	}
	ctx := context.Background()
	runtimes[0].campaign(ctx)
	leader := 0
	for cycle := 0; cycle < 6; cycle++ {
		minority := leader
		majorityLeader := (leader + 1) % len(ids)
		transport.mu.Lock()
		for index := range ids {
			if index == minority {
				continue
			}
			transport.links[ids[minority]+"->"+ids[index]] = true
			transport.links[ids[index]+"->"+ids[minority]] = true
		}
		transport.mu.Unlock()

		epoch := uint64(cycle + 2)
		minorityCommand := MetadataCommand{Type: "advance_epoch", Epoch: epoch, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
		if _, err := runtimes[minority].Propose(ctx, minorityCommand); err == nil {
			t.Fatalf("cycle %d isolated leader committed without quorum", cycle)
		}
		runtimes[majorityLeader].campaign(ctx)
		if role, _, _ := runtimes[majorityLeader].Status(); role != RaftLeader {
			t.Fatalf("cycle %d majority failed to elect leader: %s", cycle, role)
		}
		digit := string(rune('d' + cycle%3))
		majorityCommand := MetadataCommand{Type: "advance_epoch", Epoch: epoch, MembershipDigest: strings.Repeat(digit, 64), CatalogDigest: strings.Repeat("e", 64), PlacementDigest: strings.Repeat("f", 64)}
		if committed, err := runtimes[majorityLeader].Propose(ctx, majorityCommand); err != nil || committed != epoch {
			t.Fatalf("cycle %d majority commit=%d err=%v", cycle, committed, err)
		}

		transport.mu.Lock()
		transport.links = make(map[string]bool)
		transport.mu.Unlock()
		for attempt := 0; attempt < 3; attempt++ {
			runtimes[majorityLeader].broadcastHeartbeat(ctx)
		}
		for index, runtime := range runtimes {
			_, _, applied := runtime.store.State()
			view, exists := runtime.store.CommittedView()
			if applied != epoch || !exists || view.Membership != majorityCommand.MembershipDigest {
				t.Fatalf("cycle %d node %d epoch=%d view=%#v exists=%v", cycle, index, applied, view, exists)
			}
		}
		leader = majorityLeader
	}
}

func TestRaftRuntimeInstallsSnapshotOnLaggingFollower(t *testing.T) {
	ids := []string{"11111111111111111111111111111111", "22222222222222222222222222222222", "33333333333333333333333333333333"}
	transport := &memoryRaftTransport{nodes: make(map[string]*RaftRuntime), blocked: make(map[string]bool), links: make(map[string]bool)}
	runtimes := make([]*RaftRuntime, 3)
	for index, id := range ids {
		store, err := OpenRaftStore(t.TempDir(), id, 1)
		if err != nil {
			t.Fatal(err)
		}
		localID := id
		runtime, err := NewRaftRuntime(store, id, func() []RaftPeer {
			peers := make([]RaftPeer, 0, 2)
			for _, candidate := range ids {
				if candidate != localID {
					peers = append(peers, RaftPeer{NodeID: candidate, BaseURL: "memory://" + candidate})
				}
			}
			return peers
		}, transport, RaftRuntimeConfig{ElectionMin: time.Second, ElectionMax: 2 * time.Second, Heartbeat: 100 * time.Millisecond, SnapshotThreshold: 1})
		if err != nil {
			t.Fatal(err)
		}
		runtimes[index] = runtime
		transport.nodes[id] = runtime
	}
	ctx := context.Background()
	runtimes[0].campaign(ctx)
	if role, _, _ := runtimes[0].Status(); role != RaftLeader {
		t.Fatalf("role=%s", role)
	}
	transport.mu.Lock()
	transport.blocked[ids[2]] = true
	transport.mu.Unlock()
	command := MetadataCommand{Type: "advance_epoch", Epoch: 2, MembershipDigest: strings.Repeat("a", 64), CatalogDigest: strings.Repeat("b", 64), PlacementDigest: strings.Repeat("c", 64)}
	if epoch, err := runtimes[0].Propose(ctx, command); err != nil || epoch != 2 {
		t.Fatalf("proposal epoch=%d err=%v", epoch, err)
	}
	if snapshot, exists := runtimes[0].store.Snapshot(); !exists || snapshot.LastIncludedIndex != 1 || snapshot.AppliedEpoch != 2 {
		t.Fatalf("leader snapshot=%#v exists=%v", snapshot, exists)
	}
	if _, _, epoch := runtimes[2].store.State(); epoch != 1 {
		t.Fatalf("isolated follower epoch=%d", epoch)
	}
	transport.mu.Lock()
	transport.blocked[ids[2]] = false
	transport.mu.Unlock()
	runtimes[0].broadcastHeartbeat(ctx)
	if _, commitIndex, epoch := runtimes[2].store.State(); commitIndex != 1 || epoch != 2 {
		t.Fatalf("installed follower commit=%d epoch=%d", commitIndex, epoch)
	}
	if snapshot, exists := runtimes[2].store.Snapshot(); !exists || snapshot.LastIncludedIndex != 1 {
		t.Fatalf("follower snapshot=%#v exists=%v", snapshot, exists)
	}
}

func waitForSingleLeader(t *testing.T, runtimes []*RaftRuntime, timeout time.Duration, excluded int) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		leader := -1
		count := 0
		for index, runtime := range runtimes {
			if index == excluded {
				continue
			}
			if role, _, _ := runtime.Status(); role == RaftLeader {
				leader, count = index, count+1
			}
		}
		if count == 1 {
			return leader
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no single Raft leader elected")
	return -1
}
