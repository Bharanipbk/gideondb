package segmentfile

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/Bharanipbk/gideondb/internal/core"
)

const columnHeaderSize = 48

var recordMagic = [4]byte{'V', 'R', 'E', 'C'}
var vectorMagic = [4]byte{'V', 'V', 'E', 'C'}
var filterMagic = [4]byte{'V', 'F', 'L', 'T'}

type storedRecord struct {
	ID        string         `json:"id"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp int64          `json:"timestamp"`
	Version   uint64         `json:"version"`
	Namespace string         `json:"namespace,omitempty"`
}

// WriteBundle writes record and vector columns. Publishing the returned
// manifest is a separate atomic step performed only after both files are
// durable.
func WriteBundle(directory, baseName string, records []core.Record, dimension int, maxLSN uint64) (Manifest, error) {
	if dimension <= 0 || uint64(dimension) > uint64(^uint32(0)) {
		return Manifest{}, fmt.Errorf("invalid vector dimension %d", dimension)
	}
	if !safeBase(baseName) {
		return Manifest{}, fmt.Errorf("unsafe segment base name")
	}
	vectorBytes, overflow := multiply3(uint64(len(records)), uint64(dimension), 4)
	if overflow || vectorBytes > uint64(^uint(0)>>1) {
		return Manifest{}, fmt.Errorf("vector column exceeds platform address space")
	}
	stored := make([]storedRecord, len(records))
	vectors := make([]byte, int(vectorBytes))
	for position, record := range records {
		if len(record.Vector) != dimension {
			return Manifest{}, fmt.Errorf("record %q dimension mismatch", record.ID)
		}
		stored[position] = storedRecord{
			ID: record.ID, Metadata: record.Metadata, Payload: record.Payload,
			Timestamp: record.Timestamp, Version: record.Version, Namespace: record.Namespace,
		}
		start := position * dimension * 4
		for offset, value := range record.Vector {
			binary.LittleEndian.PutUint32(vectors[start+offset*4:], math.Float32bits(value))
		}
	}
	recordPayload, err := json.Marshal(stored)
	if err != nil {
		return Manifest{}, err
	}
	recordsName, vectorsName := baseName+".records", baseName+".vectors"
	if err := writeColumn(filepath.Join(directory, recordsName), recordMagic, 0, uint64(len(records)), maxLSN, recordPayload); err != nil {
		return Manifest{}, err
	}
	if err := writeColumn(filepath.Join(directory, vectorsName), vectorMagic, uint32(dimension), uint64(len(records)), maxLSN, vectors); err != nil {
		return Manifest{}, err
	}
	return Manifest{
		Format: 2, RecordsFile: recordsName, VectorsFile: vectorsName,
		Dimension: uint32(dimension), MaxLSN: maxLSN, RecordCount: uint64(len(records)),
	}, nil
}

func ReadBundle(directory string, manifest Manifest) ([]core.Record, error) {
	if manifest.Format != 2 {
		return nil, fmt.Errorf("manifest format %d is not a column bundle", manifest.Format)
	}
	if uint64(manifest.Dimension) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("vector dimension exceeds platform address space")
	}
	recordCount, recordLSN, recordPayload, err := readColumn(filepath.Join(directory, manifest.RecordsFile), recordMagic, 0)
	if err != nil {
		return nil, fmt.Errorf("read record column: %w", err)
	}
	vectorCount, vectorLSN, vectorPayload, err := readColumn(filepath.Join(directory, manifest.VectorsFile), vectorMagic, manifest.Dimension)
	if err != nil {
		return nil, fmt.Errorf("read vector column: %w", err)
	}
	if recordCount != manifest.RecordCount || vectorCount != manifest.RecordCount ||
		recordLSN != manifest.MaxLSN || vectorLSN != manifest.MaxLSN {
		return nil, fmt.Errorf("column bundle manifest mismatch")
	}
	expectedVectorBytes, overflow := multiply3(uint64(manifest.RecordCount), uint64(manifest.Dimension), 4)
	if overflow || uint64(len(vectorPayload)) != expectedVectorBytes {
		return nil, fmt.Errorf("vector column length mismatch")
	}
	var stored []storedRecord
	if err := json.Unmarshal(recordPayload, &stored); err != nil {
		return nil, fmt.Errorf("decode record column: %w", err)
	}
	if uint64(len(stored)) != manifest.RecordCount {
		return nil, fmt.Errorf("record column count mismatch")
	}
	records := make([]core.Record, len(stored))
	dimension := int(manifest.Dimension)
	for position, item := range stored {
		vector := make([]float32, dimension)
		start := position * dimension * 4
		for offset := range vector {
			vector[offset] = math.Float32frombits(binary.LittleEndian.Uint32(vectorPayload[start+offset*4:]))
		}
		records[position] = core.Record{
			ID: item.ID, Vector: vector, Metadata: item.Metadata, Payload: item.Payload,
			Timestamp: item.Timestamp, Version: item.Version, Namespace: item.Namespace,
		}
	}
	return records, nil
}

// ReadRecordColumn validates and decodes checkpoint metadata without loading
// the vector column into the Go heap.
func ReadRecordColumn(directory string, manifest Manifest) ([]core.Record, error) {
	if manifest.Format != 2 {
		return nil, fmt.Errorf("manifest format %d is not a column bundle", manifest.Format)
	}
	count, maxLSN, payload, err := readColumn(filepath.Join(directory, manifest.RecordsFile), recordMagic, 0)
	if err != nil {
		return nil, fmt.Errorf("read record column: %w", err)
	}
	if count != manifest.RecordCount || maxLSN != manifest.MaxLSN {
		return nil, fmt.Errorf("record column manifest mismatch")
	}
	var stored []storedRecord
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, fmt.Errorf("decode record column: %w", err)
	}
	if uint64(len(stored)) != manifest.RecordCount {
		return nil, fmt.Errorf("record column count mismatch")
	}
	records := make([]core.Record, len(stored))
	for position, item := range stored {
		records[position] = core.Record{ID: item.ID, Metadata: item.Metadata, Payload: item.Payload, Timestamp: item.Timestamp, Version: item.Version, Namespace: item.Namespace}
	}
	return records, nil
}

func WriteFilter(directory, name string, data []byte, recordCount, maxLSN uint64) error {
	if !safeBase(name) || filepath.Ext(name) != ".filter" {
		return fmt.Errorf("unsafe filter index file name")
	}
	return writeColumn(filepath.Join(directory, name), filterMagic, 0, recordCount, maxLSN, data)
}

func ReadFilter(directory string, manifest Manifest) ([]byte, error) {
	if manifest.FilterFile == "" || !safeBase(manifest.FilterFile) {
		return nil, fmt.Errorf("manifest does not reference a valid filter index file")
	}
	count, maxLSN, payload, err := readColumn(filepath.Join(directory, manifest.FilterFile), filterMagic, 0)
	if err != nil {
		return nil, err
	}
	if count != manifest.RecordCount || maxLSN != manifest.MaxLSN {
		return nil, fmt.Errorf("filter index manifest mismatch")
	}
	return payload, nil
}

func writeColumn(path string, magic [4]byte, dimension uint32, count, maxLSN uint64, payload []byte) error {
	var header [columnHeaderSize]byte
	copy(header[:4], magic[:])
	binary.LittleEndian.PutUint16(header[4:6], 1)
	binary.LittleEndian.PutUint32(header[8:12], dimension)
	binary.LittleEndian.PutUint64(header[16:24], count)
	binary.LittleEndian.PutUint64(header[24:32], maxLSN)
	binary.LittleEndian.PutUint64(header[32:40], uint64(len(payload)))
	binary.LittleEndian.PutUint32(header[40:44], columnChecksum(header[:], payload))
	return writeAtomic(path, append(header[:], payload...), 0o640)
}

func readColumn(path string, expectedMagic [4]byte, expectedDimension uint32) (uint64, uint64, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, 0, nil, err
	}
	var header [columnHeaderSize]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, 0, nil, err
	}
	if string(header[:4]) != string(expectedMagic[:]) {
		return 0, 0, nil, fmt.Errorf("invalid column magic")
	}
	if binary.LittleEndian.Uint16(header[4:6]) != 1 || binary.LittleEndian.Uint16(header[6:8]) != 0 ||
		binary.LittleEndian.Uint32(header[12:16]) != 0 || binary.LittleEndian.Uint32(header[44:48]) != 0 {
		return 0, 0, nil, fmt.Errorf("unsupported column version, flags, or reserved fields")
	}
	if binary.LittleEndian.Uint32(header[8:12]) != expectedDimension {
		return 0, 0, nil, fmt.Errorf("column dimension mismatch")
	}
	length := binary.LittleEndian.Uint64(header[32:40])
	if length > uint64(^uint(0)>>1) || length != uint64(info.Size()-columnHeaderSize) {
		return 0, 0, nil, fmt.Errorf("invalid column payload length")
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(file, payload); err != nil {
		return 0, 0, nil, err
	}
	if columnChecksum(header[:], payload) != binary.LittleEndian.Uint32(header[40:44]) {
		return 0, 0, nil, fmt.Errorf("column checksum mismatch")
	}
	return binary.LittleEndian.Uint64(header[16:24]), binary.LittleEndian.Uint64(header[24:32]), payload, nil
}

func columnChecksum(header, payload []byte) uint32 {
	value := crc32.Update(0, crcTable, header[4:40])
	value = crc32.Update(value, crcTable, header[44:48])
	return crc32.Update(value, crcTable, payload)
}

func multiply3(a, b, c uint64) (uint64, bool) {
	if a != 0 && b > ^uint64(0)/a {
		return 0, true
	}
	product := a * b
	if product != 0 && c > ^uint64(0)/product {
		return 0, true
	}
	return product * c, false
}
