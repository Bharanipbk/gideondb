package segmentfile

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestRecordColumnVersionsForNamedVectors(t *testing.T) {
	directory := t.TempDir()
	plain, err := WriteBundle(directory, "segment-plain", []core.Record{{ID: "a", Vector: []float32{1, 2}}}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	named, err := WriteBundle(directory, "segment-named", []core.Record{{ID: "b", Vector: []float32{1, 2},
		Vectors: map[string][]float32{"title": {0.5}}, MultiVectors: map[string][][]float32{"colbert": {{1}, {2}}}}}, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	for manifest, want := range map[*Manifest]uint16{&plain: 1, &named: 2} {
		data, err := os.ReadFile(filepath.Join(directory, manifest.RecordsFile))
		if err != nil {
			t.Fatal(err)
		}
		if got := binary.LittleEndian.Uint16(data[4:6]); got != want {
			t.Fatalf("%s version = %d, want %d", manifest.RecordsFile, got, want)
		}
	}
	records, err := ReadBundle(directory, named)
	if err != nil || records[0].Vectors["title"][0] != 0.5 || len(records[0].MultiVectors["colbert"]) != 2 {
		t.Fatalf("ReadBundle = %+v, %v", records, err)
	}
	metadataOnly, err := ReadRecordColumn(directory, named)
	if err != nil || metadataOnly[0].Vector != nil || metadataOnly[0].MultiVectors["colbert"][1][0] != 2 {
		t.Fatalf("ReadRecordColumn = %+v, %v", metadataOnly, err)
	}
	// A future record column version must be refused, not misread.
	path := filepath.Join(directory, named.RecordsFile)
	data, _ := os.ReadFile(path)
	binary.LittleEndian.PutUint16(data[4:6], 3)
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecordColumn(directory, named); err == nil {
		t.Fatal("unknown record column version accepted")
	}
	// Version 2 is only valid for record columns.
	vectors := filepath.Join(directory, named.VectorsFile)
	data, _ = os.ReadFile(vectors)
	binary.LittleEndian.PutUint16(data[4:6], 2)
	_ = os.WriteFile(vectors, data, 0o640)
	if _, _, _, err := readColumn(vectors, vectorMagic, 2); err == nil {
		t.Fatal("vector column version 2 accepted")
	}
}
