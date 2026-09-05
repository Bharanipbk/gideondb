package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const OwnershipFile = "SHARD_OWNERSHIP.json"

type ownershipManifest struct {
	Format      int                 `json:"format"`
	Collections map[string][]uint32 `json:"collections"`
}

func loadOwnership(dataPath string) (map[string]map[uint32]struct{}, error) {
	file, err := os.Open(filepath.Join(dataPath, OwnershipFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var manifest ownershipManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode shard ownership: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("shard ownership must contain one JSON value")
	}
	if manifest.Format != 1 || manifest.Collections == nil {
		return nil, fmt.Errorf("invalid shard ownership manifest")
	}
	result := make(map[string]map[uint32]struct{}, len(manifest.Collections))
	for name, shardIDs := range manifest.Collections {
		if name == "" {
			return nil, fmt.Errorf("invalid empty collection in shard ownership")
		}
		owned := make(map[uint32]struct{}, len(shardIDs))
		for _, shardID := range shardIDs {
			if _, exists := owned[shardID]; exists {
				return nil, fmt.Errorf("duplicate shard %s/%d in ownership", name, shardID)
			}
			owned[shardID] = struct{}{}
		}
		result[name] = owned
	}
	return result, nil
}

func (e *Engine) shardOwned(collection string, shardID uint32) bool {
	if e.ownership == nil {
		return true
	}
	_, owned := e.ownership[collection][shardID]
	return owned
}

// ApplyShardOwnership persists immutable local ownership and removes empty
// disk artifacts for unowned shards. Any shard containing data fails closed.
func (e *Engine) ApplyShardOwnership(assignments map[string][]uint32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	requested := make(map[string]map[uint32]struct{}, len(assignments))
	manifest := ownershipManifest{Format: 1, Collections: make(map[string][]uint32, len(assignments))}
	if len(assignments) != len(e.collections) {
		return fmt.Errorf("ownership must cover every collection")
	}
	for name, collection := range e.collections {
		shardIDs, exists := assignments[name]
		if !exists {
			return fmt.Errorf("ownership missing collection %s", name)
		}
		owned := make(map[uint32]struct{}, len(shardIDs))
		for _, shardID := range shardIDs {
			if int(shardID) >= collection.Config().ShardCount {
				return fmt.Errorf("ownership shard %s/%d out of range", name, shardID)
			}
			if _, duplicate := owned[shardID]; duplicate {
				return fmt.Errorf("duplicate ownership shard %s/%d", name, shardID)
			}
			owned[shardID] = struct{}{}
		}
		requested[name] = owned
		manifest.Collections[name] = append([]uint32(nil), shardIDs...)
		sort.Slice(manifest.Collections[name], func(i, j int) bool { return manifest.Collections[name][i] < manifest.Collections[name][j] })
	}
	if e.ownership != nil {
		if ownershipEqual(e.ownership, requested) {
			return nil
		}
		return fmt.Errorf("shard ownership is immutable once activated")
	}
	for name, collection := range e.collections {
		for shardID := range collection.Config().ShardCount {
			if _, owned := requested[name][uint32(shardID)]; owned {
				continue
			}
			records, err := collection.ShardRecords(uint32(shardID))
			if err != nil {
				return err
			}
			if len(records) != 0 {
				return fmt.Errorf("refusing to prune non-empty shard %s/%d", name, shardID)
			}
			if log := e.logs[name][shardID]; log != nil && log.LastLSN() != 0 {
				return fmt.Errorf("refusing to prune shard %s/%d with WAL history", name, shardID)
			}
			entries, err := os.ReadDir(e.shardSegmentDir(name, uint32(shardID)))
			if err == nil && len(entries) != 0 {
				return fmt.Errorf("refusing to prune shard %s/%d with segment files", name, shardID)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(e.dataPath, ".shard-ownership-*")
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
	if err := os.Rename(temporaryPath, filepath.Join(e.dataPath, OwnershipFile)); err != nil {
		return err
	}
	if err := syncDirectory(e.dataPath); err != nil {
		return err
	}
	e.ownership = requested
	for name, collection := range e.collections {
		for shardID := range collection.Config().ShardCount {
			if _, owned := requested[name][uint32(shardID)]; owned {
				continue
			}
			if log := e.logs[name][shardID]; log != nil {
				if err := log.Close(); err != nil {
					return err
				}
				e.logs[name][shardID] = nil
			}
			walPath := filepath.Join(e.dataPath, "wal", name, fmt.Sprintf("shard-%06d.wal", shardID))
			if err := os.Remove(walPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.Remove(e.shardSegmentDir(name, uint32(shardID))); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func ownershipEqual(left, right map[string]map[uint32]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for name, shards := range left {
		other, exists := right[name]
		if !exists || len(shards) != len(other) {
			return false
		}
		for shardID := range shards {
			if _, exists := other[shardID]; !exists {
				return false
			}
		}
	}
	return true
}
