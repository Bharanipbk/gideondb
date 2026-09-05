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
	"runtime"
	"strings"
	"time"

	"github.com/vectordb/vectordb/internal/cluster"
)

const backupFormat = 1

type backupHeader struct {
	Format        int                `json:"format"`
	CreatedAt     time.Time          `json:"created_at"`
	RecoveryPoint *NodeRecoveryPoint `json:"recovery_point,omitempty"`
}

const RestoredRecoveryPointFile = "BACKUP_RECOVERY_POINT.json"

// Backup creates a crash-consistent archive after checkpointing every shard.
// The destination must not already exist.
func (e *Engine) Backup(destination string) error {
	return e.backup(destination, nil)
}

// BackupAtRecoveryPoint creates an archive only if the coordinated shard
// sequence fence still exactly matches local durable state.
func (e *Engine) BackupAtRecoveryPoint(destination string, point NodeRecoveryPoint) error {
	return e.backup(destination, &point)
}

func (e *Engine) backup(destination string, recoveryPoint *NodeRecoveryPoint) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if recoveryPoint != nil {
		if recoveryPoint.MetadataEpoch == 0 || !cluster.ValidNodeID(recoveryPoint.NodeID) || !validRecoveryDigest(recoveryPoint.PlacementDigest) {
			return fmt.Errorf("invalid backup recovery point")
		}
		if _, err := cluster.ParseCapacityManifest(recoveryPoint.CapacityManifest); err != nil {
			return err
		}
		current := e.captureRecoveryPointLocked(recoveryPoint.MetadataEpoch, recoveryPoint.NodeID, recoveryPoint.PlacementDigest, recoveryPoint.CapacityManifest)
		if !reflect.DeepEqual(current, *recoveryPoint) {
			return fmt.Errorf("backup recovery point no longer matches durable shard sequences")
		}
	}
	absDestination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	absData, err := filepath.Abs(e.dataPath)
	if err != nil {
		return err
	}
	if relative, err := filepath.Rel(absData, absDestination); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("backup destination must be outside the data directory")
	}
	if _, err := os.Lstat(absDestination); err == nil {
		return fmt.Errorf("backup destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for name, collection := range e.collections {
		for shardID := range collection.Config().ShardCount {
			if !e.shardOwned(name, uint32(shardID)) {
				continue
			}
			if err := e.checkpointShardLocked(name, collection, uint32(shardID)); err != nil {
				return fmt.Errorf("checkpoint before backup: %w", err)
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(absDestination), 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(absDestination), ".vectordb-backup-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer temporary.Close()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	gzipWriter := gzip.NewWriter(temporary)
	tarWriter := tar.NewWriter(gzipWriter)
	metadata, _ := json.Marshal(backupHeader{Format: backupFormat, CreatedAt: time.Now().UTC(), RecoveryPoint: recoveryPoint})
	if err := writeTarBytes(tarWriter, "BACKUP.json", metadata, 0o640); err != nil {
		return err
	}
	err = filepath.Walk(absData, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("backup refuses non-regular file %s", path)
		}
		relative, err := filepath.Rel(absData, path)
		if err != nil {
			return err
		}
		if clean := filepath.Clean(relative); clean == cluster.IdentityFile || clean == cluster.MetadataFile || clean == cluster.RaftStateFile || clean == OwnershipFile {
			return nil
		}
		return writeTarFile(tarWriter, filepath.ToSlash(relative), path, info)
	})
	if err != nil {
		return err
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
	if err := os.Rename(temporaryPath, absDestination); err != nil {
		return err
	}
	removeTemporary = false
	return syncDirectory(filepath.Dir(absDestination))
}

func writeTarBytes(writer *tar.Writer, name string, data []byte, mode int64) error {
	sum := sha256.Sum256(data)
	header := &tar.Header{Name: name, Mode: mode, Size: int64(len(data)), Typeflag: tar.TypeReg, ModTime: time.Now().UTC(), PAXRecords: map[string]string{"VDB.sha256": hex.EncodeToString(sum[:])}}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	_, err := writer.Write(data)
	return err
}

func writeTarFile(writer *tar.Writer, name, path string, info os.FileInfo) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	header := &tar.Header{Name: name, Mode: int64(info.Mode().Perm()), Size: info.Size(), Typeflag: tar.TypeReg, ModTime: info.ModTime(), PAXRecords: map[string]string{"VDB.sha256": hex.EncodeToString(hash.Sum(nil))}}
	if err := writer.WriteHeader(header); err != nil {
		_ = file.Close()
		return err
	}
	_, copyErr := io.Copy(writer, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// RestoreBackup validates and restores an archive into a nonexistent data path.
func RestoreBackup(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("restore destination already exists")
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
		return fmt.Errorf("open backup gzip: %w", err)
	}
	defer gzipReader.Close()
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".vectordb-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	reader := tar.NewReader(gzipReader)
	seenHeader, files := false, 0
	var recoveryPoint *NodeRecoveryPoint
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read backup: %w", err)
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 {
			return fmt.Errorf("backup contains unsupported entry %q", header.Name)
		}
		if header.Name == "BACKUP.json" {
			if seenHeader || files != 0 {
				return fmt.Errorf("invalid backup header placement")
			}
			payload, err := io.ReadAll(io.LimitReader(reader, 1<<20))
			if err != nil {
				return err
			}
			if err := verifyBackupChecksum(header, payload); err != nil {
				return err
			}
			var metadata backupHeader
			if err := json.Unmarshal(payload, &metadata); err != nil || metadata.Format != backupFormat {
				return fmt.Errorf("unsupported backup format")
			}
			if metadata.RecoveryPoint != nil {
				if _, err := MergeRecoveryPoints([]NodeRecoveryPoint{*metadata.RecoveryPoint}); err != nil {
					return fmt.Errorf("invalid backup recovery point: %w", err)
				}
				recoveryPoint = metadata.RecoveryPoint
			}
			seenHeader = true
			continue
		}
		if !seenHeader {
			return fmt.Errorf("backup header is missing")
		}
		files++
		if files > 1_000_000 {
			return fmt.Errorf("backup contains too many files")
		}
		clean := filepath.Clean(filepath.FromSlash(header.Name))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe backup path %q", header.Name)
		}
		target := filepath.Join(staging, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o640)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if expected := header.PAXRecords["VDB.sha256"]; expected == "" || !strings.EqualFold(expected, hex.EncodeToString(hash.Sum(nil))) {
			return fmt.Errorf("backup checksum mismatch for %q", header.Name)
		}
	}
	if !seenHeader {
		return fmt.Errorf("backup header is missing")
	}
	if recoveryPoint != nil {
		payload, err := json.Marshal(recoveryPoint)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(staging, RestoredRecoveryPointFile), payload, 0o640); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, destination); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func verifyBackupChecksum(header *tar.Header, payload []byte) error {
	sum := sha256.Sum256(payload)
	if expected := header.PAXRecords["VDB.sha256"]; expected == "" || !strings.EqualFold(expected, hex.EncodeToString(sum[:])) {
		return fmt.Errorf("backup header checksum mismatch")
	}
	return nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
