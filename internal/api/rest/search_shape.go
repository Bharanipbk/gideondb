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
	sort         []sortField
}

// sortField orders results by a metadata field, or by "_score".
type sortField = core.SortField

const (
	maxSortFields     = 4
	defaultSortPool   = 100
	sortPoolPerResult = 10
	scoreSortField    = "_score"
)

type searchGroup struct {
	Value any                 `json:"value"`
	Hits  []core.SearchResult `json:"hits"`
}

// takeShape validates the shaping fields, removes them from the request and
// raises request.TopK to the number of candidates the index must return.
func (r *searchRequest) takeShape() (searchShape, error) {
	shape := searchShape{topK: r.TopK, offset: r.Offset, groupBy: r.GroupBy, groupSize: r.GroupSize, sort: r.Sort}
	pool := r.SortCandidates
	r.Offset, r.GroupBy, r.GroupSize, r.Sort, r.SortCandidates = 0, "", 0, nil, 0
	if len(shape.sort) > maxSortFields {
		return shape, fmt.Errorf("%w: at most %d sort fields", core.ErrInvalidArgument, maxSortFields)
	}
	for i, field := range shape.sort {
		if field.Field == "" {
			return shape, fmt.Errorf("%w: sort field is required", core.ErrInvalidArgument)
		}
		switch field.Order {
		case "":
			shape.sort[i].Order = "asc"
			if field.Field == scoreSortField {
				shape.sort[i].Order = "desc"
			}
		case "asc", "desc":
		default:
			return shape, fmt.Errorf("%w: sort order must be asc or desc", core.ErrInvalidArgument)
		}
	}
	if pool < 0 || pool > maxSearchWindow || (pool != 0 && len(shape.sort) == 0) {
		return shape, fmt.Errorf("%w: sort_candidates must be between 1 and %d and needs sort", core.ErrInvalidArgument, maxSearchWindow)
	}
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
	defer func() {
		// Sorting reorders a pool of relevance candidates; fetch it.
		if len(shape.sort) > 0 {
			if pool == 0 {
				pool = min(max(defaultSortPool, sortPoolPerResult*(shape.offset+shape.topK)), maxSearchWindow)
			}
			r.TopK = max(r.TopK, pool)
		}
	}()
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
	if len(s.sort) > 0 {
		core.SortResults(results, s.sort)
	}
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
