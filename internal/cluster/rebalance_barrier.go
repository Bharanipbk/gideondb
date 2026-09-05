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

const RebalanceBarrierFile = "cluster-rebalance-barriers.json"

type RebalanceWriteBarrier struct {
	PlanDigest  string `json:"plan_digest"`
	Collection  string `json:"collection"`
	ShardID     uint32 `json:"shard_id"`
	TargetEpoch uint64 `json:"target_epoch"`
}

type rebalanceBarrierState struct {
	Format   int                     `json:"format"`
	Barriers []RebalanceWriteBarrier `json:"barriers"`
}

type RebalanceBarrierStore struct {
	mu    sync.Mutex
	path  string
	state rebalanceBarrierState
}

func OpenRebalanceBarrierStore(dataPath string) (*RebalanceBarrierStore, error) {
	store := &RebalanceBarrierStore{path: filepath.Join(dataPath, RebalanceBarrierFile), state: rebalanceBarrierState{Format: 1, Barriers: []RebalanceWriteBarrier{}}}
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&store.state); err != nil {
		return nil, fmt.Errorf("decode rebalance barriers: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rebalance barriers must contain one JSON value")
	}
	if store.state.Format != 1 {
		return nil, fmt.Errorf("invalid rebalance barrier format")
	}
	seen := map[string]struct{}{}
	for _, barrier := range store.state.Barriers {
		if !validDigest(barrier.PlanDigest) || barrier.Collection == "" || barrier.TargetEpoch < 2 {
			return nil, fmt.Errorf("invalid rebalance write barrier")
		}
		key := barrierKey(barrier.Collection, barrier.ShardID)
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate rebalance write barrier")
		}
		seen[key] = struct{}{}
	}
	return store, nil
}

func (s *RebalanceBarrierStore) Freeze(barrier RebalanceWriteBarrier) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDigest(barrier.PlanDigest) || barrier.Collection == "" || barrier.TargetEpoch < 2 {
		return fmt.Errorf("valid rebalance write barrier is required")
	}
	for _, existing := range s.state.Barriers {
		if existing.Collection == barrier.Collection && existing.ShardID == barrier.ShardID {
			if existing == barrier {
				return nil
			}
			return fmt.Errorf("shard already frozen by a different rebalance plan")
		}
	}
	s.state.Barriers = append(s.state.Barriers, barrier)
	sort.Slice(s.state.Barriers, func(i, j int) bool {
		return s.state.Barriers[i].Collection < s.state.Barriers[j].Collection || s.state.Barriers[i].Collection == s.state.Barriers[j].Collection && s.state.Barriers[i].ShardID < s.state.Barriers[j].ShardID
	})
	return s.persistLocked()
}

func (s *RebalanceBarrierStore) IsFrozen(collection string, shardID uint32) (RebalanceWriteBarrier, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, barrier := range s.state.Barriers {
		if barrier.Collection == collection && barrier.ShardID == shardID {
			return barrier, true
		}
	}
	return RebalanceWriteBarrier{}, false
}

func (s *RebalanceBarrierStore) ReleaseCommitted(committedEpoch uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	remaining := s.state.Barriers[:0]
	for _, barrier := range s.state.Barriers {
		if committedEpoch < barrier.TargetEpoch {
			remaining = append(remaining, barrier)
		}
	}
	s.state.Barriers = remaining
	return s.persistLocked()
}

func (s *RebalanceBarrierStore) ReleasePlan(planDigest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validDigest(planDigest) {
		return fmt.Errorf("valid rebalance plan digest is required")
	}
	remaining := s.state.Barriers[:0]
	for _, barrier := range s.state.Barriers {
		if barrier.PlanDigest != planDigest {
			remaining = append(remaining, barrier)
		}
	}
	s.state.Barriers = remaining
	return s.persistLocked()
}

func barrierKey(collection string, shardID uint32) string {
	return fmt.Sprintf("%s\x00%d", collection, shardID)
}

func (s *RebalanceBarrierStore) persistLocked() error {
	payload, err := json.Marshal(s.state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".cluster-rebalance-barriers-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
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
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(s.path))
		if err != nil {
			return err
		}
		err = directory.Sync()
		_ = directory.Close()
		return err
	}
	return nil
}
