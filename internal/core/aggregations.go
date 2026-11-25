package core

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
)

// AggregationRequest is one named aggregation over every record that
// matches a search's namespace, filter and text.
//
//	stats      count, min, max, sum and avg of a numeric field
//	histogram  counts per fixed-width numeric bucket (interval)
//	range      counts per [from, to) range
//	terms      most common values (as facets)
type AggregationRequest struct {
	Type     string             `json:"type"`
	Field    string             `json:"field"`
	Interval float64            `json:"interval,omitempty"`
	Ranges   []AggregationRange `json:"ranges,omitempty"`
	Size     int                `json:"size,omitempty"`
}

// AggregationRange is a half-open [From, To) range; nil bounds are open.
type AggregationRange struct {
	Key  string   `json:"key,omitempty"`
	From *float64 `json:"from,omitempty"`
	To   *float64 `json:"to,omitempty"`
}

const (
	maxAggregations     = 16
	maxHistogramBuckets = 1000
	maxRanges           = 100
)

var aggregationName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)

// ValidateAggregations checks names, types and parameters.
func ValidateAggregations(requests map[string]AggregationRequest) error {
	if len(requests) > maxAggregations {
		return fmt.Errorf("%w: at most %d aggregations per request", ErrInvalidArgument, maxAggregations)
	}
	for name, request := range requests {
		if !aggregationName.MatchString(name) {
			return fmt.Errorf("%w: aggregation name %q must match %s", ErrInvalidArgument, name, aggregationName)
		}
		if request.Field == "" {
			return fmt.Errorf("%w: aggregation %q needs a field", ErrInvalidArgument, name)
		}
		switch request.Type {
		case "stats":
		case "histogram":
			if !(request.Interval > 0) || math.IsInf(request.Interval, 0) {
				return fmt.Errorf("%w: histogram %q needs a positive interval", ErrInvalidArgument, name)
			}
		case "range":
			if len(request.Ranges) == 0 || len(request.Ranges) > maxRanges {
				return fmt.Errorf("%w: range %q needs 1 to %d ranges", ErrInvalidArgument, name, maxRanges)
			}
			for _, r := range request.Ranges {
				if r.From != nil && r.To != nil && *r.From >= *r.To {
					return fmt.Errorf("%w: range %q has from >= to", ErrInvalidArgument, name)
				}
			}
		case "terms":
			if request.Size < 0 || request.Size > 1000 {
				return fmt.Errorf("%w: terms %q size must be between 1 and 1000", ErrInvalidArgument, name)
			}
		default:
			return fmt.Errorf("%w: aggregation %q type must be stats, histogram, range or terms", ErrInvalidArgument, name)
		}
	}
	return nil
}

// AggregationState is the mergeable partial result of one aggregation.
type AggregationState struct {
	Count  int            `json:"count,omitempty"`
	Sum    float64        `json:"sum,omitempty"`
	Min    *float64       `json:"min,omitempty"`
	Max    *float64       `json:"max,omitempty"`
	Counts map[string]int `json:"counts,omitempty"` // histogram bucket or range index
	Terms  FacetCounts    `json:"terms,omitempty"`
}

// AggregationStates holds every requested aggregation's state by name.
type AggregationStates map[string]*AggregationState

func numeric(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, !math.IsNaN(v) && !math.IsInf(v, 0)
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint64:
		return float64(v), true
	}
	return 0, false
}

// Add folds one record's metadata into every aggregation.
func (a AggregationStates) Add(requests map[string]AggregationRequest, metadata map[string]any) {
	for name, request := range requests {
		value, ok := metadata[request.Field]
		if !ok {
			continue
		}
		state := a[name]
		if state == nil {
			state = &AggregationState{}
			a[name] = state
		}
		if request.Type == "terms" {
			if state.Terms == nil {
				state.Terms = FacetCounts{}
			}
			state.Terms.Add(request.Field, value)
			continue
		}
		values := []any{value}
		if list, ok := value.([]any); ok {
			values = list
		}
		for _, item := range values {
			number, ok := numeric(item)
			if !ok {
				continue
			}
			switch request.Type {
			case "stats":
				state.Count++
				state.Sum += number
				if state.Min == nil || number < *state.Min {
					state.Min = &number
				}
				if state.Max == nil || number > *state.Max {
					state.Max = &number
				}
			case "histogram":
				key := strconv.FormatFloat(math.Floor(number/request.Interval)*request.Interval, 'g', -1, 64)
				if state.Counts == nil {
					state.Counts = map[string]int{}
				}
				if _, exists := state.Counts[key]; exists || len(state.Counts) < maxHistogramBuckets {
					state.Counts[key]++
				}
			case "range":
				if state.Counts == nil {
					state.Counts = map[string]int{}
				}
				for i, r := range request.Ranges {
					if (r.From == nil || number >= *r.From) && (r.To == nil || number < *r.To) {
						state.Counts[strconv.Itoa(i)]++
					}
				}
			}
		}
	}
}

