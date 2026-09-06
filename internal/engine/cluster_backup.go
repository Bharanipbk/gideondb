package engine

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const ClusterBackupManifestFile = "CLUSTER_BACKUP.json"

// CreateClusterBackup packages one recovery-point-bound archive per node.
func CreateClusterBackup(destination string, manifest ClusterRecoveryPoint, archives map[string]string) error {
	if err := validateClusterRecoveryManifest(manifest); err != nil {
		return err
	}
	if len(archives) != len(manifest.Nodes) {
		return fmt.Errorf("node archive set does not match recovery manifest")
	}
	for _, nodeID := range manifest.Nodes {
		if archives[nodeID] == "" {
			return fmt.Errorf("missing archive for node %s", nodeID)
		}
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("cluster backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".gideondb-cluster-backup-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	gzipWriter := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gzipWriter)
	payload, _ := json.Marshal(manifest)
	if err := writeTarBytes(tarWriter, ClusterBackupManifestFile, payload, 0o640); err != nil {
		return err
	}
	for _, nodeID := range manifest.Nodes {
		info, err := os.Stat(archives[nodeID])
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("invalid archive for node %s", nodeID)
		}
		if err := writeTarFile(tarWriter, "nodes/"+nodeID+".tar.gz", archives[nodeID], info); err != nil {
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

// RestoreClusterBackup validates every package layer before atomically
// publishing a directory containing one restored data path per original node.
func RestoreClusterBackup(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("cluster restore destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	archive, err := os.Open(source)
	if err != nil {
		return err
	}
	defer archive.Close()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".gideondb-cluster-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	reader := tar.NewReader(gzipReader)
	var manifest ClusterRecoveryPoint
	seenManifest := false
	archives := map[string]string{}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 {
			return fmt.Errorf("invalid cluster backup entry")
		}
		if header.Name == ClusterBackupManifestFile {
			if seenManifest || len(archives) != 0 {
				return fmt.Errorf("invalid cluster backup manifest placement")
			}
			payload, err := io.ReadAll(io.LimitReader(reader, 4<<20))
			if err != nil || int64(len(payload)) != header.Size || verifyBackupChecksum(header, payload) != nil || json.Unmarshal(payload, &manifest) != nil {
				return fmt.Errorf("invalid cluster backup manifest")
			}
			if err := validateClusterRecoveryManifest(manifest); err != nil {
				return err
			}
			seenManifest = true
			continue
		}
		if !seenManifest || !strings.HasPrefix(header.Name, "nodes/") || !strings.HasSuffix(header.Name, ".tar.gz") {
			return fmt.Errorf("unexpected cluster backup entry %q", header.Name)
		}
		nodeID := strings.TrimSuffix(strings.TrimPrefix(header.Name, "nodes/"), ".tar.gz")
		if _, wanted := archives[nodeID]; wanted || !containsNode(manifest.Nodes, nodeID) {
			return fmt.Errorf("duplicate or unknown node archive")
		}
		path := filepath.Join(staging, nodeID+".tar.gz")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || header.PAXRecords["VDB.sha256"] != hex.EncodeToString(hash.Sum(nil)) {
			return fmt.Errorf("node archive checksum mismatch")
		}
		archives[nodeID] = path
	}
	if !seenManifest || len(archives) != len(manifest.Nodes) {
		return fmt.Errorf("cluster backup is incomplete")
	}
	for _, nodeID := range manifest.Nodes {
		target := filepath.Join(staging, "nodes", nodeID)
		if err := RestoreBackup(archives[nodeID], target); err != nil {
			return fmt.Errorf("restore node %s: %w", nodeID, err)
		}
		payload, err := os.ReadFile(filepath.Join(target, RestoredRecoveryPointFile))
		var point NodeRecoveryPoint
		if err != nil || json.Unmarshal(payload, &point) != nil || !reflect.DeepEqual(point, nodePoint(manifest, nodeID)) {
			return fmt.Errorf("node %s recovery point does not match cluster manifest", nodeID)
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func validateClusterRecoveryManifest(manifest ClusterRecoveryPoint) error {
	points := make([]NodeRecoveryPoint, 0, len(manifest.Nodes))
	for _, nodeID := range manifest.Nodes {
		points = append(points, nodePoint(manifest, nodeID))
	}
	canonical, err := MergeRecoveryPoints(points)
	if err != nil || !reflect.DeepEqual(canonical, manifest) {
		return fmt.Errorf("cluster recovery manifest is not canonical")
	}
	return nil
}

func nodePoint(manifest ClusterRecoveryPoint, nodeID string) NodeRecoveryPoint {
	point := NodeRecoveryPoint{MetadataEpoch: manifest.MetadataEpoch, NodeID: nodeID, PlacementDigest: manifest.PlacementDigest, CapacityManifest: manifest.CapacityManifest, Shards: []ShardRecoveryPoint{}}
	for _, shard := range manifest.ShardRecovery {
		if shard.NodeID == nodeID {
			point.Shards = append(point.Shards, shard)
		}
	}
	return point
}

func containsNode(nodes []string, nodeID string) bool {
	for _, candidate := range nodes {
		if candidate == nodeID {
			return true
		}
	}
	return false
}
