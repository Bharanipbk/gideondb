package wal

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendAndRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shard.wal")
	log, err := Open(path, SyncAlways, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lsn, err := log.Append(OperationUpsert, []byte("first")); err != nil || lsn != 1 {
		t.Fatalf("Append() = %d, %v", lsn, err)
	}
	if lsn, err := log.Append(OperationDelete, []byte("second")); err != nil || lsn != 2 {
		t.Fatalf("Append() = %d, %v", lsn, err)
	}
	if lsn, err := log.Append(OperationBatchUpsert, []byte("batch")); err != nil || lsn != 3 {
		t.Fatalf("Append() = %d, %v", lsn, err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	var recovered []Record
	log, err = Open(path, SyncAlways, func(record Record) error {
		recovered = append(recovered, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if len(recovered) != 3 || recovered[0].LSN != 1 || string(recovered[1].Payload) != "second" || recovered[2].Operation != OperationBatchUpsert {
		t.Fatalf("unexpected recovery: %#v", recovered)
	}
	if lsn, err := log.Append(OperationUpsert, []byte("third")); err != nil || lsn != 4 {
		t.Fatalf("Append after recovery = %d, %v", lsn, err)
	}
}

func TestTruncatedFinalRecordIsDiscarded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shard.wal")
	log, _ := Open(path, SyncAlways, nil)
	_, _ = log.Append(OperationUpsert, []byte("complete"))
	_, _ = log.Append(OperationUpsert, []byte("truncate-me"))
	_ = log.Close()
	info, _ := os.Stat(path)
	if err := os.Truncate(path, info.Size()-3); err != nil {
		t.Fatal(err)
	}
	var recovered []Record
	log, err := Open(path, SyncAlways, func(record Record) error {
		recovered = append(recovered, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if len(recovered) != 1 || string(recovered[0].Payload) != "complete" {
		t.Fatalf("unexpected recovered records: %#v", recovered)
	}
	if lsn, err := log.Append(OperationDelete, []byte("replacement")); err != nil || lsn != 2 {
		t.Fatalf("Append() = %d, %v", lsn, err)
	}
}

func TestChecksumCorruptionFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shard.wal")
	log, _ := Open(path, SyncAlways, nil)
	_, _ = log.Append(OperationUpsert, []byte("payload"))
	_ = log.Close()
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte{'X'}, headerSize+2); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	_, err = Open(path, SyncAlways, nil)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
}

func TestApplyFailureStopsRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shard.wal")
	log, _ := Open(path, SyncAlways, nil)
	_, _ = log.Append(OperationUpsert, []byte("payload"))
	_ = log.Close()
	want := errors.New("apply failed")
	_, err := Open(path, SyncAlways, func(Record) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("expected apply failure, got %v", err)
	}
}

func BenchmarkAppendAsync(b *testing.B) {
	path := filepath.Join(b.TempDir(), "shard.wal")
	log, err := Open(path, SyncAsync, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer log.Close()
	payload := []byte(`{"id":"record","vector":[1,2,3]}`)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := log.Append(OperationUpsert, payload); err != nil {
			b.Fatal(err)
		}
	}
}
