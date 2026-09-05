// Package collection implements the collection and logical-shard boundary.
package collection

import (
	"fmt"
	"hash/fnv"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vectordb/vectordb/internal/core"
	"github.com/vectordb/vectordb/internal/index/flat"
	"github.com/vectordb/vectordb/internal/metadata"
	"github.com/vectordb/vectordb/internal/segment"
	"github.com/vectordb/vectordb/internal/shard"
)

type Collection struct {
	config  core.CollectionConfig
	shards  []*shard.Shard
	version atomic.Uint64
}

func New(config core.CollectionConfig) (*Collection, error) {
	config = config.Normalized()
	if err := config.Validate(); err != nil {
		return nil, err
	}
	c := &Collection{config: config, shards: make([]*shard.Shard, config.ShardCount)}
	for i := range c.shards {
		s, err := shard.New(uint32(i), config)
		if err != nil {
			return nil, err
		}
		c.shards[i] = s
	}
	return c, nil
}

func (c *Collection) Config() core.CollectionConfig { return c.config }

func (c *Collection) Upsert(record core.Record) (core.Record, error) {
	record, err := c.PrepareUpsert(record)
	if err != nil {
		return core.Record{}, err
	}
	if err := c.ApplyUpsert(record); err != nil {
		return core.Record{}, err
	}
	return record, nil
}

// PrepareUpsert validates and versions a record without making it visible.
// The engine uses this to append the WAL before applying the mutation.
func (c *Collection) PrepareUpsert(record core.Record) (core.Record, error) {
	if err := core.ValidateVector(record.Vector, c.config.Dimension); err != nil {
		return core.Record{}, err
	}
	if record.ID == "" {
		return core.Record{}, fmt.Errorf("%w: record id is required", core.ErrInvalidArgument)
	}
	record.Version = c.version.Add(1)
	record.Timestamp = time.Now().UnixNano()
	return record, nil
}

func (c *Collection) ApplyUpsert(record core.Record) error {
	return c.route(record.Namespace, record.ID).Upsert(record)
}

// Restore replays a persisted record while preserving its version and time.
func (c *Collection) Restore(record core.Record) error {
	if err := core.ValidateVector(record.Vector, c.config.Dimension); err != nil {
		return err
	}
	for {
		current := c.version.Load()
		if current >= record.Version || c.version.CompareAndSwap(current, record.Version) {
			break
		}
	}
	return c.route(record.Namespace, record.ID).Upsert(record)
}

func (c *Collection) Get(namespace, id string) (core.Record, error) {
	return c.route(namespace, id).Get(namespace, id)
}

func (c *Collection) Delete(namespace, id string) error {
	if err := c.route(namespace, id).Delete(namespace, id); err != nil {
		return err
	}
	c.version.Add(1)
	return nil
}

func (c *Collection) ApplyDelete(namespace, id string) error {
	if err := c.route(namespace, id).Delete(namespace, id); err != nil {
		return err
	}
	c.version.Add(1)
	return nil
}

func (c *Collection) Search(namespace string, vector []float32, k int) ([]core.SearchResult, error) {
	return c.SearchFiltered(namespace, vector, k, nil)
}

func (c *Collection) SearchFiltered(namespace string, vector []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	if err := core.ValidateVector(vector, c.config.Dimension); err != nil {
		return nil, err
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: top_k must be positive", core.ErrInvalidArgument)
	}
	if len(c.shards) == 1 {
		return c.shards[0].SearchFiltered(namespace, vector, k, filter)
	}
	perShard := make([][]core.SearchResult, len(c.shards))
	errorsByShard := make([]error, len(c.shards))
	workers := min(len(c.shards), max(1, runtime.GOMAXPROCS(0)))
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			for shardID := range jobs {
				perShard[shardID], errorsByShard[shardID] = c.shards[shardID].SearchFiltered(namespace, vector, k, filter)
			}
		}()
	}
	for shardID := range c.shards {
		jobs <- shardID
	}
	close(jobs)
	wait.Wait()
	for _, err := range errorsByShard {
		if err != nil {
			return nil, err
		}
	}
	return mergeTopK(perShard, k), nil
}

