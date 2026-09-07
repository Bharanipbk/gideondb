// Package engine coordinates collections and persistence.
package engine

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/collection"
	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/segment"
	"github.com/Bharanipbk/gideondb/internal/storage"
	"github.com/Bharanipbk/gideondb/internal/storage/segmentfile"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Engine struct {
	mu              sync.RWMutex
	dataPath        string
	collections     map[string]*collection.Collection
	logs            map[string][]*wal.Log
	syncMode        wal.SyncMode
	checkpointLSN   map[string][]uint64
	mutations       map[string][]uint64
	checkpointEvery uint64
	ownership       map[string]map[uint32]struct{}
	walDigests      map[string][]map[uint64][32]byte
	idempotency     map[string][]map[string]idempotencyEntry
}

func Open(dataPath string) (*Engine, error) {
	return OpenWithOptions(dataPath, Options{WALSyncMode: wal.SyncAlways, CheckpointEvery: 1000})
}

type Options struct {
	WALSyncMode     wal.SyncMode
	CheckpointEvery uint64
}

func OpenWithOptions(dataPath string, options Options) (*Engine, error) {
	if dataPath == "" {
		return nil, fmt.Errorf("%w: data path is required", core.ErrInvalidArgument)
	}
	if err := os.MkdirAll(filepath.Join(dataPath, "collections"), 0o750); err != nil {
		return nil, err
	}
	if options.WALSyncMode == "" {
		options.WALSyncMode = wal.SyncAlways
	}
	ownership, err := loadOwnership(dataPath)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		dataPath: dataPath, collections: make(map[string]*collection.Collection),
		logs: make(map[string][]*wal.Log), syncMode: options.WALSyncMode,
		checkpointLSN: make(map[string][]uint64), mutations: make(map[string][]uint64),
		checkpointEvery: options.CheckpointEvery,
		ownership:       ownership,
		walDigests:      make(map[string][]map[uint64][32]byte),
		idempotency:     make(map[string][]map[string]idempotencyEntry),
	}
	entries, err := os.ReadDir(filepath.Join(dataPath, "collections"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		snapshot, err := storage.Load(filepath.Join(dataPath, "collections", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("load collection %s: %w", entry.Name(), err)
		}
		c, err := collection.New(snapshot.Config)
		if err != nil {
			return nil, err
		}
		e.collections[snapshot.Config.Name] = c
		e.initializeWALDigests(snapshot.Config)
		if err := e.initializeIdempotency(snapshot.Config); err != nil {
			_ = e.closeLogs()
			return nil, err
		}
		if err := e.loadSegments(c, snapshot.Records); err != nil {
			_ = e.closeLogs()
			return nil, err
		}
		if err := e.openCollectionLogs(c); err != nil {
			_ = e.closeLogs()
			return nil, err
		}
	}
	return e, nil
}

func (e *Engine) CreateCollection(config core.CollectionConfig) error {
	if !validName.MatchString(config.Name) {
		return fmt.Errorf("%w: invalid collection name", core.ErrInvalidArgument)
	}
	c, err := collection.New(config)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ownership != nil {
		return fmt.Errorf("%w: collection schema is immutable after shard ownership activation", core.ErrInvalidArgument)
	}
	if _, exists := e.collections[config.Name]; exists {
		return core.ErrAlreadyExists
	}
	if err := e.persistCatalogLocked(c); err != nil {
		return err
	}
	e.collections[config.Name] = c
	e.initializeWALDigests(config)
	if err := e.initializeIdempotency(config); err != nil {
		delete(e.collections, config.Name)
		_ = os.Remove(e.collectionPath(config.Name))
		return err
	}
	e.checkpointLSN[config.Name] = make([]uint64, config.ShardCount)
	e.mutations[config.Name] = make([]uint64, config.ShardCount)
	if err := e.openCollectionLogs(c); err != nil {
		delete(e.collections, config.Name)
		_ = os.Remove(e.collectionPath(config.Name))
		return err
	}
	return nil
}

func (e *Engine) DeleteCollection(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ownership != nil {
		return fmt.Errorf("%w: collection schema is immutable after shard ownership activation", core.ErrInvalidArgument)
	}
	c, ok := e.collections[name]
	if !ok {
		return core.ErrNotFound
	}
	if c.HasPinnedReaders() {
		return fmt.Errorf("%w: collection has pinned snapshot readers", core.ErrInvalidArgument)
	}
	for _, log := range e.logs[name] {
		if log != nil {
			if err := log.Close(); err != nil {
				return err
			}
		}
	}
	if err := c.Close(); err != nil {
		return err
	}
	if err := os.Remove(e.collectionPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.RemoveAll(filepath.Join(e.dataPath, "wal", name)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(e.dataPath, "segments", name)); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(e.dataPath, "idempotency", name)); err != nil {
		return err
	}
	delete(e.collections, name)
	delete(e.logs, name)
	delete(e.checkpointLSN, name)
	delete(e.mutations, name)
	delete(e.idempotency, name)
	return nil
}

func (e *Engine) ListCollections() []core.CollectionConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	configs := make([]core.CollectionConfig, 0, len(e.collections))
	for _, c := range e.collections {
		configs = append(configs, c.Config())
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].Name < configs[j].Name })
	return configs
}

