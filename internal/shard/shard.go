// Package shard owns logical-shard routing targets and their segments.
package shard

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
	"github.com/Bharanipbk/gideondb/internal/segment"
)

type Shard struct {
	mu         sync.RWMutex
	id         uint32
	config     core.CollectionConfig
	active     *segment.Segment
	immutable  *generation
	nextGen    uint64
	pinned     int
	closed     bool
	tombstones map[string]struct{}
	count      int
}

type generation struct {
	id      uint64
	segment *segment.Segment
	readers int
	retired bool
}

// Snapshot pins one immutable shard generation until Close is called.
type Snapshot struct {
	shard      *Shard
	generation *generation
	once       sync.Once
}

func (r *Snapshot) Generation() uint64 { return r.generation.id }
func (r *Snapshot) ShardID() uint32    { return r.shard.id }
func (r *Snapshot) Get(namespace, id string) (core.Record, error) {
	return r.generation.segment.Get(namespace, id)
}
func (r *Snapshot) Records() []core.Record { return r.generation.segment.Records() }
func (r *Snapshot) SearchFilteredWithEF(namespace string, vector []float32, k int, filter *metadata.Expr, efSearch int) ([]core.SearchResult, error) {
	return r.generation.segment.SearchFilteredWithOptions(namespace, vector, k, filter, nil, efSearch)
}
func (r *Snapshot) Close() error {
	var closeErr error
	r.once.Do(func() { closeErr = r.shard.release(r.generation) })
	return closeErr
}

func New(id uint32, config core.CollectionConfig) (*Shard, error) {
	active, err := segment.New(config)
	if err != nil {
		return nil, err
	}
	return &Shard{id: id, config: config, active: active, tombstones: make(map[string]struct{})}, nil
}

func recordKey(namespace, id string) string              { return namespace + "\x00" + id }
func hasKey(values map[string]struct{}, key string) bool { _, ok := values[key]; return ok }
func (s *Shard) ID() uint32                              { return s.id }

func (s *Shard) Upsert(record core.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey(record.Namespace, record.ID)
	existed := s.active.Has(record.Namespace, record.ID)
	if !existed && s.immutable != nil && !hasKey(s.tombstones, key) {
		existed = s.immutable.segment.Has(record.Namespace, record.ID)
	}
	if err := s.active.Upsert(record); err != nil {
		return err
	}
	delete(s.tombstones, key)
	if !existed {
		s.count++
	}
	return nil
}

func (s *Shard) Get(namespace, id string) (core.Record, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if record, err := s.active.Get(namespace, id); err == nil {
		return record, nil
	} else if err != core.ErrNotFound {
		return core.Record{}, err
	}
	if _, deleted := s.tombstones[recordKey(namespace, id)]; deleted || s.immutable == nil {
		return core.Record{}, core.ErrNotFound
	}
	return s.immutable.segment.Get(namespace, id)
}

func (s *Shard) Delete(namespace, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := recordKey(namespace, id)
	_, activeErr := s.active.Get(namespace, id)
	immutableExists := false
	if s.immutable != nil {
		_, immutableErr := s.immutable.segment.Get(namespace, id)
		immutableExists = immutableErr == nil
		if immutableErr != nil && immutableErr != core.ErrNotFound {
			return immutableErr
		}
	}
	if activeErr != nil && activeErr != core.ErrNotFound {
		return activeErr
	}
	if activeErr == core.ErrNotFound && (!immutableExists || hasKey(s.tombstones, key)) {
		return core.ErrNotFound
	}
	if activeErr == nil {
		if err := s.active.Delete(namespace, id); err != nil {
			return err
		}
	}
	if immutableExists {
		s.tombstones[key] = struct{}{}
	}
	s.count--
	return nil
}

func (s *Shard) Search(namespace string, vector []float32, k int) ([]core.SearchResult, error) {
	return s.SearchFiltered(namespace, vector, k, nil)
}

func (s *Shard) SearchFiltered(namespace string, vector []float32, k int, filter *metadata.Expr) ([]core.SearchResult, error) {
	return s.SearchFilteredWithEF(namespace, vector, k, filter, 0)
}

func (s *Shard) SearchFilteredWithEF(namespace string, vector []float32, k int, filter *metadata.Expr, efSearch int) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	results, err := s.active.SearchFilteredWithOptions(namespace, vector, k, filter, nil, efSearch)
	if err != nil {
		return nil, err
	}
	if s.immutable != nil {
		excluded := s.active.Keys()
		for key := range s.tombstones {
			excluded[key] = struct{}{}
		}
		base, err := s.immutable.segment.SearchFilteredWithOptions(namespace, vector, k, filter, excluded, efSearch)
		if err != nil {
			return nil, err
		}
		results = append(results, base...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
	if len(results) > k {
		results = results[:k]
	}
	return results, nil
}

func (s *Shard) Records() []core.Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	merged := make(map[string]core.Record)
	if s.immutable != nil {
		for _, record := range s.immutable.segment.Records() {
			merged[recordKey(record.Namespace, record.ID)] = record
		}
	}
	for key := range s.tombstones {
		delete(merged, key)
	}
	for _, record := range s.active.Records() {
		merged[recordKey(record.Namespace, record.ID)] = record
	}
	result := make([]core.Record, 0, len(merged))
	for _, record := range merged {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Namespace < result[j].Namespace || (result[i].Namespace == result[j].Namespace && result[i].ID < result[j].ID)
	})
	return result
}

func (s *Shard) Len() int { s.mu.RLock(); defer s.mu.RUnlock(); return s.count }

// InstallImmutable replaces the checkpoint base and clears the covered delta.
// The checkpoint must be published and its WAL reset before this is called.
func (s *Shard) InstallImmutable(base *segment.Segment) error {
	active, err := segment.New(s.config)
	if err != nil {
		return err
	}
	s.mu.Lock()
	old := s.immutable
	s.nextGen++
	s.immutable, s.active, s.tombstones = &generation{id: s.nextGen, segment: base}, active, make(map[string]struct{})
	s.count = base.Len()
	var closeOld *segment.Segment
	if old != nil {
		old.retired = true
		if old.readers == 0 {
			closeOld = old.segment
		}
	}
	s.mu.Unlock()
	if closeOld != nil {
		return closeOld.Close()
	}
	return nil
}

func (s *Shard) PinSnapshot() (*Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("shard is closed")
	}
	if s.immutable == nil {
		return nil, fmt.Errorf("%w: shard has no checkpoint generation", core.ErrInvalidArgument)
	}
	s.immutable.readers++
	s.pinned++
	return &Snapshot{shard: s, generation: s.immutable}, nil
}

func (s *Shard) HasPinnedReaders() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pinned > 0
}

func (s *Shard) release(value *generation) error {
	s.mu.Lock()
	if value.readers <= 0 {
		s.mu.Unlock()
		return nil
	}
	value.readers--
	s.pinned--
	shouldClose := value.retired && value.readers == 0
	s.mu.Unlock()
	if shouldClose {
		return value.segment.Close()
	}
	return nil
}

func (s *Shard) Close() error {
	s.mu.Lock()
	s.closed = true
	var closeCurrent *segment.Segment
	if s.immutable != nil {
		s.immutable.retired = true
		if s.immutable.readers == 0 {
			closeCurrent = s.immutable.segment
		}
	}
	s.mu.Unlock()
	if closeCurrent != nil {
		return closeCurrent.Close()
	}
	return nil
}
