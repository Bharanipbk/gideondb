package metadata

import (
	"encoding/json"
	"fmt"
	"math/bits"
	"sort"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func MarshalRecords(records []core.Record) ([]byte, error) {
	index := NewIndex()
	for position, record := range records {
		index.Upsert(uint64(position+1), record.Metadata)
	}
	return index.MarshalBinary(uint64(len(records)))
}

type persistedIndex struct {
	Version     int              `json:"version"`
	RecordCount uint64           `json:"record_count"`
	Fields      []persistedField `json:"fields"`
}

type persistedField struct {
	Name     string             `json:"name"`
	Values   []persistedValue   `json:"values"`
	Postings []persistedPosting `json:"postings"`
	Numbers  []persistedNumber  `json:"numeric,omitempty"`
}

type persistedValue struct {
	ID    uint64 `json:"id"`
	Value any    `json:"value"`
}

type persistedPosting struct {
	Key string   `json:"key"`
	IDs []uint64 `json:"ids"`
}

type persistedNumber struct {
	ID    uint64  `json:"id"`
	Value float64 `json:"value"`
}

// MarshalBinary serializes filter-ready structures in deterministic order.
func (i *Index) MarshalBinary(recordCount uint64) ([]byte, error) {
	if i == nil || i.universe.Len() != int(recordCount) {
		return nil, fmt.Errorf("metadata index record count mismatch")
	}
	snapshot := persistedIndex{Version: 1, RecordCount: recordCount}
	fields := make([]string, 0, len(i.values))
	for field := range i.values {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		stored := persistedField{Name: field}
		ids := make([]uint64, 0, len(i.values[field]))
		for id := range i.values[field] {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		for _, id := range ids {
			stored.Values = append(stored.Values, persistedValue{ID: id, Value: i.values[field][id]})
		}
		keys := make([]string, 0, len(i.postings[field]))
		for key := range i.postings[field] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			set := i.postings[field][key].Set(recordCount)
			stored.Postings = append(stored.Postings, persistedPosting{Key: key, IDs: setIDs(set)})
		}
		if numeric := i.numeric[field]; numeric != nil {
			for id, value := range numeric.current {
				stored.Numbers = append(stored.Numbers, persistedNumber{ID: id, Value: value})
			}
			sort.Slice(stored.Numbers, func(a, b int) bool {
				return stored.Numbers[a].Value < stored.Numbers[b].Value || (stored.Numbers[a].Value == stored.Numbers[b].Value && stored.Numbers[a].ID < stored.Numbers[b].ID)
			})
		}
		snapshot.Fields = append(snapshot.Fields, stored)
	}
	return json.Marshal(snapshot)
}

// LoadBinary validates and installs persisted postings without scanning records.
func LoadBinary(data []byte, recordCount uint64) (*Index, error) {
	var snapshot persistedIndex
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode metadata index: %w", err)
	}
	if snapshot.Version != 1 || snapshot.RecordCount != recordCount {
		return nil, fmt.Errorf("metadata index version or record count mismatch")
	}
	result := NewIndex()
	for id := uint64(1); id <= recordCount; id++ {
		result.universe.Add(id)
	}
	seenFields := make(map[string]struct{}, len(snapshot.Fields))
	for _, field := range snapshot.Fields {
		if field.Name == "" {
			return nil, fmt.Errorf("metadata index contains empty field")
		}
		if _, duplicate := seenFields[field.Name]; duplicate {
			return nil, fmt.Errorf("metadata index contains duplicate field %q", field.Name)
		}
		seenFields[field.Name] = struct{}{}
		values := make(map[uint64]any, len(field.Values))
		present := newSet(recordCount)
		for _, item := range field.Values {
			if item.ID == 0 || item.ID > recordCount || validateScalar(item.Value) != nil {
				return nil, fmt.Errorf("metadata index contains invalid value ordinal")
			}
			if _, duplicate := values[item.ID]; duplicate {
				return nil, fmt.Errorf("metadata index contains duplicate value ordinal")
			}
			values[item.ID] = item.Value
			present.Add(item.ID)
		}
		postings := make(map[string]*posting, len(field.Postings))
		covered := make(map[uint64]struct{}, len(values))
		for _, stored := range field.Postings {
			if stored.Key == "" || postings[stored.Key] != nil {
				return nil, fmt.Errorf("metadata index contains invalid posting")
			}
			p := &posting{}
			for _, id := range stored.IDs {
				value, exists := values[id]
				if !exists || canonical(value) != stored.Key {
					return nil, fmt.Errorf("metadata posting does not match value")
				}
				if _, duplicate := covered[id]; duplicate {
					return nil, fmt.Errorf("metadata ordinal occurs in multiple postings")
				}
				covered[id] = struct{}{}
				p.Add(id)
			}
			postings[stored.Key] = p
		}
		if len(covered) != len(values) {
			return nil, fmt.Errorf("metadata postings do not cover field values")
		}
		result.values[field.Name], result.present[field.Name], result.postings[field.Name] = values, present, postings
		if len(field.Numbers) > 0 {
			numeric := &numericField{current: make(map[uint64]float64, len(field.Numbers)), invalidBase: newSet(recordCount)}
			for _, item := range field.Numbers {
				value, exists := values[item.ID]
				numberValue, numericValue := number(value)
				if !exists || !numericValue || numberValue != item.Value {
					return nil, fmt.Errorf("metadata numeric index does not match value")
				}
				if _, duplicate := numeric.current[item.ID]; duplicate {
					return nil, fmt.Errorf("metadata numeric index contains duplicate ordinal")
				}
				numeric.current[item.ID] = item.Value
				numeric.base = append(numeric.base, numericEntry{id: item.ID, value: item.Value})
			}
			result.numeric[field.Name] = numeric
		}
		numericValues := 0
		for _, value := range values {
			if _, ok := number(value); ok {
				numericValues++
			}
		}
		if numericValues != len(field.Numbers) {
			return nil, fmt.Errorf("metadata numeric index does not cover numeric values")
		}
	}
	return result, nil
}

func setIDs(set *Set) []uint64 {
	ids := make([]uint64, 0, set.Len())
	for word, value := range set.words {
		for value != 0 {
			position := bits.TrailingZeros64(value)
			ids = append(ids, uint64(word*64+position))
			value &= value - 1
		}
	}
	return ids
}
