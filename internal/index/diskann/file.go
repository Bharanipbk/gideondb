package diskann

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

// File layout, version 1 (all integers little-endian):
//
//	header   64 bytes: magic "GDAN", version u16, flags u16, dimension u32,
//	         max degree u32, count u64, medoid u32, metric u8, 3 reserved,
//	         tag u64, payload length u64, CRC-32C u32, 12 reserved
//	ranges   dimension float32 minimums, then dimension float32 steps used
//	         for the 8-bit navigation codes (unit vectors for cosine)
//	records  count fixed-size records: dimension float32, degree u32,
//	         max degree u32 neighbour ordinals (unused slots are zero)
//
// The CRC covers the header (with the CRC field zeroed) and the payload.
// Tag is an opaque caller value; segments store a digest of the record keys
// so a graph is never paired with records in a different order.
const (
	headerSize    = 64
	formatVersion = 1
)

var (
	fileMagic = [4]byte{'G', 'D', 'A', 'N'}
	crcTable  = crc32.MakeTable(crc32.Castagnoli)
	metrics   = map[core.Metric]byte{core.MetricDot: 1, core.MetricL2: 2, core.MetricCosine: 3}
)

func recordSize(dimension, maxDegree int) int { return 4*dimension + 4 + 4*maxDegree }

