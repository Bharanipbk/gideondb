package core

import (
	"encoding/json"
	"fmt"
	"sort"
)

// FacetRequest asks for value counts of one metadata field over every
// record that matches the search's namespace and filter (and, for a text
// query, contains at least one query term).
type FacetRequest struct {
	Field string `json:"field"`
	// Size is the number of buckets returned, by count; default 10, max 1000.
	Size int `json:"size,omitempty"`
}

// FacetBucket is one distinct value and how many records hold it.
type FacetBucket struct {
	Value any `json:"value"`
	Count int `json:"count"`
}

// FacetCounts accumulates counts per field, keyed by the value's JSON form
// so that 1 and "1" stay distinct.
type FacetCounts map[string]map[string]*FacetBucket

// MaxFacets bounds the fields one request may facet on.
const MaxFacets = 16

func ValidateFacets(facets []FacetRequest) error {
	if len(facets) > MaxFacets {
		return fmt.Errorf("%w: at most %d facets per request", ErrInvalidArgument, MaxFacets)
	}
	seen := map[string]struct{}{}
	for _, facet := range facets {
		if facet.Field == "" {
			return fmt.Errorf("%w: facet field is required", ErrInvalidArgument)
		}
		if _, duplicate := seen[facet.Field]; duplicate {
			return fmt.Errorf("%w: duplicate facet field %q", ErrInvalidArgument, facet.Field)
		}
		seen[facet.Field] = struct{}{}
		if facet.Size < 0 || facet.Size > 1000 {
			return fmt.Errorf("%w: facet size must be between 1 and 1000", ErrInvalidArgument)
		}
	}
	return nil
}

// Add counts a record's value for field. Arrays count each distinct
// element once; objects and nulls are skipped.
func (f FacetCounts) Add(field string, value any) {
	if list, ok := value.([]any); ok {
		seen := map[string]struct{}{}
		for _, element := range list {
			if key, ok := facetKey(element); ok {
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					f.bump(field, key, element, 1)
				}
			}
		}
		return
	}
	if key, ok := facetKey(value); ok {
		f.bump(field, key, value, 1)
	}
}

// Merge adds other's counts into f.
func (f FacetCounts) Merge(other FacetCounts) {
	for field, buckets := range other {
		for key, bucket := range buckets {
			f.bump(field, key, bucket.Value, bucket.Count)
		}
	}
}

func (f FacetCounts) bump(field, key string, value any, count int) {
	buckets := f[field]
	if buckets == nil {
		buckets = map[string]*FacetBucket{}
		f[field] = buckets
	}
	if bucket := buckets[key]; bucket != nil {
		bucket.Count += count
		return
	}
	buckets[key] = &FacetBucket{Value: value, Count: count}
}

// Top returns each requested field's largest buckets, ties broken by the
// value's JSON form so results are deterministic across shards.
func (f FacetCounts) Top(requests []FacetRequest) map[string][]FacetBucket {
	out := make(map[string][]FacetBucket, len(requests))
	for _, request := range requests {
		size := request.Size
		if size == 0 {
			size = 10
		}
		keys := make([]string, 0, len(f[request.Field]))
		for key := range f[request.Field] {
			keys = append(keys, key)
		}
		buckets := f[request.Field]
		sort.Slice(keys, func(i, j int) bool {
			if buckets[keys[i]].Count != buckets[keys[j]].Count {
				return buckets[keys[i]].Count > buckets[keys[j]].Count
			}
			return keys[i] < keys[j]
		})
		list := make([]FacetBucket, 0, min(size, len(keys)))
		for _, key := range keys[:min(size, len(keys))] {
			list = append(list, *buckets[key])
		}
		out[request.Field] = list
	}
	return out
}

func facetKey(value any) (string, bool) {
	switch value.(type) {
	case string, bool, float64, float32, int, int64, uint64, json.Number:
		encoded, err := json.Marshal(value)
		return string(encoded), err == nil
	}
	return "", false
}
