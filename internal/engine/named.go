package engine

import (
	"errors"
	"fmt"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// SearchNamed searches one named vector field of a collection.
func (e *Engine) SearchNamed(name, namespace string, query core.NamedQuery, filter *metadata.Expr) ([]core.SearchResult, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchNamed(namespace, query, filter)
}

// SearchShardNamed is SearchNamed restricted to one locally owned shard.
func (e *Engine) SearchShardNamed(name string, shardID uint32, namespace string, query core.NamedQuery, filter *metadata.Expr) ([]core.SearchResult, error) {
	if !e.shardOwned(name, shardID) {
		return nil, core.ErrShardNotOwned
	}
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	return c.SearchShardNamed(shardID, namespace, query, filter)
}

// RerankMaxSim rescores results by MaxSim between query and each record's
// token vectors in field, and returns them best first. Records without the
// field are dropped because they have no comparable score. Records must be
// stored on this node.
func (e *Engine) RerankMaxSim(name string, results []core.SearchResult, field string, query [][]float32) ([]core.SearchResult, error) {
	c, err := e.getCollection(name)
	if err != nil {
		return nil, err
	}
	declared, ok := c.Config().VectorField(field)
	if !ok || !declared.MultiVector {
		return nil, fmt.Errorf("%w: maxsim rerank requires a multi-vector field", core.ErrInvalidArgument)
	}
	reranked := make([]core.SearchResult, 0, len(results))
	for _, result := range results {
		record, err := c.Get(result.Namespace, result.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		document := record.MultiVectors[field]
		if document == nil {
			continue
		}
		score, err := core.MaxSim(declared.Metric, query, document, distance.Score)
		if err != nil {
			return nil, err
		}
		result.Score = score
		reranked = append(reranked, result)
	}
	sort.SliceStable(reranked, func(i, j int) bool {
		return reranked[i].Score > reranked[j].Score || (reranked[i].Score == reranked[j].Score && reranked[i].ID < reranked[j].ID)
	})
	return reranked, nil
}

// AttachText copies each result's record text onto the result, for a
// cross-encoder that reranks record text. Records must be stored here.
func (e *Engine) AttachText(name string, results []core.SearchResult) error {
	c, err := e.getCollection(name)
	if err != nil {
		return err
	}
	for position, result := range results {
		record, err := c.Get(result.Namespace, result.ID)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		results[position].Text = record.Text
	}
	return nil
}
