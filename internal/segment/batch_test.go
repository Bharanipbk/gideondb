package segment

import (
	"reflect"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestUpsertBatchMatchesUpsertLoop(t *testing.T) {
	for _, indexType := range []core.IndexType{core.IndexFlat, core.IndexHNSW} {
		config := core.CollectionConfig{Name: "b", Dimension: 4, Metric: core.MetricL2, ShardCount: 1, Index: core.IndexConfig{Type: indexType}}.Normalized()
		looped, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		batched, _ := New(config)
		var records []core.Record
		for i := range 3000 {
			records = append(records, core.Record{ID: string(rune('a'+i%26)) + string(rune('0'+i/26%10)) + string(rune('A'+i/260)), Vector: []float32{float32(i), float32(i % 7), float32(i % 13), 1},
				Metadata: map[string]any{"bucket": float64(i % 5)}, Text: "doc number"})
		}
		// Overwrites inside the batch and of earlier records.
		records = append(records, core.Record{ID: records[0].ID, Vector: []float32{9, 9, 9, 9}, Text: "replaced"})
		for _, record := range records {
			if err := looped.Upsert(record); err != nil {
				t.Fatal(err)
			}
		}
		if err := batched.UpsertBatch(records[:1000]); err != nil {
			t.Fatal(err)
		}
		if err := batched.UpsertBatch(records[1000:]); err != nil {
			t.Fatal(err)
		}
		want, got := byKey(looped.Records()), byKey(batched.Records())
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("%s: batched records differ from looped records", indexType)
		}
		if looped.Len() != batched.Len() {
			t.Fatalf("%s: len %d vs %d", indexType, looped.Len(), batched.Len())
		}
		results, err := batched.Search("", []float32{9, 9, 9, 9}, 1)
		if err != nil || len(results) != 1 || results[0].ID != records[0].ID {
			t.Fatalf("%s: overwritten vector not searchable: %v %v", indexType, results, err)
		}
		hits, err := batched.SearchHybrid("", core.HybridQuery{Text: "replaced", TopK: 5}, nil, nil)
		if err != nil || len(hits) != 1 {
			t.Fatalf("%s: lexical index not updated: %v %v", indexType, hits, err)
		}
	}
}

func TestUpsertBatchRejectsMissingID(t *testing.T) {
	s, _ := New(core.CollectionConfig{Name: "b", Dimension: 2, Metric: core.MetricL2, ShardCount: 1}.Normalized())
	if err := s.UpsertBatch([]core.Record{{ID: "a", Vector: []float32{1, 0}}, {Vector: []float32{0, 1}}}); err == nil {
		t.Fatal("missing id accepted")
	}
	if s.Len() != 0 {
		t.Fatal("rejected batch was partially applied")
	}
}

func byKey(records []core.Record) map[string]core.Record {
	out := make(map[string]core.Record, len(records))
	for _, record := range records {
		out[record.Namespace+"\x00"+record.ID] = record
	}
	return out
}
