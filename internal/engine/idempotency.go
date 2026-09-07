package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Bharanipbk/gideondb/internal/collection"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

const idempotencyFormat = 1

var validIdempotencyKey = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type idempotencyEntry struct {
	Key       string        `json:"key"`
	Digest    string        `json:"digest"`
	Records   []core.Record `json:"records"`
	Completed bool          `json:"completed"`
	Sequence  uint64        `json:"sequence,omitempty"`
}

type idempotencyLedger struct {
	Format  int                `json:"format"`
	Entries []idempotencyEntry `json:"entries"`
}

func validateIdempotencyKey(key string) error {
	if !validIdempotencyKey.MatchString(key) {
		return fmt.Errorf("%w: Idempotency-Key must be 1-128 characters using letters, digits, '.', '_', ':', or '-'", core.ErrInvalidArgument)
	}
	return nil
}

func idempotencyDigest(records []core.Record) (string, error) {
	payload, err := json.Marshal(records)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func (e *Engine) initializeIdempotency(config core.CollectionConfig) error {
	ledgers := make([]map[string]idempotencyEntry, config.ShardCount)
	for shardID := range ledgers {
		ledgers[shardID] = make(map[string]idempotencyEntry)
		path := e.idempotencyPath(config.Name, uint32(shardID))
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read idempotency ledger %s/%d: %w", config.Name, shardID, err)
		}
		var ledger idempotencyLedger
		if err := json.Unmarshal(data, &ledger); err != nil || ledger.Format != idempotencyFormat {
			return fmt.Errorf("decode idempotency ledger %s/%d: invalid format", config.Name, shardID)
		}
		for _, entry := range ledger.Entries {
			if err := validateIdempotencyKey(entry.Key); err != nil || entry.Digest == "" || len(entry.Records) == 0 {
				return fmt.Errorf("decode idempotency ledger %s/%d: invalid entry", config.Name, shardID)
			}
			ledgers[shardID][entry.Key] = entry
		}
	}
	e.idempotency[config.Name] = ledgers
	return nil
}

func (e *Engine) commitReservedBatchLocked(name string, c *collection.Collection, shardID uint32, key string, entry idempotencyEntry) ([]core.Record, uint64, error) {
	payload, err := json.Marshal(walMutation{Records: entry.Records})
	if err != nil {
		return nil, 0, err
	}
	sequence, err := e.logs[name][shardID].Append(wal.OperationBatchUpsert, payload)
	if err != nil {
		return nil, 0, err
	}
	for _, record := range entry.Records {
		if err := c.Restore(record); err != nil {
			return nil, 0, fmt.Errorf("apply idempotent WAL-backed shard batch: %w", err)
		}
	}
	entry.Completed, entry.Sequence = true, sequence
	e.idempotency[name][shardID][key] = entry
	if err := e.persistIdempotencyLocked(name, shardID); err != nil {
		return nil, 0, err
	}
	e.mutations[name][shardID] += uint64(len(entry.Records))
	if e.checkpointEvery > 0 && e.mutations[name][shardID] >= e.checkpointEvery {
		if err := e.checkpointShardLocked(name, c, shardID); err != nil {
			return nil, 0, err
		}
	}
	return cloneRecords(entry.Records), sequence, nil
}

func (e *Engine) persistIdempotencyLocked(name string, shardID uint32) error {
	entries := e.idempotency[name][shardID]
	ledger := idempotencyLedger{Format: idempotencyFormat, Entries: make([]idempotencyEntry, 0, len(entries))}
	for _, entry := range entries {
		ledger.Entries = append(ledger.Entries, entry)
	}
	payload, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	path := e.idempotencyPath(name, shardID)
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".ledger-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(payload); err != nil {
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (e *Engine) idempotencyPath(name string, shardID uint32) string {
	return filepath.Join(e.dataPath, "idempotency", name, fmt.Sprintf("shard-%06d.json", shardID))
}

func cloneRecords(records []core.Record) []core.Record {
	result := make([]core.Record, len(records))
	for index, record := range records {
		result[index] = record
		result[index].Vector = append([]float32(nil), record.Vector...)
		if record.Metadata != nil {
			result[index].Metadata = make(map[string]any, len(record.Metadata))
			for key, value := range record.Metadata {
				result[index].Metadata[key] = value
			}
		}
	}
	return result
}
