// Package lite embeds a GideonDB engine in a Go process, SQLite style: open a
// local data directory, create collections, upsert and search, close. No
// server, network listener or authentication is involved.
//
// The API in this package is the stable embedded surface. It wraps the
// internal engine without exposing internal types, so engine refactors do not
// break callers. A data directory written by this package can later be served
// by the gideondb binary (`gideondb -data-path <dir>`), and the reverse, but
// only one process may open a data directory at a time.
//
// A DB is safe for concurrent use by multiple goroutines.
package lite

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/engine"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

// Sentinel errors. Test with errors.Is.
var (
	ErrNotFound          = core.ErrNotFound
	ErrAlreadyExists     = core.ErrAlreadyExists
	ErrDimensionMismatch = core.ErrDimensionMismatch
	ErrInvalidArgument   = core.ErrInvalidArgument
	ErrClosed            = errors.New("gideondb lite: database is closed")
)

// Metric names how vector similarity is computed.
const (
	Cosine = "cosine"
	Dot    = "dot"
	L2     = "l2"
)

// Index types.
const (
	IndexFlat = "flat"
	IndexHNSW = "hnsw"
)

// Fusion modes for hybrid search.
const (
	FusionWeighted = "weighted"
	FusionRRF      = "rrf"
)

// Options tune durability. The zero value is safe: every write is fsynced
// and shards checkpoint every 1000 mutations.
type Options struct {
	// AsyncWAL skips the per-write fsync. Faster bulk loads; the last few
	// writes may be lost on a power failure (never on a clean Close).
	AsyncWAL bool
	// CheckpointEvery checkpoints a shard after this many mutations. Zero
	// selects 1000.
	CheckpointEvery uint64
}

// Collection describes a collection. Name, Dimension and Metric are
// required; the rest have defaults (1 shard, flat index).
type Collection struct {
	Name      string
	Dimension int
	Metric    string
	Shards    int
	Index     Index
}

// Index selects the vector index. The zero value is an exact flat index.
type Index struct {
	Type           string
	M              int
	EFConstruction int
	EFSearch       int
	// Quantization "binary" keeps 1-bit codes and rescores at full
	// precision. Flat index only.
	Quantization string
	Oversampling int
}

// Record is a stored item. ID and either Vector or Text (for keyword-only
// collections queried by Text) are expected; Vector must match the
// collection dimension when set.
type Record struct {
	ID        string
	Namespace string
	Vector    []float32
	// Sparse is an optional sparse vector (term -> weight).
	Sparse map[string]float32
	// Text is indexed for BM25 keyword search.
	Text string
	// Metadata is filterable; Payload is returned but not indexed.
	Metadata map[string]any
	Payload  map[string]any
}

// Query is a search request. Set Vector for dense search, Text and/or Sparse
// for keyword or hybrid search, or any combination.
type Query struct {
	Namespace string
	Vector    []float32
	Sparse    map[string]float32
	Text      string
	TopK      int
	// Filter uses the REST filter syntax, e.g.
	// {"language": "go"} or {"year": {"$gte": 2020}}.
	Filter map[string]any
	// Alpha is the dense weight for weighted fusion; nil selects 0.5.
	Alpha *float32
	// Fusion is FusionWeighted (default) or FusionRRF.
	Fusion string
	// EFSearch overrides the HNSW search breadth for dense-only queries.
	EFSearch int
}

// Result is one search hit. Larger Score is always better.
type Result struct {
	ID        string
	Namespace string
	Score     float32
	Metadata  map[string]any
	Payload   map[string]any
}

// DB is an open embedded database.
type DB struct {
	mu     sync.RWMutex
	engine *engine.Engine
}

// Open opens or creates the data directory at path.
func Open(path string, options *Options) (*DB, error) {
	var o Options
	if options != nil {
		o = *options
	}
	syncMode := wal.SyncAlways
	if o.AsyncWAL {
		syncMode = wal.SyncAsync
	}
	if o.CheckpointEvery == 0 {
		o.CheckpointEvery = 1000
	}
	e, err := engine.OpenWithOptions(path, engine.Options{WALSyncMode: syncMode, CheckpointEvery: o.CheckpointEvery})
	if err != nil {
		return nil, fmt.Errorf("gideondb lite: open %s: %w", path, err)
	}
	return &DB{engine: e}, nil
}

// Close flushes and closes the database. Further calls return ErrClosed.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.engine == nil {
		return ErrClosed
	}
	err := db.engine.Close()
	db.engine = nil
	return err
}

func (db *DB) use(fn func(*engine.Engine) error) error {
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.engine == nil {
		return ErrClosed
	}
	return fn(db.engine)
}