func (e *Engine) PinSnapshot(name string) (*collection.Snapshot, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.collections[name]
	if !ok {
		return nil, core.ErrNotFound
	}
	return c.PinSnapshot()
}

func (e *Engine) DescribeCollection(name string) (core.CollectionConfig, int, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return core.CollectionConfig{}, 0, err
	}
	return c.Config(), c.Count(), nil
}

func (e *Engine) Upsert(name string, record core.Record) (core.Record, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return core.Record{}, core.ErrNotFound
	}
	stored, err := c.PrepareUpsert(record)
	if err != nil {
		return core.Record{}, err
	}
	payload, err := json.Marshal(walMutation{Record: &stored})
	if err != nil {
		return core.Record{}, err
	}
	shardID := c.RouteShard(stored.Namespace, stored.ID)
	if !e.shardOwned(name, shardID) {
		return core.Record{}, core.ErrShardNotOwned
	}
	if _, err := e.logs[name][shardID].Append(wal.OperationUpsert, payload); err != nil {
		return core.Record{}, err
	}
	if err := c.ApplyUpsert(stored); err != nil {
		return core.Record{}, fmt.Errorf("apply WAL-backed upsert: %w", err)
	}
	if err := e.afterMutationLocked(name, c, shardID); err != nil {
		return core.Record{}, err
	}
	return stored, nil
}

func (e *Engine) BatchUpsert(name string, records []core.Record) ([]core.Record, error) {
	if len(records) == 0 || len(records) > 10_000 {
		return nil, fmt.Errorf("%w: batch size must be between 1 and 10000", core.ErrInvalidArgument)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return nil, core.ErrNotFound
	}
	prepared := make([]core.Record, len(records))
	groups := make(map[uint32][]core.Record)
	for position, record := range records {
		stored, err := c.PrepareUpsert(record)
		if err != nil {
			return nil, fmt.Errorf("record %d: %w", position, err)
		}
		prepared[position] = stored
		shardID := c.RouteShard(stored.Namespace, stored.ID)
		groups[shardID] = append(groups[shardID], stored)
	}
	for shardID := range groups {
		if !e.shardOwned(name, shardID) {
			return nil, core.ErrShardNotOwned
		}
	}
	shardIDs := make([]int, 0, len(groups))
	for shardID := range groups {
		shardIDs = append(shardIDs, int(shardID))
	}
	sort.Ints(shardIDs)
	for _, value := range shardIDs {
		shardID := uint32(value)
		group := groups[shardID]
		payload, err := json.Marshal(walMutation{Records: group})
		if err != nil {
			return nil, err
		}
		if _, err := e.logs[name][shardID].Append(wal.OperationBatchUpsert, payload); err != nil {
			return nil, fmt.Errorf("batch partially committed before shard %d: %w", shardID, err)
		}
		for _, record := range group {
			if err := c.ApplyUpsert(record); err != nil {
				return nil, fmt.Errorf("apply WAL-backed batch: %w", err)
			}
		}
		e.mutations[name][shardID] += uint64(len(group))
		if e.checkpointEvery > 0 && e.mutations[name][shardID] >= e.checkpointEvery {
			if err := e.checkpointShardLocked(name, c, shardID); err != nil {
				return nil, err
			}
		}
	}
	return prepared, nil
}

// RouteShard returns the immutable logical shard for a record key.
func (e *Engine) RouteShard(name, namespace, id string) (uint32, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return 0, err
	}
	if id == "" {
		return 0, fmt.Errorf("%w: record id is required", core.ErrInvalidArgument)
	}
	return c.RouteShard(namespace, id), nil
}

// BatchUpsertShard durably commits one already-grouped logical-shard mutation.
func (e *Engine) BatchUpsertShard(name string, shardID uint32, records []core.Record) ([]core.Record, error) {
	prepared, _, err := e.BatchUpsertShardWithSequence(name, shardID, records)
	return prepared, err
}

// BatchUpsertShardWithSequence returns the durable WAL sequence used by
// replica transport in addition to the leader-prepared records.
func (e *Engine) BatchUpsertShardWithSequence(name string, shardID uint32, records []core.Record) ([]core.Record, uint64, error) {
	return e.BatchUpsertShardIdempotent(name, shardID, records, "")
}

