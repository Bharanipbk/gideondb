package cluster

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClusterMetadataPersistsAndHonorsConfiguredID(t *testing.T) {
	path := t.TempDir()
	created, err := LoadOrCreateMetadata(path, clusterTestID)
	if err != nil {
		t.Fatal(err)
	}
	if created.ClusterID != clusterTestID || created.Epoch != 1 || created.Format != 1 {
		t.Fatalf("unexpected metadata: %#v", created)
	}
	loaded, err := LoadOrCreateMetadata(path, clusterTestID)
	if err != nil || loaded != created {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
	if _, err := LoadOrCreateMetadata(path, "44444444444444444444444444444444"); err == nil {
		t.Fatal("expected persisted cluster mismatch")
	}
	info, err := os.Stat(filepath.Join(path, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("metadata permissions are too broad: %o", info.Mode().Perm())
	}
}

func TestClusterMetadataGeneratedAndStrictlyValidated(t *testing.T) {
	path := t.TempDir()
	metadata, err := LoadOrCreateMetadata(path, "")
	if err != nil || !ValidNodeID(metadata.ClusterID) {
		t.Fatalf("metadata=%#v err=%v", metadata, err)
	}
	corrupt := filepath.Join(t.TempDir(), MetadataFile)
	if err := os.WriteFile(corrupt, []byte(`{"format":1,"cluster_id":"bad","epoch":1,"created_at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMetadata(corrupt); err == nil {
		t.Fatal("expected invalid metadata rejection")
	}
}
