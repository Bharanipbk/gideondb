// Package segmentfile implements immutable shard checkpoint files and atomic
// manifests. The format is experimental until v0.1.0.
package segmentfile

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	headerSize     = 40
	version        = 1
	maximumPayload = 1 << 34
)

var magic = [4]byte{'V', 'S', 'E', 'G'}
var crcTable = crc32.MakeTable(crc32.Castagnoli)

type Manifest struct {
	Format      int          `json:"format"`
	SegmentFile string       `json:"segment_file,omitempty"`
	RecordsFile string       `json:"records_file,omitempty"`
	VectorsFile string       `json:"vectors_file,omitempty"`
	GraphFile   string       `json:"graph_file,omitempty"`
	FilterFile  string       `json:"filter_file,omitempty"`
	Dimension   uint32       `json:"dimension,omitempty"`
	MaxLSN      uint64       `json:"max_lsn"`
	RecordCount uint64       `json:"record_count"`
	Segments    []SegmentRef `json:"segments,omitempty"`
}

// SegmentRef describes one immutable column bundle in a format-3 manifest.
// Entries are ordered from oldest to newest and have increasing MaxLSN values.
type SegmentRef struct {
	RecordsFile    string `json:"records_file"`
	VectorsFile    string `json:"vectors_file"`
	GraphFile      string `json:"graph_file,omitempty"`
	FilterFile     string `json:"filter_file,omitempty"`
	TombstonesFile string `json:"tombstones_file,omitempty"`
	Dimension      uint32 `json:"dimension"`
	MaxLSN         uint64 `json:"max_lsn"`
	RecordCount    uint64 `json:"record_count"`
	SizeBytes      uint64 `json:"size_bytes,omitempty"`
}

func Write(path string, records any, recordCount uint64, maxLSN uint64) error {
	payload, err := json.Marshal(records)
	if err != nil {
		return err
	}
	if uint64(len(payload)) > maximumPayload {
		return fmt.Errorf("segment payload exceeds maximum")
	}
	var header [headerSize]byte
	copy(header[:4], magic[:])
	binary.LittleEndian.PutUint16(header[4:6], version)
	binary.LittleEndian.PutUint64(header[8:16], uint64(len(payload)))
	binary.LittleEndian.PutUint64(header[16:24], recordCount)
	binary.LittleEndian.PutUint64(header[24:32], maxLSN)
	binary.LittleEndian.PutUint32(header[32:36], checksum(header[:], payload))
	return writeAtomic(path, append(header[:], payload...), 0o640)
}

func Read(path string, target any) (recordCount uint64, maxLSN uint64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	var header [headerSize]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, 0, fmt.Errorf("read segment header: %w", err)
	}
	if string(header[:4]) != string(magic[:]) {
		return 0, 0, fmt.Errorf("invalid segment magic")
	}
	if binary.LittleEndian.Uint16(header[4:6]) != version || binary.LittleEndian.Uint16(header[6:8]) != 0 {
		return 0, 0, fmt.Errorf("unsupported segment version or flags")
	}
	length := binary.LittleEndian.Uint64(header[8:16])
	if length > maximumPayload {
		return 0, 0, fmt.Errorf("segment payload too large")
	}
	maxInt := uint64(^uint(0) >> 1)
	if length > maxInt {
		return 0, 0, fmt.Errorf("segment payload exceeds platform address space")
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(file, payload); err != nil {
		return 0, 0, fmt.Errorf("read segment payload: %w", err)
	}
	var trailing [1]byte
	if n, _ := file.Read(trailing[:]); n != 0 {
		return 0, 0, fmt.Errorf("segment has trailing bytes")
	}
	if binary.LittleEndian.Uint32(header[36:40]) != 0 {
		return 0, 0, fmt.Errorf("segment reserved field is nonzero")
	}
	if checksum(header[:], payload) != binary.LittleEndian.Uint32(header[32:36]) {
		return 0, 0, fmt.Errorf("segment checksum mismatch")
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return 0, 0, fmt.Errorf("decode segment: %w", err)
	}
	return binary.LittleEndian.Uint64(header[16:24]), binary.LittleEndian.Uint64(header[24:32]), nil
}

func checksum(header, payload []byte) uint32 {
	value := crc32.Update(0, crcTable, header[4:32])
	value = crc32.Update(value, crcTable, header[36:40])
	return crc32.Update(value, crcTable, payload)
}

