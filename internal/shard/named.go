package shard

import (
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// SearchNamed searches a named vector field across the mutable delta and
// the immutable base, hiding base records shadowed or deleted by the delta.
func (s *Shard) SearchNamed(namespace string, query core.NamedQuery, filter *metadata.Expr) ([]core.SearchResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	results, err := s.active.SearchNamed(namespace, query, filter, nil)
	if err != nil {
		return nil, err
	}
	if s.immutable != nil {
		excluded := s.active.Keys()
		for key := range s.tombstones {
			excluded[key] = struct{}{}
		}
		base, err := s.immutable.segment.SearchNamed(namespace, query, filter, excluded)
		if err != nil {
			return nil, err
		}
		results = append(results, base...)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score || (results[i].Score == results[j].Score && results[i].ID < results[j].ID)
	})
	if len(results) > query.TopK {
		results = results[:query.TopK]
	}
	return results, nil
}
