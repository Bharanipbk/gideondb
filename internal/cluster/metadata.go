package cluster

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const MetadataFile = "CLUSTER_META.json"

// Metadata is the durable local control-plane envelope. Epoch 1 is a bootstrap
// epoch; later consensus-backed changes must advance it rather than overwrite it.
type Metadata struct {
	Format    int       `json:"format"`
	ClusterID string    `json:"cluster_id"`
	Epoch     uint64    `json:"epoch"`
	CreatedAt time.Time `json:"created_at"`
}

func LoadOrCreateMetadata(dataPath, configuredClusterID string) (Metadata, error) {
	path := filepath.Join(dataPath, MetadataFile)
	metadata, err := LoadMetadata(path)
	if err == nil {
		if configuredClusterID != "" && metadata.ClusterID != configuredClusterID {
			return Metadata{}, fmt.Errorf("configured cluster ID does not match persisted metadata")
		}
		return metadata, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Metadata{}, err
	}
	clusterID := configuredClusterID
	if clusterID == "" {
		value := make([]byte, 16)
		if _, err := rand.Read(value); err != nil {
			return Metadata{}, err
		}
		clusterID = hex.EncodeToString(value)
	}
	if !ValidNodeID(clusterID) {
		return Metadata{}, fmt.Errorf("cluster ID must be 32 lowercase hexadecimal characters and non-zero")
	}
	metadata = Metadata{Format: 1, ClusterID: clusterID, Epoch: 1, CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return Metadata{}, err
	}
	temporary, err := os.CreateTemp(dataPath, ".cluster-metadata-*")
	if err != nil {
		return Metadata{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Metadata{}, err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return Metadata{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Metadata{}, err
	}
	if err := temporary.Close(); err != nil {
		return Metadata{}, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadOrCreateMetadata(dataPath, configuredClusterID)
		}
		return Metadata{}, err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dataPath)
		if err != nil {
			return Metadata{}, err
		}
		syncErr := directory.Sync()
		_ = directory.Close()
		if syncErr != nil {
			return Metadata{}, syncErr
		}
	}
	return metadata, nil
}

func LoadMetadata(path string) (Metadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var metadata Metadata
	if err := decoder.Decode(&metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode cluster metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Metadata{}, fmt.Errorf("cluster metadata must contain one JSON value")
	}
	if metadata.Format != 1 || !ValidNodeID(metadata.ClusterID) || metadata.Epoch == 0 || metadata.CreatedAt.IsZero() {
		return Metadata{}, fmt.Errorf("invalid cluster metadata")
	}
	return metadata, nil
}
