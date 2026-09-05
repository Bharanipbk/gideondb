// Package cluster contains distributed node and cluster foundations.
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
	"strings"
	"time"
)

const IdentityFile = "NODE_ID.json"

type Identity struct {
	Format    int       `json:"format"`
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
}

func LoadOrCreate(dataPath string) (Identity, error) {
	path := filepath.Join(dataPath, IdentityFile)
	identity, err := LoadIdentity(path)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, err
	}
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return Identity{}, err
	}
	identity = Identity{Format: 1, ID: hex.EncodeToString(value), CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(identity)
	if err != nil {
		return Identity{}, err
	}
	temporary, err := os.CreateTemp(dataPath, ".node-identity-*")
	if err != nil {
		return Identity{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return Identity{}, err
	}
	if _, err := temporary.Write(append(payload, '\n')); err != nil {
		_ = temporary.Close()
		return Identity{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return Identity{}, err
	}
	if err := temporary.Close(); err != nil {
		return Identity{}, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return LoadIdentity(path)
		}
		return Identity{}, err
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(dataPath)
		if err != nil {
			return Identity{}, err
		}
		syncErr := directory.Sync()
		_ = directory.Close()
		if syncErr != nil {
			return Identity{}, syncErr
		}
	}
	return identity, nil
}

func LoadIdentity(path string) (Identity, error) {
	file, err := os.Open(path)
	if err != nil {
		return Identity{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var identity Identity
	if err := decoder.Decode(&identity); err != nil {
		return Identity{}, fmt.Errorf("decode node identity: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Identity{}, fmt.Errorf("node identity must contain one JSON value")
	}
	if identity.Format != 1 || !ValidNodeID(identity.ID) || identity.CreatedAt.IsZero() {
		return Identity{}, fmt.Errorf("invalid node identity")
	}
	return identity, nil
}

// ValidNodeID reports whether value is the canonical non-zero 128-bit node identifier form.
func ValidNodeID(value string) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 || value != strings.ToLower(value) {
		return false
	}
	allZero := true
	for _, value := range decoded {
		if value != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return false
	}
	return true
}
