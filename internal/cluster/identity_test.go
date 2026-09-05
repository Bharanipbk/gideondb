package cluster

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestIdentityIsStableAndValid(t *testing.T) {
	path := t.TempDir()
	first, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(first.ID) != 32 || first.CreatedAt != second.CreatedAt {
		t.Fatalf("identity changed: %#v %#v", first, second)
	}
	info, err := os.Stat(filepath.Join(path, IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity mode=%v", info.Mode().Perm())
	}
}

func TestConcurrentIdentityCreationConverges(t *testing.T) {
	path := t.TempDir()
	identities := make([]Identity, 8)
	errorsByWorker := make([]error, 8)
	var wait sync.WaitGroup
	for worker := range identities {
		wait.Add(1)
		go func() { defer wait.Done(); identities[worker], errorsByWorker[worker] = LoadOrCreate(path) }()
	}
	wait.Wait()
	for worker := range identities {
		if errorsByWorker[worker] != nil {
			t.Fatal(errorsByWorker[worker])
		}
		if identities[worker].ID != identities[0].ID {
			t.Fatalf("identity split: %#v", identities)
		}
	}
}

func TestCorruptIdentityFailsClosed(t *testing.T) {
	path := t.TempDir()
	if err := os.WriteFile(filepath.Join(path, IdentityFile), []byte(`{"format":1,"id":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(path); err == nil {
		t.Fatal("expected corrupt identity rejection")
	}
}