// Merge adds other's partial state into a.
func (a AggregationStates) Merge(other AggregationStates) {
	for name, state := range other {
		into := a[name]
		if into == nil {
			copied := *state
			copied.Counts = map[string]int{}
			for k, v := range state.Counts {
				copied.Counts[k] = v
			}
			if state.Terms != nil {
				copied.Terms = FacetCounts{}
				copied.Terms.Merge(state.Terms)
			}
			a[name] = &copied
			continue
		}
		into.Count += state.Count
		into.Sum += state.Sum
		if state.Min != nil && (into.Min == nil || *state.Min < *into.Min) {
			value := *state.Min
			into.Min = &value
		}
		if state.Max != nil && (into.Max == nil || *state.Max > *into.Max) {
			value := *state.Max
			into.Max = &value
		}
		for k, v := range state.Counts {
			if into.Counts == nil {
				into.Counts = map[string]int{}
			}
			into.Counts[k] += v
		}
		if state.Terms != nil {
			if into.Terms == nil {
				into.Terms = FacetCounts{}
			}
			into.Terms.Merge(state.Terms)
		}
	}
}

// AggregationBucket is one histogram, range or terms bucket.
type AggregationBucket struct {
	Key   any      `json:"key"`
	From  *float64 `json:"from,omitempty"`
	To    *float64 `json:"to,omitempty"`
	Count int      `json:"count"`
}

// AggregationResult is the client-facing result of one aggregation.
type AggregationResult struct {
	Count   *int                `json:"count,omitempty"`
	Min     *float64            `json:"min,omitempty"`
	Max     *float64            `json:"max,omitempty"`
	Sum     *float64            `json:"sum,omitempty"`
	Avg     *float64            `json:"avg,omitempty"`
	Buckets []AggregationBucket `json:"buckets,omitempty"`
}

// Results turns merged state into client results, in a stable order.
func (a AggregationStates) Results(requests map[string]AggregationRequest) map[string]AggregationResult {
	out := make(map[string]AggregationResult, len(requests))
	for name, request := range requests {
		state := a[name]
		if state == nil {
			state = &AggregationState{}
		}
		var result AggregationResult
		switch request.Type {
		case "stats":
			count, sum := state.Count, state.Sum
			result.Count, result.Min, result.Max = &count, state.Min, state.Max
			if count > 0 {
				avg := sum / float64(count)
				result.Sum, result.Avg = &sum, &avg
			}
		case "histogram":
			keys := make([]float64, 0, len(state.Counts))
			for key := range state.Counts {
				value, _ := strconv.ParseFloat(key, 64)
				keys = append(keys, value)
			}
			sort.Float64s(keys)
			result.Buckets = make([]AggregationBucket, 0, len(keys))
			for _, key := range keys {
				result.Buckets = append(result.Buckets, AggregationBucket{Key: key, Count: state.Counts[strconv.FormatFloat(key, 'g', -1, 64)]})
			}
		case "range":
			result.Buckets = make([]AggregationBucket, len(request.Ranges))
			for i, r := range request.Ranges {
				key := r.Key
				if key == "" {
					key = rangeKey(r)
				}
				result.Buckets[i] = AggregationBucket{Key: key, From: r.From, To: r.To, Count: state.Counts[strconv.Itoa(i)]}
			}
		case "terms":
			buckets := state.Terms.Top([]FacetRequest{{Field: request.Field, Size: request.Size}})[request.Field]
			result.Buckets = make([]AggregationBucket, len(buckets))
			for i, bucket := range buckets {
				result.Buckets[i] = AggregationBucket{Key: bucket.Value, Count: bucket.Count}
			}
		}
		out[name] = result
	}
	return out
}

func rangeKey(r AggregationRange) string {
	bound := func(value *float64) string {
		if value == nil {
			return "*"
		}
		return strconv.FormatFloat(*value, 'g', -1, 64)
	}
	return bound(r.From) + "-" + bound(r.To)
}

// MatchSpec lists what to count over a search's matching records.
type MatchSpec struct {
	Facets       []FacetRequest
	Aggregations map[string]AggregationRequest
}

// Empty reports whether nothing is requested.
func (m MatchSpec) Empty() bool { return len(m.Facets) == 0 && len(m.Aggregations) == 0 }

// Validate checks facets and aggregations.
func (m MatchSpec) Validate() error {
	if err := ValidateFacets(m.Facets); err != nil {
		return err
	}
	return ValidateAggregations(m.Aggregations)
}

// MatchCounts is the mergeable result of a MatchSpec.
type MatchCounts struct {
	Facets       FacetCounts       `json:"facets,omitempty"`
	Aggregations AggregationStates `json:"aggregations,omitempty"`
}

// NewMatchCounts returns empty, mergeable counts.
func NewMatchCounts() MatchCounts {
	return MatchCounts{Facets: FacetCounts{}, Aggregations: AggregationStates{}}
}

// Merge adds other into m.
func (m MatchCounts) Merge(other MatchCounts) {
	m.Facets.Merge(other.Facets)
	m.Aggregations.Merge(other.Aggregations)
}

// SuggestCounts are mergeable word statistics for /suggest: document
// frequencies of completions of the last word, and of near spellings of
// each query word (the word itself included when indexed).
type SuggestCounts struct {
	Completions map[string]int            `json:"completions,omitempty"`
	Corrections map[string]map[string]int `json:"corrections,omitempty"`
}

func NewSuggestCounts() SuggestCounts {
	return SuggestCounts{Completions: map[string]int{}, Corrections: map[string]map[string]int{}}
}

// Merge adds other into s.
func (s SuggestCounts) Merge(other SuggestCounts) {
	for word, count := range other.Completions {
		s.Completions[word] += count
	}
	for word, candidates := range other.Corrections {
		into := s.Corrections[word]
		if into == nil {
			into = map[string]int{}
			s.Corrections[word] = into
		}
		for candidate, count := range candidates {
			into[candidate] += count
		}
	}
}
