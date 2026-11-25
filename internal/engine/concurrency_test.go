package engine

import (
	"fmt"
	"sync"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/wal"
)

// Writers on different shards run concurrently; every write must still be
// durable and replay to the same state.
func TestConcurrentWritesAcrossShardsRecoverExactly(t *testing.T) {
	path := t.TempDir()
	db, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAsync, CheckpointEvery: 700})
	if err != nil {
		t.Fatal(err)
	}
	config := core.CollectionConfig{Name: "c", Dimension: 4, Metric: core.MetricL2, ShardCount: 4, Index: core.IndexConfig{Type: core.IndexHNSW}}
	if err := db.CreateCollection(config); err != nil {
		t.Fatal(err)
	}
	const writers, batches, batchSize = 8, 12, 100
	var wg sync.WaitGroup
	errs := make(chan error, writers*3)
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range batches {
				records := make([]core.Record, batchSize)
				for i := range records {
					n := (w*batches+b)*batchSize + i
					records[i] = core.Record{ID: fmt.Sprintf("w%d-%d", w, n), Vector: []float32{float32(n), float32(w), float32(b), 1}, Metadata: map[string]any{"w": float64(w)}}
				}
				if _, err := db.BatchUpsert("c", records); err != nil {
					errs <- err
					return
				}
				// Every writer also contends on a shared key and deletes one of its own.
				if _, err := db.Upsert("c", core.Record{ID: "shared", Vector: []float32{float32(w), 0, 0, 0}}); err != nil {
					errs <- err
					return
				}
				if err := db.Delete("c", "", records[0].ID); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	// Readers run alongside the writers.
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := db.Search("c", "", []float32{1, 1, 1, 1}, 5); err != nil {
					errs <- err
					return
				}
				_, _ = db.ReplicaSequence("c", 1)
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	want := writers*batches*(batchSize-1) + 1 // one delete per batch, plus "shared"
	before := allRecords(t, db)
	if len(before) != want {
		t.Fatalf("records = %d, want %d", len(before), want)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenWithOptions(path, Options{WALSyncMode: wal.SyncAsync, CheckpointEvery: 700})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	after := allRecords(t, reopened)
	if len(after) != want {
		t.Fatalf("after reopen records = %d, want %d", len(after), want)
	}
	index := map[string]core.Record{}
	for _, record := range before {
		index[record.ID] = record
	}
	for _, record := range after {
		previous, ok := index[record.ID]
		if !ok || previous.Version != record.Version || previous.Vector[0] != record.Vector[0] {
			t.Fatalf("record %s differs after recovery: %+v vs %+v", record.ID, previous, record)
		}
	}
}

func allRecords(t *testing.T, db *Engine) []core.Record {
	t.Helper()
	c, err := db.getCollection("c")
	if err != nil {
		t.Fatal(err)
	}
	return c.Records()
}
