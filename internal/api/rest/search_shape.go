package rest

import (
	"fmt"

	"github.com/Bharanipbk/gideondb/internal/core"
)

const (
	// maxSearchWindow bounds offset+top_k and the candidates fetched for
	// grouping, so deep pages cannot force unbounded index work.
	maxSearchWindow  = 10_000
	maxGroupSize     = 100
	groupOversample  = 4
	defaultGroupSize = 1
)

// searchShape is the result shaping a client asked for: score pagination
// (offset) and grouping by a metadata field (group_by).
type searchShape struct {
	topK, offset int
	groupBy      string
	groupSize    int
}

type searchGroup struct {
	Value any                 `json:"value"`
	Hits  []core.SearchResult `json:"hits"`
}

// takeShape validates the shaping fields, removes them from the request and
// raises request.TopK to the number of candidates the index must return.
func (r *searchRequest) takeShape() (searchShape, error) {
	shape := searchShape{topK: r.TopK, offset: r.Offset, groupBy: r.GroupBy, groupSize: r.GroupSize}
	r.Offset, r.GroupBy, r.GroupSize = 0, "", 0
	if shape.offset < 0 {
		return shape, fmt.Errorf("%w: offset must not be negative", core.ErrInvalidArgument)
	}
	if shape.groupBy == "" && shape.groupSize != 0 {
		return shape, fmt.Errorf("%w: group_size requires group_by", core.ErrInvalidArgument)
	}
	if shape.topK <= 0 {
		return shape, nil // the engine reports the invalid top_k
	}
	if shape.offset+shape.topK > maxSearchWindow {
		return shape, fmt.Errorf("%w: offset + top_k must not exceed %d", core.ErrInvalidArgument, maxSearchWindow)
	}
	if shape.groupBy == "" {
		r.TopK = shape.offset + shape.topK
		return shape, nil
	}
	if shape.groupSize == 0 {
		shape.groupSize = defaultGroupSize
	}
	if shape.groupSize < 0 || shape.groupSize > maxGroupSize {
		return shape, fmt.Errorf("%w: group_size must be between 1 and %d", core.ErrInvalidArgument, maxGroupSize)
	}
	// Groups are formed from the best candidates; fetching a multiple of
	// the requested hits makes it likely that enough groups fill up.
	r.TopK = min((shape.offset+shape.topK)*shape.groupSize*groupOversample, maxSearchWindow)
	return shape, nil
}

// apply returns the response key and value for results ordered best first.
func (s searchShape) apply(results []core.SearchResult) (string, any) {
	if s.groupBy == "" {
		start := min(s.offset, len(results))
		return "results", results[start:min(start+s.topK, len(results))]
	}
	groups := make([]searchGroup, 0, s.topK)
	positions := make(map[string]int)
	skipped := 0
	for _, result := range results {
		value, ok := result.Metadata[s.groupBy]
		if !ok || value == nil {
			continue
		}
		key := fmt.Sprintf("%T:%v", value, value)
		position, seen := positions[key]
		if !seen {
			// Offset skips whole groups, in order of their best hit.
			if skipped < s.offset {
				skipped++
				positions[key] = -1
				continue
			}
			if len(groups) == s.topK {
				continue
			}
			positions[key] = len(groups)
			groups = append(groups, searchGroup{Value: value})
			position = len(groups) - 1
		}
		if position >= 0 && len(groups[position].Hits) < s.groupSize {
			groups[position].Hits = append(groups[position].Hits, result)
		}
	}
	return "groups", groups
}