// BatchUpsertShardIdempotent commits a shard batch once for a durable key. A
// retry with the same canonical request returns the originally prepared record
// versions; reusing the key for different content is rejected.
func (e *Engine) BatchUpsertShardIdempotent(name string, shardID uint32, records []core.Record, idempotencyKey string) ([]core.Record, uint64, error) {
	if len(records) == 0 || len(records) > 10_000 {
		return nil, 0, fmt.Errorf("%w: batch size must be between 1 and 10000", core.ErrInvalidArgument)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return nil, 0, core.ErrNotFound
	}
	if !e.shardOwned(name, shardID) {
		return nil, 0, core.ErrShardNotOwned
	}
	if int(shardID) >= c.Config().ShardCount {
		return nil, 0, fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	requestDigest, err := idempotencyDigest(records)
	if err != nil {
		return nil, 0, err
	}
	if idempotencyKey != "" {
		if err := validateIdempotencyKey(idempotencyKey); err != nil {
			return nil, 0, err
		}
		if retained, exists := e.idempotency[name][shardID][idempotencyKey]; exists {
			if retained.Digest != requestDigest {
				return nil, 0, core.ErrIdempotencyConflict
			}
			if retained.Completed {
				return cloneRecords(retained.Records), retained.Sequence, nil
			}
			return e.commitReservedBatchLocked(name, c, shardID, idempotencyKey, retained)
		}
	}
	for position, record := range records {
		if record.ID == "" || c.RouteShard(record.Namespace, record.ID) != shardID {
			return nil, 0, fmt.Errorf("%w: record %d does not route to shard %d", core.ErrInvalidArgument, position, shardID)
		}
		if err := core.ValidateVector(record.Vector, c.Config().Dimension); err != nil {
			return nil, 0, fmt.Errorf("record %d: %w", position, err)
		}
		if record.SparseVector != nil {
			if err := core.ValidateSparseVector(record.SparseVector); err != nil {
				return nil, 0, fmt.Errorf("record %d: %w", position, err)
			}
		}
	}
	prepared := make([]core.Record, len(records))
	for position, record := range records {
		stored, err := c.PrepareUpsert(record)
		if err != nil {
			return nil, 0, fmt.Errorf("record %d: %w", position, err)
		}
		prepared[position] = stored
	}
	if idempotencyKey != "" {
		entry := idempotencyEntry{Key: idempotencyKey, Digest: requestDigest, Records: cloneRecords(prepared)}
		e.idempotency[name][shardID][idempotencyKey] = entry
		if err := e.persistIdempotencyLocked(name, shardID); err != nil {
			delete(e.idempotency[name][shardID], idempotencyKey)
			return nil, 0, err
		}
		return e.commitReservedBatchLocked(name, c, shardID, idempotencyKey, entry)
	}
	payload, err := json.Marshal(walMutation{Records: prepared})
	if err != nil {
		return nil, 0, err
	}
	sequence, err := e.logs[name][shardID].Append(wal.OperationBatchUpsert, payload)
	if err != nil {
		return nil, 0, err
	}
	for _, record := range prepared {
		if err := c.ApplyUpsert(record); err != nil {
			return nil, 0, fmt.Errorf("apply WAL-backed shard batch: %w", err)
		}
	}
	e.mutations[name][shardID] += uint64(len(prepared))
	if e.checkpointEvery > 0 && e.mutations[name][shardID] >= e.checkpointEvery {
		if err := e.checkpointShardLocked(name, c, shardID); err != nil {
			return nil, 0, err
		}
	}
	return prepared, sequence, nil
}

// ApplyReplicaBatch appends a leader-prepared batch at an exact follower WAL
// sequence before making it visible. Retries are idempotent only when their
// canonical WAL payload matches the retained sequence.
func (e *Engine) ApplyReplicaBatch(name string, shardID uint32, sequence uint64, records []core.Record) error {
	if sequence == 0 || len(records) == 0 || len(records) > 10_000 {
		return fmt.Errorf("%w: invalid replica batch", core.ErrInvalidArgument)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return core.ErrNotFound
	}
	if !e.shardOwned(name, shardID) {
		return core.ErrShardNotOwned
	}
	if int(shardID) >= c.Config().ShardCount {
		return fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	for position, record := range records {
		if record.ID == "" || record.Version == 0 || record.Timestamp == 0 || c.RouteShard(record.Namespace, record.ID) != shardID {
			return fmt.Errorf("%w: invalid prepared replica record %d", core.ErrInvalidArgument, position)
		}
		if err := core.ValidateVector(record.Vector, c.Config().Dimension); err != nil {
			return fmt.Errorf("record %d: %w", position, err)
		}
		if record.SparseVector != nil {
			if err := core.ValidateSparseVector(record.SparseVector); err != nil {
				return fmt.Errorf("record %d: %w", position, err)
			}
		}
	}
	payload, err := json.Marshal(walMutation{Records: records})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	log := e.logs[name][shardID]
	last := log.LastLSN()
	if sequence <= last {
		retained, exists := e.walDigests[name][shardID][sequence]
		if !exists {
			return core.ErrReplicationCompacted
		}
		if retained != digest {
			return core.ErrReplicationConflict
		}
		return nil
	}
	if sequence != last+1 {
		return fmt.Errorf("%w: expected %d, received %d", core.ErrReplicationGap, last+1, sequence)
	}
	lsn, err := log.Append(wal.OperationBatchUpsert, payload)
	if err != nil {
		return err
	}
	if lsn != sequence {
		return fmt.Errorf("replica WAL assigned unexpected sequence %d", lsn)
	}
	for _, record := range records {
		if err := c.Restore(record); err != nil {
			return fmt.Errorf("apply replica WAL batch: %w", err)
		}
	}
	e.walDigests[name][shardID][sequence] = digest
	e.mutations[name][shardID] += uint64(len(records))
	return nil
}

// ExportReplicaSnapshot returns a consistent materialized shard and the WAL
// sequence through which it is current.
func (e *Engine) ExportReplicaSnapshot(name string, shardID uint32) (uint64, []core.Record, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.collections[name]
	if !ok {
		return 0, nil, core.ErrNotFound
	}
	if !e.shardOwned(name, shardID) {
		return 0, nil, core.ErrShardNotOwned
	}
	if int(shardID) >= c.Config().ShardCount {
		return 0, nil, fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	records, err := c.ShardRecords(shardID)
	return e.logs[name][shardID].LastLSN(), records, err
}

func (e *Engine) ReplicaSequence(name string, shardID uint32) (uint64, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.collections[name]
	if !ok {
		return 0, core.ErrNotFound
	}
	if !e.shardOwned(name, shardID) {
		return 0, core.ErrShardNotOwned
	}
	if int(shardID) >= c.Config().ShardCount {
		return 0, fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	return e.logs[name][shardID].LastLSN(), nil
}

// InstallReplicaSnapshot durably replaces a follower shard and resumes its WAL
// immediately after the snapshot sequence.
func (e *Engine) InstallReplicaSnapshot(name string, shardID uint32, sequence uint64, records []core.Record) error {
	if sequence == 0 {
		return fmt.Errorf("%w: snapshot sequence must be positive", core.ErrInvalidArgument)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return core.ErrNotFound
	}
	if !e.shardOwned(name, shardID) {
		return core.ErrShardNotOwned
	}
	if int(shardID) >= c.Config().ShardCount {
		return fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	if sequence < e.logs[name][shardID].LastLSN() {
		return fmt.Errorf("%w: snapshot sequence is older than local WAL", core.ErrReplicationConflict)
	}
	seen := make(map[string]struct{}, len(records))
	for position, record := range records {
		key := record.Namespace + "\x00" + record.ID
		if record.ID == "" || record.Version == 0 || record.Timestamp == 0 || c.RouteShard(record.Namespace, record.ID) != shardID {
			return fmt.Errorf("%w: invalid snapshot record %d", core.ErrInvalidArgument, position)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("%w: duplicate snapshot record %d", core.ErrInvalidArgument, position)
		}
		seen[key] = struct{}{}
		if err := core.ValidateVector(record.Vector, c.Config().Dimension); err != nil {
			return fmt.Errorf("record %d: %w", position, err)
		}
		if record.SparseVector != nil {
			if err := core.ValidateSparseVector(record.SparseVector); err != nil {
				return fmt.Errorf("record %d: %w", position, err)
			}
		}
	}
	directory := e.shardSegmentDir(name, shardID)
	segmentBase := fmt.Sprintf("segment-%020d", sequence)
	manifestPath := filepath.Join(directory, "MANIFEST.json")
	if _, err := segmentfile.LoadManifest(manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest, err := segmentfile.WriteBundle(directory, segmentBase, records, c.Config().Dimension, sequence)
	if err != nil {
		return err
	}
	filterData, err := metadata.MarshalRecords(records)
	if err != nil {
		return err
	}
	manifest.FilterFile = segmentBase + ".filter"
	if err := segmentfile.WriteFilter(directory, manifest.FilterFile, filterData, manifest.RecordCount, manifest.MaxLSN); err != nil {
		return err
	}
	if c.Config().Index.Type == core.IndexHNSW {
		graph, err := segment.BuildHNSWGraph(c.Config(), records)
		if err != nil {
			return err
		}
		manifest.GraphFile = segmentBase + ".graph"
		if err := segmentfile.WriteGraph(directory, manifest.GraphFile, graph); err != nil {
			return err
		}
	}
	if err := segmentfile.SaveManifest(manifestPath, manifest); err != nil {
		return err
	}
	if err := e.logs[name][shardID].Reset(sequence + 1); err != nil {
		return err
	}
	if err := c.ReplaceShardRecords(shardID, records); err != nil {
		return err
	}
	e.checkpointLSN[name][shardID] = sequence
	e.mutations[name][shardID] = 0
	e.walDigests[name][shardID] = make(map[uint64][32]byte)
	if !c.ShardHasPinnedReaders(shardID) {
		if err := segmentfile.CleanupOrphans(directory, manifest); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) initializeWALDigests(config core.CollectionConfig) {
	digests := make([]map[uint64][32]byte, config.ShardCount)
	for shardID := range digests {
		digests[shardID] = make(map[uint64][32]byte)
	}
	e.walDigests[config.Name] = digests
}

func (e *Engine) Get(name, namespace, id string) (core.Record, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return core.Record{}, err
	}
	if !e.shardOwned(name, c.RouteShard(namespace, id)) {
		return core.Record{}, core.ErrShardNotOwned
	}
	return c.Get(namespace, id)
}

// Scroll returns a deterministic page from the records physically present on
// this node. The caller supplies the last namespace/ID pair from the prior page.
func (e *Engine) Scroll(name, namespace, afterNamespace, afterID string, limit int) ([]core.Record, bool, error) {
	return e.scroll(name, nil, namespace, afterNamespace, afterID, limit)
}

// ScrollShard returns a deterministic page containing only records routed to
// shardID. It is used by the placement-aware cluster coordinator.
func (e *Engine) ScrollShard(name string, shardID uint32, namespace, afterNamespace, afterID string, limit int) ([]core.Record, bool, error) {
	return e.scroll(name, &shardID, namespace, afterNamespace, afterID, limit)
}

func (e *Engine) scroll(name string, shardID *uint32, namespace, afterNamespace, afterID string, limit int) ([]core.Record, bool, error) {
	if limit < 1 || limit > 200 {
		return nil, false, fmt.Errorf("%w: limit must be between 1 and 200", core.ErrInvalidArgument)
	}
	c, err := e.getCollection(name)
	if err != nil {
		return nil, false, err
	}
	records := c.Records()
	sort.Slice(records, func(i, j int) bool {
		return records[i].Namespace < records[j].Namespace || (records[i].Namespace == records[j].Namespace && records[i].ID < records[j].ID)
	})
	page := make([]core.Record, 0, limit)
	for _, record := range records {
		if shardID != nil && c.RouteShard(record.Namespace, record.ID) != *shardID {
			continue
		}
		if namespace != "" && record.Namespace != namespace {
			continue
		}
		if record.Namespace < afterNamespace || (record.Namespace == afterNamespace && record.ID <= afterID) {
			continue
		}
		if len(page) == limit {
			return page, true, nil
		}
		page = append(page, record)
	}
	return page, false, nil
}

func (e *Engine) Delete(name, namespace, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return core.ErrNotFound
	}
	if _, err := c.Get(namespace, id); err != nil {
		return err
	}
	payload, err := json.Marshal(walMutation{Namespace: namespace, ID: id})
	if err != nil {
		return err
	}
	shardID := c.RouteShard(namespace, id)
	if !e.shardOwned(name, shardID) {
		return core.ErrShardNotOwned
	}
	if _, err := e.logs[name][shardID].Append(wal.OperationDelete, payload); err != nil {
		return err
	}
	if err := c.ApplyDelete(namespace, id); err != nil {
		return err
	}
	return e.afterMutationLocked(name, c, shardID)
}

func (e *Engine) Search(name, namespace string, vector []float32, k int) ([]core.SearchResult, error) {
	return e.SearchFiltered(name, namespace, vector, k, nil)
}

func (e *Engine) SearchFiltered(name, namespace string, vector []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	return e.SearchFilteredWithEF(name, namespace, vector, k, filter, 0)
}

func (e *Engine) SearchFilteredWithEF(name, namespace string, vector []float32, k int, filter *metadata.Expr, efSearch int) ([]core.SearchResult, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchFilteredWithEF(namespace, vector, k, filter, efSearch)
}

func (e *Engine) SearchSparseHybrid(name, namespace string, dense []float32, sparse map[string]float32, alpha float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchSparseHybrid(namespace, dense, sparse, alpha, k, filter)
}

func (e *Engine) SearchShardFiltered(name string, shardID uint32, namespace string, vector []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	return e.SearchShardFilteredWithEF(name, shardID, namespace, vector, k, filter, 0)
}

func (e *Engine) SearchShardFilteredWithEF(name string, shardID uint32, namespace string, vector []float32, k int, filter *metadata.Expr, efSearch int) ([]core.SearchResult, error) {
	if !e.shardOwned(name, shardID) {
		return nil, core.ErrShardNotOwned
	}
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchShardFilteredWithEF(shardID, namespace, vector, k, filter, efSearch)
}

func (e *Engine) SearchShardSparseHybrid(name string, shardID uint32, namespace string, dense []float32, sparse map[string]float32, alpha float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	if !e.shardOwned(name, shardID) {
		return nil, core.ErrShardNotOwned
	}
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchShardSparseHybrid(shardID, namespace, dense, sparse, alpha, k, filter)
}

func (e *Engine) getCollection(name string) (*collection.Collection, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.collections[name]
	if !ok {
		return nil, core.ErrNotFound
	}
	return c, nil
}

func (e *Engine) persistCatalogLocked(c *collection.Collection) error {
	return storage.Save(e.collectionPath(c.Config().Name), storage.Snapshot{
		Config: c.Config(),
	})
}

func (e *Engine) collectionPath(name string) string {
	return filepath.Join(e.dataPath, "collections", name+".json")
}

type walMutation struct {
	Record    *core.Record  `json:"record,omitempty"`
	Records   []core.Record `json:"records,omitempty"`
	Namespace string        `json:"namespace,omitempty"`
	ID        string        `json:"id,omitempty"`
}

func (e *Engine) openCollectionLogs(c *collection.Collection) error {
	config := c.Config()
	logs := make([]*wal.Log, config.ShardCount)
	for shardID := range logs {
		path := filepath.Join(e.dataPath, "wal", config.Name, fmt.Sprintf("shard-%06d.wal", shardID))
		if !e.shardOwned(config.Name, uint32(shardID)) {
			info, err := os.Stat(path)
			if err == nil && info.Size() != 0 {
				return fmt.Errorf("unowned shard %s/%d still contains WAL data", config.Name, shardID)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		checkpoint := e.checkpointLSN[config.Name][shardID]
		log, err := wal.OpenWithOptions(path, wal.OpenOptions{
			SyncMode: e.syncMode, InitialLSN: checkpoint + 1, ReplayAfter: checkpoint,
		}, func(record wal.Record) error {
			e.walDigests[config.Name][shardID][record.LSN] = sha256.Sum256(record.Payload)
			var mutation walMutation
			if err := json.Unmarshal(record.Payload, &mutation); err != nil {
				return err
			}
			switch record.Operation {
			case wal.OperationUpsert:
				if mutation.Record == nil || c.RouteShard(mutation.Record.Namespace, mutation.Record.ID) != uint32(shardID) {
					return fmt.Errorf("upsert routed to wrong shard")
				}
				return c.Restore(*mutation.Record)
			case wal.OperationDelete:
				if mutation.ID == "" || c.RouteShard(mutation.Namespace, mutation.ID) != uint32(shardID) {
					return fmt.Errorf("delete routed to wrong shard")
				}
				err := c.ApplyDelete(mutation.Namespace, mutation.ID)
				if errors.Is(err, core.ErrNotFound) {
					return nil
				}
				return err
			case wal.OperationBatchUpsert:
				if len(mutation.Records) == 0 || len(mutation.Records) > 10_000 {
					return fmt.Errorf("invalid batch WAL payload")
				}
				for _, item := range mutation.Records {
					if c.RouteShard(item.Namespace, item.ID) != uint32(shardID) {
						return fmt.Errorf("batch record routed to wrong shard")
					}
					if err := c.Restore(item); err != nil {
						return err
					}
				}
				return nil
			default:
				return fmt.Errorf("unknown operation %d", record.Operation)
			}
		})
		if err != nil {
			for i := 0; i < shardID; i++ {
				if logs[i] != nil {
					_ = logs[i].Close()
				}
			}
			return fmt.Errorf("open WAL for %s shard %d: %w", config.Name, shardID, err)
		}
		logs[shardID] = log
	}
	e.logs[config.Name] = logs
	return nil
}

func (e *Engine) loadSegments(c *collection.Collection, legacy []core.Record) error {
	config := c.Config()
	lsns := make([]uint64, config.ShardCount)
	for shardID := 0; shardID < config.ShardCount; shardID++ {
		if !e.shardOwned(config.Name, uint32(shardID)) {
			entries, err := os.ReadDir(e.shardSegmentDir(config.Name, uint32(shardID)))
			if err == nil && len(entries) != 0 {
				return fmt.Errorf("unowned shard %s/%d still contains segment data", config.Name, shardID)
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			for _, record := range legacy {
				if c.RouteShard(record.Namespace, record.ID) == uint32(shardID) {
					return fmt.Errorf("unowned shard %s/%d still contains legacy data", config.Name, shardID)
				}
			}
			continue
		}
		directory := e.shardSegmentDir(config.Name, uint32(shardID))
		manifestPath := filepath.Join(directory, "MANIFEST.json")
		manifest, err := segmentfile.LoadManifest(manifestPath)
		if errors.Is(err, os.ErrNotExist) {
			for _, record := range legacy {
				if c.RouteShard(record.Namespace, record.ID) == uint32(shardID) {
					if err := c.Restore(record); err != nil {
						return err
					}
				}
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("load manifest for %s shard %d: %w", config.Name, shardID, err)
		}
		var records []core.Record
		if manifest.Format == 3 {
			var locations []segmentfile.VectorLocation
			if config.Index.Type == core.IndexFlat {
				records, locations, err = resolveMappedSegments(directory, manifest.Segments)
			} else {
				records, _, err = materializeSegments(directory, manifest.Segments)
			}
			if err != nil {
				return fmt.Errorf("load multi-segment shard %s/%d: %w", config.Name, shardID, err)
			}
			if uint64(len(records)) != manifest.RecordCount {
				return fmt.Errorf("multi-segment live record count mismatch for %s shard %d", config.Name, shardID)
			}
			var metadataIndex *metadata.Index
			if manifest.FilterFile != "" {
				payload, filterErr := segmentfile.ReadFilter(directory, manifest)
				if filterErr != nil {
					return fmt.Errorf("load multi-segment filter index: %w", filterErr)
				}
				metadataIndex, filterErr = metadata.LoadBinary(payload, manifest.RecordCount)
				if filterErr != nil {
					return fmt.Errorf("restore multi-segment filter index: %w", filterErr)
				}
			}
			for _, record := range records {
				if c.RouteShard(record.Namespace, record.ID) != uint32(shardID) {
					return fmt.Errorf("multi-segment record routed to wrong shard")
				}
			}
			if config.Index.Type == core.IndexFlat {
				mapped, mapErr := segmentfile.OpenCompositeMappedVectors(directory, manifest.Segments, locations)
				if mapErr != nil {
					return fmt.Errorf("map multi-segment vectors: %w", mapErr)
				}
				if err := c.InstallMappedShardWithMetadata(uint32(shardID), records, mapped, mapped, metadataIndex); err != nil {
					_ = mapped.Close()
					return err
				}
			} else if config.Index.Type == core.IndexHNSW && manifest.GraphFile != "" {
				graph, graphErr := segmentfile.ReadGraph(directory, manifest)
				if graphErr != nil {
					return fmt.Errorf("load multi-segment hnsw graph: %w", graphErr)
				}
				if err := c.InstallHNSWShardWithMetadata(uint32(shardID), records, graph, metadataIndex); err != nil {
					return err
				}
			} else if err := c.InstallRecordsShardWithMetadata(uint32(shardID), records, metadataIndex); err != nil {
				return err
			}
			records = nil
		} else if manifest.Format == 1 {
			count, maxLSN, err := segmentfile.Read(filepath.Join(directory, manifest.SegmentFile), &records)
			if err != nil {
				return fmt.Errorf("load legacy segment for %s shard %d: %w", config.Name, shardID, err)
			}
			if count != uint64(len(records)) || count != manifest.RecordCount || maxLSN != manifest.MaxLSN {
				return fmt.Errorf("legacy segment manifest mismatch for %s shard %d", config.Name, shardID)
			}
		} else if config.Index.Type == core.IndexFlat {
			if manifest.Dimension != uint32(config.Dimension) {
				return fmt.Errorf("segment dimension mismatch for %s shard %d", config.Name, shardID)
			}
			records, err = segmentfile.ReadRecordColumn(directory, manifest)
			if err != nil {
				return fmt.Errorf("load column segment for %s shard %d: %w", config.Name, shardID, err)
			}
			var metadataIndex *metadata.Index
			if manifest.FilterFile != "" {
				payload, filterErr := segmentfile.ReadFilter(directory, manifest)
				if filterErr != nil {
					return fmt.Errorf("load filter index for %s shard %d: %w", config.Name, shardID, filterErr)
				}
				metadataIndex, filterErr = metadata.LoadBinary(payload, manifest.RecordCount)
				if filterErr != nil {
					return fmt.Errorf("restore filter index for %s shard %d: %w", config.Name, shardID, filterErr)
				}
			}
			mapped, err := segmentfile.OpenMappedVectors(filepath.Join(directory, manifest.VectorsFile), manifest.Dimension, manifest.RecordCount, manifest.MaxLSN)
			if err != nil {
				return fmt.Errorf("map vector segment for %s shard %d: %w", config.Name, shardID, err)
			}
			if err := c.InstallMappedShardWithMetadata(uint32(shardID), records, mapped, mapped, metadataIndex); err != nil {
				_ = mapped.Close()
				return err
			}
			records = nil
		} else {
			if manifest.Dimension != uint32(config.Dimension) {
				return fmt.Errorf("segment dimension mismatch for %s shard %d", config.Name, shardID)
			}
			records, err = segmentfile.ReadBundle(directory, manifest)
			if err != nil {
				return fmt.Errorf("load column segment for %s shard %d: %w", config.Name, shardID, err)
			}
			var metadataIndex *metadata.Index
			if manifest.FilterFile != "" {
				payload, filterErr := segmentfile.ReadFilter(directory, manifest)
				if filterErr != nil {
					return fmt.Errorf("load filter index for %s shard %d: %w", config.Name, shardID, filterErr)
				}
				metadataIndex, filterErr = metadata.LoadBinary(payload, manifest.RecordCount)
				if filterErr != nil {
					return fmt.Errorf("restore filter index for %s shard %d: %w", config.Name, shardID, filterErr)
				}
			}
			if manifest.GraphFile != "" {
				graph, err := segmentfile.ReadGraph(directory, manifest)
				if err != nil {
					return fmt.Errorf("load hnsw graph for %s shard %d: %w", config.Name, shardID, err)
				}
				if err := c.InstallHNSWShardWithMetadata(uint32(shardID), records, graph, metadataIndex); err != nil {
					return fmt.Errorf("restore hnsw graph for %s shard %d: %w", config.Name, shardID, err)
				}
				records = nil
			}
		}
		for _, record := range records {
			if c.RouteShard(record.Namespace, record.ID) != uint32(shardID) {
				return fmt.Errorf("segment record routed to wrong shard")
			}
			if err := c.Restore(record); err != nil {
				return err
			}
		}
		if err := segmentfile.CleanupOrphans(directory, manifest); err != nil {
			return fmt.Errorf("clean orphan segments for %s shard %d: %w", config.Name, shardID, err)
		}
		lsns[shardID] = manifest.MaxLSN
	}
	e.checkpointLSN[config.Name] = lsns
	e.mutations[config.Name] = make([]uint64, config.ShardCount)
	return nil
}

func (e *Engine) Checkpoint(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.collections[name]
	if !ok {
		return core.ErrNotFound
	}
	for shardID := range c.Config().ShardCount {
		if !e.shardOwned(name, uint32(shardID)) {
			continue
		}
		if err := e.checkpointShardLocked(name, c, uint32(shardID)); err != nil {
			return err
		}
	}
	return nil
}

// MigratePersistentFormats rewrites every owned legacy checkpoint through the
// current durable format-3 publisher. Current checkpoints are left unchanged.
func (e *Engine) MigratePersistentFormats() error {
	for _, config := range e.ListCollections() {
		if err := e.Checkpoint(config.Name); err != nil {
			return fmt.Errorf("migrate collection %s: %w", config.Name, err)
		}
	}
	return nil
}

func (e *Engine) afterMutationLocked(name string, c *collection.Collection, shardID uint32) error {
	e.mutations[name][shardID]++
	if e.checkpointEvery > 0 && e.mutations[name][shardID] >= e.checkpointEvery {
		return e.checkpointShardLocked(name, c, shardID)
	}
	return nil
}

func (e *Engine) checkpointShardLocked(name string, c *collection.Collection, shardID uint32) error {
	log := e.logs[name][shardID]
	if log == nil {
		return core.ErrShardNotOwned
	}
	if err := log.Sync(); err != nil {
		return err
	}
	maxLSN := log.LastLSN()
	if maxLSN < e.checkpointLSN[name][shardID] {
		return fmt.Errorf("WAL LSN regressed below checkpoint")
	}
	delta, tombstones, err := c.ShardCheckpointDelta(shardID)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(e.shardSegmentDir(name, shardID), "MANIFEST.json")
	old, oldErr := segmentfile.LoadManifest(manifestPath)
	if oldErr != nil && !errors.Is(oldErr, os.ErrNotExist) {
		return oldErr
	}
	if oldErr == nil && old.Format == 3 && maxLSN == old.MaxLSN && len(delta) == 0 && len(tombstones) == 0 {
		return nil
	}
	records, err := c.ShardRecords(shardID)
	if err != nil {
		return err
	}
	directory := e.shardSegmentDir(name, shardID)
	refs := make([]segmentfile.SegmentRef, 0, 9)
	if oldErr == nil {
		switch old.Format {
		case 1:
			// Combined legacy checkpoints cannot be referenced by a format-3
			// manifest, so materialize their complete live view as one bundle.
			delta = records
			tombstones = nil
		case 2:
			ref, err := refFromManifest(directory, old)
			if err != nil {
				return err
			}
			refs = append(refs, ref)
		case 3:
			refs = append(refs, old.Segments...)
		}
	}
	writeDelta := oldErr != nil || (oldErr == nil && old.Format == 1) || len(delta) != 0 || len(tombstones) != 0
	if writeDelta {
		segmentBase := fmt.Sprintf("segment-%020d-delta", maxLSN)
		ref, err := writeSegmentRef(directory, segmentBase, delta, tombstones, c.Config().Dimension, maxLSN)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
	}
	refs, err = compactSegmentRefs(directory, refs, c.Config().Dimension, maxLSN)
	if err != nil {
		return err
	}
	manifest := segmentfile.Manifest{Format: 3, Dimension: uint32(c.Config().Dimension), MaxLSN: maxLSN, RecordCount: uint64(len(records)), Segments: refs}
	filterData, err := metadata.MarshalRecords(records)
	if err != nil {
		return err
	}
	manifest.FilterFile = fmt.Sprintf("view-%020d.filter", maxLSN)
	if err := segmentfile.WriteFilter(directory, manifest.FilterFile, filterData, manifest.RecordCount, manifest.MaxLSN); err != nil {
		return err
	}
	var graphData []byte
	if c.Config().Index.Type == core.IndexHNSW {
		graphData, err = segment.BuildHNSWGraph(c.Config(), records)
		if err != nil {
			return err
		}
		manifest.GraphFile = fmt.Sprintf("view-%020d.graph", maxLSN)
		if err := segmentfile.WriteGraph(directory, manifest.GraphFile, graphData); err != nil {
			return err
		}
	}
	if err := segmentfile.SaveManifest(manifestPath, manifest); err != nil {
		return err
	}
	if err := log.Reset(maxLSN + 1); err != nil {
		return err
	}
	e.walDigests[name][shardID] = make(map[uint64][32]byte)
	metadataIndex, err := metadata.LoadBinary(filterData, manifest.RecordCount)
	if err != nil {
		return err
	}
	if c.Config().Index.Type == core.IndexHNSW {
		if err := c.InstallHNSWShardWithMetadata(shardID, records, graphData, metadataIndex); err != nil {
			return err
		}
	} else {
		baseRecords, locations, err := resolveMappedSegments(directory, manifest.Segments)
		if err != nil {
			return err
		}
		mapped, err := segmentfile.OpenCompositeMappedVectors(directory, manifest.Segments, locations)
		if err != nil {
			return err
		}
		if err := c.InstallMappedShardWithMetadata(shardID, baseRecords, mapped, mapped, metadataIndex); err != nil {
			_ = mapped.Close()
			return err
		}
	}
	e.checkpointLSN[name][shardID] = maxLSN
	e.mutations[name][shardID] = 0
	if !c.ShardHasPinnedReaders(shardID) {
		if err := segmentfile.CleanupOrphans(directory, manifest); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) shardSegmentDir(name string, shardID uint32) string {
	return filepath.Join(e.dataPath, "segments", name, fmt.Sprintf("shard-%06d", shardID))
}

func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	first := e.closeLogs()
	for _, c := range e.collections {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (e *Engine) closeLogs() error {
	var first error
	for _, logs := range e.logs {
		for _, log := range logs {
			if log == nil {
				continue
			}
			if err := log.Close(); err != nil && first == nil {
				first = err
			}
		}
	}
	return first
}