// WriteFile writes vectors and graph atomically to path: a temporary file in
// the same directory is synced and renamed into place.
func WriteFile(path string, metric core.Metric, vectors [][]float32, graph *Graph, maxDegree int, tag uint64) error {
	if len(vectors) == 0 || len(graph.Neighbors) != len(vectors) || metrics[metric] == 0 {
		return fmt.Errorf("%w: invalid diskann file contents", core.ErrInvalidArgument)
	}
	dimension := len(vectors[0])
	ranges := codeRanges(metric, vectors)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-diskann-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	size := recordSize(dimension, maxDegree)
	payloadLength := uint64(8*dimension) + uint64(len(vectors))*uint64(size)
	header := make([]byte, headerSize)
	copy(header, fileMagic[:])
	binary.LittleEndian.PutUint16(header[4:], formatVersion)
	binary.LittleEndian.PutUint32(header[8:], uint32(dimension))
	binary.LittleEndian.PutUint32(header[12:], uint32(maxDegree))
	binary.LittleEndian.PutUint64(header[16:], uint64(len(vectors)))
	binary.LittleEndian.PutUint32(header[24:], graph.Medoid)
	header[28] = metrics[metric]
	binary.LittleEndian.PutUint64(header[32:], tag)
	binary.LittleEndian.PutUint64(header[40:], payloadLength)
	if _, err := temporary.Write(header); err != nil {
		return err
	}
	writer := bufio.NewWriterSize(temporary, 1<<20)
	checksum := crc32.Update(0, crcTable, header)
	record := make([]byte, size)
	write := func(data []byte) error {
		checksum = crc32.Update(checksum, crcTable, data)
		_, err := writer.Write(data)
		return err
	}
	rangeBytes := make([]byte, 4*len(ranges))
	for i, value := range ranges {
		binary.LittleEndian.PutUint32(rangeBytes[4*i:], math.Float32bits(value))
	}
	if err := write(rangeBytes); err != nil {
		return err
	}
	for position, vector := range vectors {
		clear(record)
		if len(vector) != dimension {
			return core.ErrDimensionMismatch
		}
		neighbors := graph.Neighbors[position]
		if len(neighbors) > maxDegree {
			return fmt.Errorf("%w: diskann node degree exceeds maximum", core.ErrInvalidArgument)
		}
		for i, value := range vector {
			binary.LittleEndian.PutUint32(record[4*i:], math.Float32bits(value))
		}
		binary.LittleEndian.PutUint32(record[4*dimension:], uint32(len(neighbors)))
		for i, neighbor := range neighbors {
			binary.LittleEndian.PutUint32(record[4*dimension+4+4*i:], neighbor)
		}
		if err := write(record); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	binary.LittleEndian.PutUint32(header[48:], checksum)
	if _, err := temporary.WriteAt(header[48:52], 48); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return err
	}
	if directory, err := os.Open(filepath.Dir(path)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

// codeRanges returns per-dimension minimums followed by quantization steps
// over the vectors the codes are taken from (unit vectors for cosine).
func codeRanges(metric core.Metric, vectors [][]float32) []float32 {
	dimension := len(vectors[0])
	ranges := make([]float32, 2*dimension)
	maximums := make([]float32, dimension)
	unit := make([]float32, dimension)
	for position, vector := range vectors {
		normalized := codeInput(metric, vector, unit)
		for i, value := range normalized {
			if position == 0 || value < ranges[i] {
				ranges[i] = value
			}
			if position == 0 || value > maximums[i] {
				maximums[i] = value
			}
		}
	}
	for i := range maximums {
		ranges[dimension+i] = (maximums[i] - ranges[i]) / 255
	}
	return ranges
}

// codeInput returns vector, or its unit form in scratch for cosine.
func codeInput(metric core.Metric, vector, scratch []float32) []float32 {
	if metric != core.MetricCosine {
		return vector
	}
	scale := float32(1)
	if norm := distance.SquaredNorm(vector); norm > 0 {
		scale = 1 / float32(math.Sqrt(float64(norm)))
	}
	for i, value := range vector {
		scratch[i] = value * scale
	}
	return scratch
}

type fileHeader struct {
	dimension, maxDegree, count int
	medoid                      uint32
	metric                      core.Metric
	tag                         uint64
}

func parseHeader(mapped []byte) (fileHeader, error) {
	if len(mapped) < headerSize || string(mapped[:4]) != string(fileMagic[:]) {
		return fileHeader{}, fmt.Errorf("invalid diskann file magic")
	}
	header := mapped[:headerSize]
	if version := binary.LittleEndian.Uint16(header[4:]); version != formatVersion {
		return fileHeader{}, fmt.Errorf("unsupported diskann file version %d", version)
	}
	if binary.LittleEndian.Uint16(header[6:]) != 0 {
		return fileHeader{}, fmt.Errorf("unsupported diskann file flags")
	}
	for _, value := range append(append([]byte(nil), header[29:32]...), header[52:64]...) {
		if value != 0 {
			return fileHeader{}, fmt.Errorf("nonzero diskann reserved field")
		}
	}
	h := fileHeader{
		dimension: int(binary.LittleEndian.Uint32(header[8:])), maxDegree: int(binary.LittleEndian.Uint32(header[12:])),
		medoid: binary.LittleEndian.Uint32(header[24:]), tag: binary.LittleEndian.Uint64(header[32:]),
	}
	for metric, code := range metrics {
		if code == header[28] {
			h.metric = metric
		}
	}
	count := binary.LittleEndian.Uint64(header[16:])
	if h.metric == "" || h.dimension <= 0 || h.dimension > 1<<16 || h.maxDegree <= 0 || h.maxDegree > 1024 || count == 0 || count > 1<<32-1 || uint64(h.medoid) >= count {
		return fileHeader{}, fmt.Errorf("invalid diskann file header")
	}
	h.count = int(count)
	expected := uint64(8*h.dimension) + count*uint64(recordSize(h.dimension, h.maxDegree))
	if binary.LittleEndian.Uint64(header[40:]) != expected || uint64(len(mapped)-headerSize) != expected {
		return fileHeader{}, fmt.Errorf("diskann file length mismatch")
	}
	zeroed := append([]byte(nil), header...)
	binary.LittleEndian.PutUint32(zeroed[48:], 0)
	checksum := crc32.Update(crc32.Update(0, crcTable, zeroed), crcTable, mapped[headerSize:])
	if checksum != binary.LittleEndian.Uint32(header[48:]) {
		return fileHeader{}, fmt.Errorf("diskann file checksum mismatch")
	}
	return h, nil
}
