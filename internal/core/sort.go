package core

import (
	"fmt"
	"sort"
	"strings"
)

// SortField orders results by a metadata field, or by "_score".
type SortField struct {
	Field string `json:"field"`
	// Order is "asc" (default for fields) or "desc" (default for _score).
	Order string `json:"order,omitempty"`
}

const (
	MaxSortFields     = 4
	DefaultSortPool   = 100
	SortPoolPerResult = 10
	ScoreSortField    = "_score"
	// MaxSortPool bounds the relevance candidates a sort may reorder.
	MaxSortPool = 10_000
)

// NormalizeSort validates sort fields and fills in default orders.
func NormalizeSort(fields []SortField) ([]SortField, error) {
	if len(fields) > MaxSortFields {
		return nil, fmt.Errorf("%w: at most %d sort fields", ErrInvalidArgument, MaxSortFields)
	}
	out := make([]SortField, len(fields))
	for i, field := range fields {
		if field.Field == "" {
			return nil, fmt.Errorf("%w: sort field is required", ErrInvalidArgument)
		}
		switch field.Order {
		case "":
			field.Order = "asc"
			if field.Field == ScoreSortField {
				field.Order = "desc"
			}
		case "asc", "desc":
		default:
			return nil, fmt.Errorf("%w: sort order must be asc or desc", ErrInvalidArgument)
		}
		out[i] = field
	}
	return out, nil
}

// SortPool is the number of relevance candidates to fetch before sorting a
// page of offset+topK results; explicit is the caller's override (0 = default).
func SortPool(explicit, offset, topK int) int {
	if explicit > 0 {
		return explicit
	}
	return min(max(DefaultSortPool, SortPoolPerResult*(offset+topK)), MaxSortPool)
}

// SortResults orders results by the sort fields, stably, so score order
// breaks remaining ties. Missing values sort last in either order; numbers
// sort before strings, and booleans after both.
func SortResults(results []SearchResult, fields []SortField) {
	sort.SliceStable(results, func(i, j int) bool {
		for _, field := range fields {
			a, aok := sortValue(results[i], field.Field)
			b, bok := sortValue(results[j], field.Field)
			if !aok || !bok {
				if aok != bok {
					return aok
				}
				continue
			}
			c := compareSortValues(a, b)
			if c == 0 {
				continue
			}
			if field.Order == "desc" {
				return c > 0
			}
			return c < 0
		}
		return false
	})
}

func sortValue(result SearchResult, field string) (any, bool) {
	if field == ScoreSortField {
		return float64(result.Score), true
	}
	value, ok := result.Metadata[field]
	if !ok || value == nil {
		return nil, false
	}
	switch value.(type) {
	case float64, string, bool:
		return value, true
	}
	return nil, false
}

func sortRank(value any) int {
	switch value.(type) {
	case float64:
		return 0
	case string:
		return 1
	}
	return 2
}

func compareSortValues(a, b any) int {
	if ra, rb := sortRank(a), sortRank(b); ra != rb {
		return ra - rb
	}
	switch x := a.(type) {
	case float64:
		y := b.(float64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
	case string:
		return strings.Compare(x, b.(string))
	case bool:
		y := b.(bool)
		if x == y {
			return 0
		}
		if !x {
			return -1
		}
		return 1
	}
	return 0
}