func (c *Collection) SearchShardFiltered(shardID uint32, namespace string, vector []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	if err := core.ValidateVector(vector, c.config.Dimension); err != nil {
		return nil, err
	}
	if k <= 0 {
		return nil, fmt.Errorf("%w: top_k must be positive", core.ErrInvalidArgument)
	}
	if uint64(shardID) >= uint64(len(c.shards)) {
		return nil, fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	return c.shards[shardID].SearchFiltered(namespace, vector, k, filter)
}

type searchResultHeap []core.SearchResult

func mergeTopK(perShard [][]core.SearchResult, k int) []core.SearchResult {
	top := make(searchResultHeap, 0, k)
	for _, results := range perShard {
		for _, result := range results {
			if len(top) < k {
				pushSearchResult(&top, result)
				continue
			}
			worst := top[0]
			if result.Score > worst.Score || (result.Score == worst.Score && result.ID < worst.ID) {
				top[0] = result
				fixSearchResultRoot(top)
			}
		}
	}
	sortSearchResults(top)
	return top
}

func worseSearchResult(a, b core.SearchResult) bool {
	return a.Score < b.Score || (a.Score == b.Score && a.ID > b.ID)
}

func pushSearchResult(h *searchResultHeap, result core.SearchResult) {
	*h = append(*h, result)
	position := len(*h) - 1
	for position > 0 {
		parent := (position - 1) / 2
		if !worseSearchResult((*h)[position], (*h)[parent]) {
			break
		}
		(*h)[position], (*h)[parent] = (*h)[parent], (*h)[position]
		position = parent
	}
}

func fixSearchResultRoot(h searchResultHeap) {
	position := 0
	for {
		left := position*2 + 1
		if left >= len(h) {
			return
		}
		worst := left
		if right := left + 1; right < len(h) && worseSearchResult(h[right], h[left]) {
			worst = right
		}
		if !worseSearchResult(h[worst], h[position]) {
			return
		}
		h[position], h[worst] = h[worst], h[position]
		position = worst
	}
}

func sortSearchResults(results []core.SearchResult) {
	for position := 1; position < len(results); position++ {
		result := results[position]
		insert := position
		for insert > 0 && (result.Score > results[insert-1].Score || (result.Score == results[insert-1].Score && result.ID < results[insert-1].ID)) {
			results[insert] = results[insert-1]
			insert--
		}
		results[insert] = result
	}
}

func (c *Collection) Records() []core.Record {
	var records []core.Record
	for _, s := range c.shards {
		records = append(records, s.Records()...)
	}
	return records
}

func (c *Collection) ShardRecords(shardID uint32) ([]core.Record, error) {
	if int(shardID) >= len(c.shards) {
		return nil, fmt.Errorf("%w: shard %d out of range", core.ErrInvalidArgument, shardID)
	}
	return c.shards[shardID].Records(), nil
}

// ReplaceShardRecords replaces one shard with a complete materialized replica
// snapshot while preserving the leader-assigned record versions.
func (c *Collection) ReplaceShardRecords(shardID uint32, records []core.Record) error {
	if int(shardID) >= len(c.shards) {
		return fmt.Errorf("%w: shard %d out of range", core.ErrInvalidArgument, shardID)
	}
	base, err := segment.New(c.config)
	if err != nil {
		return err
	}
	for _, record := range records {
		if c.RouteShard(record.Namespace, record.ID) != shardID {
			_ = base.Close()
			return fmt.Errorf("snapshot record routed to wrong shard")
		}
		if err := base.Upsert(record); err != nil {
			_ = base.Close()
			return err
		}
		for {
			current := c.version.Load()
			if current >= record.Version || c.version.CompareAndSwap(current, record.Version) {
				break
			}
		}
	}
	return c.shards[shardID].InstallImmutable(base)
}

func (c *Collection) InstallMappedShard(shardID uint32, records []core.Record, source flat.MappedSource, closer interface{ Close() error }) error {
	if c.config.Index.Type != core.IndexFlat {
		return fmt.Errorf("%w: mapped checkpoints require a flat index", core.ErrInvalidArgument)
	}
	if int(shardID) >= len(c.shards) {
		return fmt.Errorf("%w: shard %d out of range", core.ErrInvalidArgument, shardID)
	}
	for _, record := range records {
		if c.RouteShard(record.Namespace, record.ID) != shardID {
			return fmt.Errorf("mapped segment record routed to wrong shard")
		}
		for {
			current := c.version.Load()
			if current >= record.Version || c.version.CompareAndSwap(current, record.Version) {
				break
			}
		}
	}
	base, err := segment.NewMapped(c.config, records, source, closer)
	if err != nil {
		return err
	}
	return c.shards[shardID].InstallImmutable(base)
}

func (c *Collection) Close() error {
	var first error
	for _, s := range c.shards {
		if err := s.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (c *Collection) Count() int {
	count := 0
	for _, s := range c.shards {
		count += s.Len()
	}
	return count
}

func (c *Collection) route(namespace, id string) *shard.Shard {
	return c.shards[c.RouteShard(namespace, id)]
}

func (c *Collection) RouteShard(namespace, id string) uint32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(namespace))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	return uint32(h.Sum64() % uint64(len(c.shards)))
}
