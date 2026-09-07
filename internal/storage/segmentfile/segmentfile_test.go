package segmentfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestSegmentAndManifestRoundTrip(t *testing.T) {
	directory := t.TempDir()
	records := []core.Record{{ID: "one", Vector: []float32{1, 2}, Version: 7}}
	segmentPath := filepath.Join(directory, "segment-7.vseg")
	if err := Write(segmentPath, records, 1, 7); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(directory, "MANIFEST.json")
	if err := SaveManifest(manifestPath, Manifest{SegmentFile: "segment-7.vseg", MaxLSN: 7, RecordCount: 1}); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []core.Record
	count, lsn, err := Read(filepath.Join(directory, manifest.SegmentFile), &got)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || lsn != 7 || len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("unexpected segment: count=%d lsn=%d records=%#v", count, lsn, got)
	}
}

func TestSegmentCorruptionFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "segment.vseg")
	if err := Write(path, []core.Record{{ID: "one"}}, 1, 1); err != nil {
		t.Fatal(err)
	}
	file, _ := os.OpenFile(path, os.O_RDWR, 0)
	_, _ = file.WriteAt([]byte{'X'}, headerSize+2)
	_ = file.Close()
	var records []core.Record
	_, _, err := Read(path, &records)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
}

func TestManifestRejectsTraversal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "MANIFEST.json")
	if err := SaveManifest(path, Manifest{SegmentFile: "../outside", MaxLSN: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(path); err == nil {
		t.Fatal("expected unsafe path to be rejected")
	}
}

func TestCleanupOrphansPreservesManifestAndUnknownFiles(t *testing.T) {
	directory := t.TempDir()
	manifest := Manifest{Format: 2, RecordsFile: "segment-20.records", VectorsFile: "segment-20.vectors", Dimension: 2, MaxLSN: 20}
	for _, name := range []string{
		"MANIFEST.json", "segment-20.records", "segment-20.vectors",
		"segment-10.records", "segment-10.vectors", "segment-5.vseg",
		"segment-30.graph", ".tmp-interrupted", "operator-notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupOrphans(directory, manifest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"MANIFEST.json", "segment-20.records", "segment-20.vectors", "operator-notes.txt"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("preserved file %s: %v", name, err)
		}
	}
	for _, name := range []string{"segment-10.records", "segment-10.vectors", "segment-5.vseg", "segment-30.graph", ".tmp-interrupted"} {
		if _, err := os.Stat(filepath.Join(directory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("orphan file %s still exists: %v", name, err)
		}
	}
}

func TestColumnBundleRoundTrip(t *testing.T) {
	directory := t.TempDir()
	records := []core.Record{
		{ID: "one", Vector: []float32{1.25, -2.5}, Metadata: map[string]any{"kind": "a"}, Version: 4},
		{ID: "two", Vector: []float32{3.5, 4.75}, Namespace: "tenant", Version: 5},
	}
	manifest, err := WriteBundle(directory, "segment-5", records, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveManifest(filepath.Join(directory, "MANIFEST.json"), manifest); err != nil {
		t.Fatal(err)
	}
	loadedManifest, err := LoadManifest(filepath.Join(directory, "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadBundle(directory, loadedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "one" || got[0].Vector[1] != -2.5 || got[1].Namespace != "tenant" {
		t.Fatalf("unexpected bundle records: %#v", got)
	}
}

func TestVectorColumnCorruptionFails(t *testing.T) {
	directory := t.TempDir()
	manifest, err := WriteBundle(directory, "segment-1", []core.Record{{ID: "one", Vector: []float32{1, 2}}}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, manifest.VectorsFile)
	file, _ := os.OpenFile(path, os.O_RDWR, 0)
	_, _ = file.WriteAt([]byte{'X'}, columnHeaderSize+1)
	_ = file.Close()
	if _, err := ReadBundle(directory, manifest); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected vector checksum failure, got %v", err)
	}
}

func TestBundleRejectsDimensionMismatch(t *testing.T) {
	_, err := WriteBundle(t.TempDir(), "segment", []core.Record{{ID: "bad", Vector: []float32{1}}}, 2, 1)
	if err == nil {
		t.Fatal("expected dimension mismatch")
	}
}

func BenchmarkReadBundle1Kx128(b *testing.B) {
	directory := b.TempDir()
	records := make([]core.Record, 1000)
	for position := range records {
		records[position] = core.Record{ID: fmt.Sprintf("record-%d", position), Vector: make([]float32, 128)}
		for offset := range records[position].Vector {
			records[position].Vector[offset] = float32(position + offset)
		}
	}
	manifest, err := WriteBundle(directory, "benchmark", records, 128, 1000)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := ReadBundle(directory, manifest); err != nil {
			b.Fatal(err)
		}
	}
}

func TestMappedVectorsScoreAndClose(t *testing.T) {
	directory := t.TempDir()
	manifest, err := WriteBundle(directory, "mapped", []core.Record{
		{ID: "one", Vector: []float32{1, 2}},
		{ID: "two", Vector: []float32{3, 4}},
	}, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := OpenMappedVectors(filepath.Join(directory, manifest.VectorsFile), 2, 2, 9)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.Len() != 2 || mapped.Dimension() != 2 || mapped.MappedBytes() == 0 {
		t.Fatalf("invalid mapping stats")
	}
	score, err := mapped.Score(core.MetricDot, []float32{1, 1}, 1)
	if err != nil || score != 7 {
		t.Fatalf("mapped dot score = %v, %v", score, err)
	}
	vector, err := mapped.VectorCopy(0)
	if err != nil || len(vector) != 2 || vector[1] != 2 {
		t.Fatalf("mapped vector = %v, %v", vector, err)
	}
	if err := mapped.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := mapped.Score(core.MetricDot, []float32{1, 1}, 0); err == nil {
		t.Fatal("score after close should fail")
	}
}

func TestMappedVectorsRejectsManifestMismatch(t *testing.T) {
	directory := t.TempDir()
	manifest, _ := WriteBundle(directory, "mapped", []core.Record{{ID: "one", Vector: []float32{1, 2}}}, 2, 3)
	if mapped, err := OpenMappedVectors(filepath.Join(directory, manifest.VectorsFile), 3, 1, 3); err == nil {
		_ = mapped.Close()
		t.Fatal("expected dimension mismatch")
	}
}
