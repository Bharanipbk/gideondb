package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const RebalanceStateFile = "cluster-rebalance.json"

type RebalanceDataMover interface {
	CopySnapshot(context.Context, ShardMovement, string) error
	CatchUpWAL(context.Context, ShardMovement, string) error
}

type RebalancePreparation struct {
	Format          int      `json:"format"`
	PlanDigest      string   `json:"plan_digest"`
	FromEpoch       uint64   `json:"from_epoch"`
	ToEpoch         uint64   `json:"to_epoch"`
	Completed       []string `json:"completed"`
	ReadyForCutover bool     `json:"ready_for_cutover"`
}

// RebalanceExecutor durably executes only the data-copy prerequisites of a
// plan. Leadership transfer, placement commit, and replica removal remain
// outside this component so a partial preparation can never imply cutover.
type RebalanceExecutor struct {
	mu    sync.Mutex
	path  string
	state RebalancePreparation
}

func OpenRebalanceExecutor(dataPath string) (*RebalanceExecutor, error) {
	executor := &RebalanceExecutor{path: filepath.Join(dataPath, RebalanceStateFile)}
	file, err := os.Open(executor.path)
	if errors.Is(err, os.ErrNotExist) {
		return executor, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&executor.state); err != nil {
		return nil, fmt.Errorf("decode rebalance state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("rebalance state must contain one JSON value")
	}
	if err := validatePreparation(executor.state); err != nil {
		return nil, err
	}
	return executor, nil
}

func (e *RebalanceExecutor) Status() (RebalancePreparation, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Format == 0 {
		return RebalancePreparation{}, false
	}
	return clonePreparation(e.state), true
}

func (e *RebalanceExecutor) Ready(plan RebalancePlan) (bool, error) {
	digest, _, err := validateExecutionPlan(plan)
	if err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Format == 1 && e.state.PlanDigest == digest && e.state.ReadyForCutover, nil
}

func (e *RebalanceExecutor) Prepare(ctx context.Context, plan RebalancePlan, mover RebalanceDataMover) (RebalancePreparation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if mover == nil {
		return RebalancePreparation{}, fmt.Errorf("rebalance data mover is required")
	}
	digest, required, err := validateExecutionPlan(plan)
	if err != nil {
		return RebalancePreparation{}, err
	}
	if e.state.Format == 0 {
		e.state = RebalancePreparation{Format: 1, PlanDigest: digest, FromEpoch: plan.FromEpoch, ToEpoch: plan.ToEpoch, Completed: []string{}}
		if err := e.persistLocked(); err != nil {
			e.state = RebalancePreparation{}
			return RebalancePreparation{}, err
		}
	} else if e.state.PlanDigest != digest || e.state.FromEpoch != plan.FromEpoch || e.state.ToEpoch != plan.ToEpoch {
		return clonePreparation(e.state), fmt.Errorf("a different rebalance plan is already prepared or in progress")
	}
	completed := stringSet(e.state.Completed)
	for _, movement := range plan.Movements {
		for _, action := range movement.Actions {
			if action.Type != "copy_snapshot" && action.Type != "catch_up_wal" {
				continue
			}
			if _, done := completed[action.ID]; done {
				continue
			}
			for _, dependency := range action.Requires {
				if _, done := completed[dependency]; !done {
					return clonePreparation(e.state), fmt.Errorf("rebalance action %s has incomplete dependency %s", action.ID, dependency)
				}
			}
			if err := ctx.Err(); err != nil {
				return clonePreparation(e.state), err
			}
			if action.Type == "copy_snapshot" {
				err = mover.CopySnapshot(ctx, movement, action.TargetID)
			} else {
				err = mover.CatchUpWAL(ctx, movement, action.TargetID)
			}
			if err != nil {
				return clonePreparation(e.state), fmt.Errorf("%s: %w", action.ID, err)
			}
			completed[action.ID] = struct{}{}
			e.state.Completed = append(e.state.Completed, action.ID)
			if err := e.persistLocked(); err != nil {
				return clonePreparation(e.state), err
			}
		}
	}
	e.state.ReadyForCutover = true
	for actionID := range required {
		if _, done := completed[actionID]; !done {
			e.state.ReadyForCutover = false
			break
		}
	}
	if err := e.persistLocked(); err != nil {
		return clonePreparation(e.state), err
	}
	return clonePreparation(e.state), nil
}

// ResetCommitted clears a completed preparation only after its target epoch is
// known to be committed, allowing the next plan to start.
func (e *RebalanceExecutor) ResetCommitted(committedEpoch uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Format == 0 {
		return nil
	}
	if !e.state.ReadyForCutover || committedEpoch < e.state.ToEpoch {
		return fmt.Errorf("rebalance preparation has not reached committed cutover")
	}
	if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	e.state = RebalancePreparation{}
	return nil
}

// Abort clears the exact uncommitted plan after its source barriers have been
// released by the coordinator.
func (e *RebalanceExecutor) Abort(plan RebalancePlan) error {
	digest, _, err := validateExecutionPlan(plan)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Format == 0 {
		return nil
	}
	if e.state.PlanDigest != digest {
		return fmt.Errorf("active rebalance preparation does not match abort plan")
	}
	if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	e.state = RebalancePreparation{}
	return nil
}

func validateExecutionPlan(plan RebalancePlan) (string, map[string]struct{}, error) {
	if plan.FromEpoch == 0 || plan.ToEpoch <= plan.FromEpoch || plan.Authoritative {
		return "", nil, fmt.Errorf("valid non-authoritative next-epoch rebalance plan is required")
	}
	all := make(map[string]struct{})
	required := make(map[string]struct{})
	for _, movement := range plan.Movements {
		for _, action := range movement.Actions {
			if action.ID == "" {
				return "", nil, fmt.Errorf("rebalance action ID is required")
			}
			if _, duplicate := all[action.ID]; duplicate {
				return "", nil, fmt.Errorf("duplicate rebalance action ID %s", action.ID)
			}
			all[action.ID] = struct{}{}
			if action.Type == "copy_snapshot" || action.Type == "catch_up_wal" {
				if !ValidNodeID(action.SourceID) || !ValidNodeID(action.TargetID) || action.SourceID == action.TargetID {
					return "", nil, fmt.Errorf("invalid data movement action %s", action.ID)
				}
				required[action.ID] = struct{}{}
			}
		}
	}
	for _, movement := range plan.Movements {
		for _, action := range movement.Actions {
			for _, dependency := range action.Requires {
				if _, exists := all[dependency]; !exists {
					return "", nil, fmt.Errorf("rebalance action %s has unknown dependency %s", action.ID, dependency)
				}
			}
		}
	}
	payload, err := json.Marshal(plan)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), required, nil
}

