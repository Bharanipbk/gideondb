//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package segmentfile

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"sync"
	"syscall"
	"unsafe"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

// MappedVectors owns a read-only mmap of a validated VVEC file. Call Close
// only after all searches using the mapping have completed.
type MappedVectors struct {
	mu        sync.RWMutex
	mapped    []byte
	payload   []byte
	floats    []float32
	dimension int
	count     int
	maxLSN    uint64
}

func OpenMappedVectors(path string, expectedDimension uint32, expectedCount, expectedLSN uint64) (*MappedVectors, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < columnHeaderSize || uint64(info.Size()) > uint64(^uint(0)>>1) {
		return nil, fmt.Errorf("invalid vector column file size")
	}
	mapped, err := syscall.Mmap(int(file.Fd()), 0, int(info.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap vector column: %w", err)
	}
	fail := func(err error) (*MappedVectors, error) { _ = syscall.Munmap(mapped); return nil, err }
	header := mapped[:columnHeaderSize]
	if string(header[:4]) != string(vectorMagic[:]) {
		return fail(fmt.Errorf("invalid vector column magic"))
	}
	if binary.LittleEndian.Uint16(header[4:6]) != 1 || binary.LittleEndian.Uint16(header[6:8]) != 0 ||
		binary.LittleEndian.Uint32(header[12:16]) != 0 || binary.LittleEndian.Uint32(header[44:48]) != 0 {
		return fail(fmt.Errorf("unsupported vector column version, flags, or reserved fields"))
	}
	dimension := binary.LittleEndian.Uint32(header[8:12])
	count := binary.LittleEndian.Uint64(header[16:24])
	maxLSN := binary.LittleEndian.Uint64(header[24:32])
	length := binary.LittleEndian.Uint64(header[32:40])
	if dimension != expectedDimension || count != expectedCount || maxLSN != expectedLSN {
		return fail(fmt.Errorf("mapped vector column manifest mismatch"))
	}
	expectedLength, overflow := multiply3(count, uint64(dimension), 4)
	if overflow || length != expectedLength || length != uint64(len(mapped)-columnHeaderSize) {
		return fail(fmt.Errorf("mapped vector column length mismatch"))
	}
	payload := mapped[columnHeaderSize:]
	if columnChecksum(header, payload) != binary.LittleEndian.Uint32(header[40:44]) {
		return fail(fmt.Errorf("mapped vector column checksum mismatch"))
	}
	var floats []float32
	if len(payload) > 0 && nativeLittleEndian() && uintptr(unsafe.Pointer(&payload[0]))%unsafe.Alignof(float32(0)) == 0 {
		floats = unsafe.Slice((*float32)(unsafe.Pointer(&payload[0])), len(payload)/4)
	}
	return &MappedVectors{mapped: mapped, payload: payload, floats: floats, dimension: int(dimension), count: int(count), maxLSN: maxLSN}, nil
}

func (m *MappedVectors) Len() int       { m.mu.RLock(); defer m.mu.RUnlock(); return m.count }
func (m *MappedVectors) Dimension() int { return m.dimension }
func (m *MappedVectors) MappedBytes() uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return uint64(len(m.mapped))
}

func (m *MappedVectors) Score(metric core.Metric, query []float32, ordinal int) (float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ScoreAcquired(metric, query, ordinal)
}

// BeginRead pins the mapping for a multi-vector operation. It must be paired
// with EndRead. ScoreAcquired is valid only while this read lease is held.
func (m *MappedVectors) BeginRead() error {
	m.mu.RLock()
	if m.mapped == nil {
		m.mu.RUnlock()
		return fmt.Errorf("mapped vectors are closed")
	}
	return nil
}

func (m *MappedVectors) EndRead() { m.mu.RUnlock() }

func (m *MappedVectors) ScoreAcquired(metric core.Metric, query []float32, ordinal int) (float32, error) {
	querySquaredNorm := float32(0)
	if metric == core.MetricCosine {
		querySquaredNorm = distance.SquaredNorm(query)
	}
	return m.ScoreAcquiredPrepared(metric, query, querySquaredNorm, ordinal)
}

func (m *MappedVectors) ScoreAcquiredPrepared(metric core.Metric, query []float32, querySquaredNorm float32, ordinal int) (float32, error) {
	if m.mapped == nil {
		return 0, fmt.Errorf("mapped vectors are closed")
	}
	if len(query) != m.dimension {
		return 0, core.ErrDimensionMismatch
	}
	if ordinal < 0 || ordinal >= m.count {
		return 0, fmt.Errorf("%w: vector ordinal out of range", core.ErrInvalidArgument)
	}
	if m.floats != nil {
		start := ordinal * m.dimension
		return distance.ScoreWithQuerySquaredNorm(metric, query, querySquaredNorm, m.floats[start:start+m.dimension])
	}
	start := ordinal * m.dimension * 4
	var product, bb, squared float32
	for position, queryValue := range query {
		value := math.Float32frombits(binary.LittleEndian.Uint32(m.payload[start+position*4:]))
		switch metric {
		case core.MetricDot:
			product += queryValue * value
		case core.MetricL2:
			difference := queryValue - value
			squared += difference * difference
		case core.MetricCosine:
			product += queryValue * value
			bb += value * value
		default:
			return 0, fmt.Errorf("%w: unsupported metric %q", core.ErrInvalidArgument, metric)
		}
	}
	switch metric {
	case core.MetricDot:
		return product, nil
	case core.MetricL2:
		return -squared, nil
	default:
		if querySquaredNorm == 0 || bb == 0 {
			return 0, nil
		}
		return product / float32(math.Sqrt(float64(querySquaredNorm*bb))), nil
	}
}

func (m *MappedVectors) VectorCopy(ordinal int) ([]float32, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.mapped == nil {
		return nil, fmt.Errorf("mapped vectors are closed")
	}
	if ordinal < 0 || ordinal >= m.count {
		return nil, fmt.Errorf("%w: vector ordinal out of range", core.ErrInvalidArgument)
	}
	result := make([]float32, m.dimension)
	if m.floats != nil {
		copy(result, m.floats[ordinal*m.dimension:(ordinal+1)*m.dimension])
		return result, nil
	}
	start := ordinal * m.dimension * 4
	for position := range result {
		result[position] = math.Float32frombits(binary.LittleEndian.Uint32(m.payload[start+position*4:]))
	}
	return result, nil
}

func (m *MappedVectors) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mapped == nil {
		return nil
	}
	err := syscall.Munmap(m.mapped)
	m.mapped, m.payload, m.floats, m.count = nil, nil, nil, 0
	return err
}

func nativeLittleEndian() bool {
	value := uint16(1)
	return *(*byte)(unsafe.Pointer(&value)) == 1
}