func SaveManifest(path string, manifest Manifest) error {
	if manifest.Format == 0 {
		if manifest.RecordsFile != "" || manifest.VectorsFile != "" {
			manifest.Format = 2
		} else {
			manifest.Format = 1
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o640)
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	switch manifest.Format {
	case 1:
		if manifest.SegmentFile == "" || !safeBase(manifest.SegmentFile) {
			return Manifest{}, fmt.Errorf("invalid legacy segment manifest")
		}
	case 2:
		if manifest.RecordsFile == "" || manifest.VectorsFile == "" || manifest.Dimension == 0 ||
			!safeBase(manifest.RecordsFile) || !safeBase(manifest.VectorsFile) ||
			(manifest.GraphFile != "" && !safeBase(manifest.GraphFile)) ||
			(manifest.FilterFile != "" && !safeBase(manifest.FilterFile)) {
			return Manifest{}, fmt.Errorf("invalid column segment manifest")
		}
	case 3:
		if len(manifest.Segments) == 0 || len(manifest.Segments) > 16 || manifest.Dimension == 0 {
			return Manifest{}, fmt.Errorf("invalid multi-segment manifest")
		}
		if (manifest.GraphFile != "" && !safeBase(manifest.GraphFile)) || (manifest.FilterFile != "" && !safeBase(manifest.FilterFile)) {
			return Manifest{}, fmt.Errorf("invalid multi-segment view index")
		}
		var previousLSN uint64
		for position, segment := range manifest.Segments {
			if segment.Dimension != manifest.Dimension || segment.RecordsFile == "" || segment.VectorsFile == "" ||
				!safeBase(segment.RecordsFile) || !safeBase(segment.VectorsFile) ||
				(segment.GraphFile != "" && !safeBase(segment.GraphFile)) ||
				(segment.FilterFile != "" && !safeBase(segment.FilterFile)) ||
				(segment.TombstonesFile != "" && !safeBase(segment.TombstonesFile)) ||
				(position > 0 && segment.MaxLSN <= previousLSN) || segment.MaxLSN > manifest.MaxLSN {
				return Manifest{}, fmt.Errorf("invalid multi-segment entry %d", position)
			}
			previousLSN = segment.MaxLSN
		}
		if manifest.Segments[len(manifest.Segments)-1].MaxLSN != manifest.MaxLSN {
			return Manifest{}, fmt.Errorf("multi-segment manifest LSN mismatch")
		}
	default:
		return Manifest{}, fmt.Errorf("unsupported segment manifest format %d", manifest.Format)
	}
	return manifest, nil
}

// CleanupOrphans removes temporary, obsolete, and unpublished segment files
// after the current manifest and all of its referenced files have been
// validated. Unknown files are preserved so this routine cannot erase operator
// data or files introduced by a newer format.
func CleanupOrphans(directory string, manifest Manifest) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	referenced := map[string]struct{}{"MANIFEST.json": {}}
	for _, name := range []string{manifest.SegmentFile, manifest.RecordsFile, manifest.VectorsFile, manifest.GraphFile, manifest.FilterFile} {
		if name != "" {
			referenced[name] = struct{}{}
		}
	}
	for _, segment := range manifest.Segments {
		for _, name := range []string{segment.RecordsFile, segment.VectorsFile, segment.GraphFile, segment.FilterFile, segment.TombstonesFile} {
			if name != "" {
				referenced[name] = struct{}{}
			}
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, keep := referenced[name]; keep {
			continue
		}
		if !orphanCandidate(name) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphan segment %q: %w", name, err)
		}
	}
	return nil
}

func WriteGraph(directory, name string, data []byte) error {
	if !safeBase(name) || !strings.HasSuffix(name, ".graph") {
		return fmt.Errorf("unsafe graph file name")
	}
	return writeAtomic(filepath.Join(directory, name), data, 0o640)
}

func ReadGraph(directory string, manifest Manifest) ([]byte, error) {
	if manifest.GraphFile == "" || !safeBase(manifest.GraphFile) {
		return nil, fmt.Errorf("manifest does not reference a valid graph file")
	}
	return os.ReadFile(filepath.Join(directory, manifest.GraphFile))
}

func orphanCandidate(name string) bool {
	if strings.HasPrefix(name, ".tmp-") {
		return true
	}
	if !strings.HasPrefix(name, "segment-") {
		return false
	}
	for _, suffix := range []string{".records", ".vectors", ".vseg", ".graph", ".filter", ".tombstones"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func safeBase(value string) bool {
	return filepath.Base(value) == value && value != "." && value != ".."
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