// RebalancePlanDigest returns the canonical identity used by preparation
// journals and write barriers.
func RebalancePlanDigest(plan RebalancePlan) (string, error) {
	digest, _, err := validateExecutionPlan(plan)
	return digest, err
}

func validatePreparation(state RebalancePreparation) error {
	if state.Format != 1 || !validDigest(state.PlanDigest) || state.FromEpoch == 0 || state.ToEpoch <= state.FromEpoch {
		return fmt.Errorf("invalid rebalance state")
	}
	seen := make(map[string]struct{}, len(state.Completed))
	for _, actionID := range state.Completed {
		if actionID == "" {
			return fmt.Errorf("invalid completed rebalance action")
		}
		if _, duplicate := seen[actionID]; duplicate {
			return fmt.Errorf("duplicate completed rebalance action")
		}
		seen[actionID] = struct{}{}
	}
	return nil
}

func clonePreparation(state RebalancePreparation) RebalancePreparation {
	state.Completed = append([]string(nil), state.Completed...)
	return state
}

func (e *RebalanceExecutor) persistLocked() error {
	payload, err := json.Marshal(e.state)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(e.path), ".cluster-rebalance-*")
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
	if err := os.Rename(temporaryPath, e.path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(filepath.Dir(e.path))
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		_ = directory.Close()
		return syncErr
	}
	return nil
}
