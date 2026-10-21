package collection

import (
	"fmt"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/metadata"
)

// SearchNamed searches one named vector field in every shard.
func (c *Collection) SearchNamed(namespace string, query core.NamedQuery, filter *metadata.Expr) ([]core.SearchResult, error) {
	if err := query.Validate(c.config); err != nil {
		return nil, err
	}
	perShard := make([][]core.SearchResult, len(c.shards))
	for shardID := range c.shards {
		results, err := c.shards[shardID].SearchNamed(namespace, query, filter)
		if err != nil {
			return nil, err
		}
		perShard[shardID] = results
	}
	return mergeTopK(perShard, query.TopK), nil
}

func (c *Collection) SearchShardNamed(shardID uint32, namespace string, query core.NamedQuery, filter *metadata.Expr) ([]core.SearchResult, error) {
	if err := query.Validate(c.config); err != nil {
		return nil, err
	}
	if uint64(shardID) >= uint64(len(c.shards)) {
		return nil, fmt.Errorf("%w: shard ID out of range", core.ErrInvalidArgument)
	}
	return c.shards[shardID].SearchNamed(namespace, query, filter)
}