// CreateCollection creates a collection. It returns ErrAlreadyExists when
// the name is taken.
func (db *DB) CreateCollection(c Collection) error {
	config := core.CollectionConfig{
		Name: c.Name, Dimension: c.Dimension, Metric: core.Metric(c.Metric), ShardCount: c.Shards,
		Index: core.IndexConfig{
			Type: core.IndexType(c.Index.Type), M: c.Index.M, EFConstruction: c.Index.EFConstruction,
			EFSearch: c.Index.EFSearch, Quantization: core.Quantization(c.Index.Quantization), Oversampling: c.Index.Oversampling,
		},
	}
	if config.ShardCount == 0 {
		config.ShardCount = 1
	}
	return db.use(func(e *engine.Engine) error { return e.CreateCollection(config) })
}

// Collections lists collections sorted by name.
func (db *DB) Collections() ([]Collection, error) {
	var out []Collection
	err := db.use(func(e *engine.Engine) error {
		for _, c := range e.ListCollections() {
			out = append(out, fromConfig(c))
		}
		return nil
	})
	return out, err
}

// Count returns the number of live records in a collection.
func (db *DB) Count(collection string) (int, error) {
	var n int
	err := db.use(func(e *engine.Engine) error {
		var err error
		_, n, err = e.DescribeCollection(collection)
		return err
	})
	return n, err
}

// DeleteCollection drops a collection and its data.
func (db *DB) DeleteCollection(name string) error {
	return db.use(func(e *engine.Engine) error { return e.DeleteCollection(name) })
}

// Upsert inserts or replaces records atomically per call (up to 10000).
func (db *DB) Upsert(collection string, records ...Record) error {
	batch := make([]core.Record, len(records))
	for i, r := range records {
		batch[i] = core.Record{ID: r.ID, Namespace: r.Namespace, Vector: r.Vector, SparseVector: r.Sparse, Text: r.Text, Metadata: r.Metadata, Payload: r.Payload}
	}
	return db.use(func(e *engine.Engine) error {
		_, err := e.BatchUpsert(collection, batch)
		return err
	})
}

// Get returns one record by namespace and ID.
func (db *DB) Get(collection, namespace, id string) (Record, error) {
	var out Record
	err := db.use(func(e *engine.Engine) error {
		r, err := e.Get(collection, namespace, id)
		out = Record{ID: r.ID, Namespace: r.Namespace, Vector: r.Vector, Sparse: r.SparseVector, Text: r.Text, Metadata: r.Metadata, Payload: r.Payload}
		return err
	})
	return out, err
}

// Delete removes one record. Deleting a missing record returns ErrNotFound.
func (db *DB) Delete(collection, namespace, id string) error {
	return db.use(func(e *engine.Engine) error { return e.Delete(collection, namespace, id) })
}

// Search runs dense, keyword (BM25), sparse or hybrid search depending on
// which of Vector, Text and Sparse are set.
func (db *DB) Search(collection string, q Query) ([]Result, error) {
	if q.TopK <= 0 {
		q.TopK = 10
	}
	var filter *metadata.Expr
	if q.Filter != nil {
		var err error
		if filter, err = metadata.Parse(q.Filter); err != nil {
			return nil, err
		}
	}
	var hits []core.SearchResult
	err := db.use(func(e *engine.Engine) error {
		var err error
		if q.Text == "" && q.Sparse == nil {
			hits, err = e.SearchFilteredWithEF(collection, q.Namespace, q.Vector, q.TopK, filter, q.EFSearch)
			return err
		}
		alpha := float32(0.5)
		if q.Alpha != nil {
			alpha = *q.Alpha
		}
		hits, err = e.SearchHybrid(collection, q.Namespace, core.HybridQuery{
			Dense: q.Vector, Sparse: q.Sparse, Text: q.Text, Alpha: alpha, Fusion: core.FusionMode(q.Fusion), TopK: q.TopK,
		}, filter)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(hits))
	for i, h := range hits {
		out[i] = Result{ID: h.ID, Namespace: h.Namespace, Score: h.Score, Metadata: h.Metadata, Payload: h.Payload}
	}
	return out, nil
}

func fromConfig(c core.CollectionConfig) Collection {
	return Collection{
		Name: c.Name, Dimension: c.Dimension, Metric: string(c.Metric), Shards: c.ShardCount,
		Index: Index{
			Type: string(c.Index.Type), M: c.Index.M, EFConstruction: c.Index.EFConstruction, EFSearch: c.Index.EFSearch,
			Quantization: string(c.Index.Quantization), Oversampling: c.Index.Oversampling,
		},
	}
}
