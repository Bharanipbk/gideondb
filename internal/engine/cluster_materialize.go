package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/vectordb/vectordb/internal/cluster"
	"github.com/vectordb/vectordb/internal/core"
)

// MaterializeClusterRestore builds fresh target node data paths from a restore
// plan and an already verified RestoreClusterBackup directory.
func MaterializeClusterRestore(restoredClusterPath, destination string, plan ClusterRestorePlan) error {
	if len(plan.Assignments) == 0 || plan.BackupEpoch == 0 || plan.TargetEpoch == 0 || !validRecoveryDigest(plan.BackupPlacementDigest) {
		return fmt.Errorf("non-empty fenced cluster restore plan is required")
	}
	if _, err := cluster.ParseCapacityManifest(plan.CapacityManifest); err != nil {
		return fmt.Errorf("invalid restore plan capacity manifest: %w", err)
	}
	seenShards := map[string]struct{}{}
	for _, assignment := range plan.Assignments {
		key := fmt.Sprintf("%s\x00%d", assignment.Collection, assignment.ShardID)
		if assignment.Collection == "" || !cluster.ValidNodeID(assignment.SourceNodeID) {
			return fmt.Errorf("invalid restore assignment for %s/%d", assignment.Collection, assignment.ShardID)
		}
		if _, duplicate := seenShards[key]; duplicate {
			return fmt.Errorf("duplicate restore assignment for %s/%d", assignment.Collection, assignment.ShardID)
		}
		seenShards[key] = struct{}{}
		if len(assignment.TargetReplicas) == 0 || assignment.TargetLeaderID != assignment.TargetReplicas[0] {
			return fmt.Errorf("invalid target replica set for %s/%d", assignment.Collection, assignment.ShardID)
		}
		replicas := map[string]struct{}{}
		for _, targetID := range assignment.TargetReplicas {
			if !cluster.ValidNodeID(targetID) {
				return fmt.Errorf("invalid target node identity for %s/%d", assignment.Collection, assignment.ShardID)
			}
			if _, duplicate := replicas[targetID]; duplicate {
				return fmt.Errorf("duplicate target node for %s/%d", assignment.Collection, assignment.ShardID)
			}
			replicas[targetID] = struct{}{}
		}
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("restore materialization destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".vectordb-remap-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)

	sources := map[string]*Engine{}
	defer func() {
		for _, source := range sources {
			_ = source.Close()
		}
	}()
	var configs []core.CollectionConfig
	for _, assignment := range plan.Assignments {
		if _, exists := sources[assignment.SourceNodeID]; exists {
			continue
		}
		sourcePath := filepath.Join(restoredClusterPath, "nodes", assignment.SourceNodeID)
		payload, err := os.ReadFile(filepath.Join(sourcePath, RestoredRecoveryPointFile))
		if err != nil {
			return fmt.Errorf("read source provenance %s: %w", assignment.SourceNodeID, err)
		}
		var point NodeRecoveryPoint
		if err := json.Unmarshal(payload, &point); err != nil || point.NodeID != assignment.SourceNodeID || point.MetadataEpoch != plan.BackupEpoch || point.PlacementDigest != plan.BackupPlacementDigest || point.CapacityManifest != plan.CapacityManifest {
			return fmt.Errorf("source provenance does not match restore plan")
		}
		source, err := Open(sourcePath)
		if err != nil {
			return fmt.Errorf("open restore source %s: %w", assignment.SourceNodeID, err)
		}
		sources[assignment.SourceNodeID] = source
		candidateConfigs := source.ListCollections()
		if configs == nil {
			configs = candidateConfigs
		} else if !reflect.DeepEqual(configs, candidateConfigs) {
			return fmt.Errorf("restore source collection catalogs differ")
		}
	}

	targetIDs := map[string]struct{}{}
	ownership := map[string]map[string][]uint32{}
	for _, assignment := range plan.Assignments {
		if !sourcePointContains(restoredClusterPath, assignment) {
			return fmt.Errorf("source recovery point does not contain %s/%d at sequence %d", assignment.Collection, assignment.ShardID, assignment.SourceSequence)
		}
		for _, targetID := range assignment.TargetReplicas {
			targetIDs[targetID] = struct{}{}
			if ownership[targetID] == nil {
				ownership[targetID] = map[string][]uint32{}
			}
			ownership[targetID][assignment.Collection] = append(ownership[targetID][assignment.Collection], assignment.ShardID)
		}
	}
	orderedTargets := make([]string, 0, len(targetIDs))
	for targetID := range targetIDs {
		orderedTargets = append(orderedTargets, targetID)
	}
	sort.Strings(orderedTargets)
	for _, targetID := range orderedTargets {
		for _, config := range configs {
			if _, exists := ownership[targetID][config.Name]; !exists {
				ownership[targetID][config.Name] = []uint32{}
			}
		}
		target, err := Open(filepath.Join(staging, "nodes", targetID))
		if err != nil {
			return err
		}
		for _, config := range configs {
			if err := target.CreateCollection(config); err != nil {
				_ = target.Close()
				return err
			}
		}
		if err := target.ApplyShardOwnership(ownership[targetID]); err != nil {
			_ = target.Close()
			return err
		}
		for _, assignment := range plan.Assignments {
			if !containsTarget(assignment.TargetReplicas, targetID) {
				continue
			}
			sequence, records, err := sources[assignment.SourceNodeID].ExportReplicaSnapshot(assignment.Collection, assignment.ShardID)
			if err != nil || sequence != assignment.SourceSequence {
				_ = target.Close()
				return fmt.Errorf("source shard sequence changed for %s/%d", assignment.Collection, assignment.ShardID)
			}
			if sequence == 0 {
				if len(records) != 0 {
					_ = target.Close()
					return fmt.Errorf("zero-sequence source shard %s/%d contains records", assignment.Collection, assignment.ShardID)
				}
				continue
			}
			if err := target.InstallReplicaSnapshot(assignment.Collection, assignment.ShardID, sequence, records); err != nil {
				_ = target.Close()
				return err
			}
		}
		if err := target.Close(); err != nil {
			return err
		}
	}
	for _, source := range sources {
		if err := source.Close(); err != nil {
			return err
		}
	}
	sources = map[string]*Engine{}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func sourcePointContains(root string, assignment ShardRestoreAssignment) bool {
	payload, err := os.ReadFile(filepath.Join(root, "nodes", assignment.SourceNodeID, RestoredRecoveryPointFile))
	if err != nil {
		return false
	}
	var point NodeRecoveryPoint
	if json.Unmarshal(payload, &point) != nil {
		return false
	}
	for _, shard := range point.Shards {
		if shard.Collection == assignment.Collection && shard.ShardID == assignment.ShardID && shard.Sequence == assignment.SourceSequence {
			return true
		}
	}
	return false
}

func containsTarget(targets []string, wanted string) bool {
	for _, target := range targets {
		if target == wanted {
			return true
		}
	}
	return false
}
