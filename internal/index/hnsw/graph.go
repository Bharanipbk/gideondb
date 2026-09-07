package hnsw

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index"
)

const graphHeaderSize = 64

var graphMagic = [4]byte{'G', 'H', 'S', 'W'}
var graphCRCTable = crc32.MakeTable(crc32.Castagnoli)

// MarshalGraph encodes graph topology in a versioned checksummed format.
// Vectors remain in the checkpoint vector column and are supplied to LoadGraph.
func (h *Index) MarshalGraph() ([]byte, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	payloadSize := uint64(0)
	for ordinal, item := range h.nodes {
		payloadSize += 16
		for layer := 0; layer <= item.level; layer++ {
			neighbors := h.neighborsAt(ordinal, layer)
			payloadSize += 4 + uint64(len(neighbors))*4
		}
	}
	if payloadSize > uint64(^uint(0)>>1)-graphHeaderSize {
		return nil, fmt.Errorf("hnsw graph exceeds platform address space")
	}
	data := make([]byte, graphHeaderSize+int(payloadSize))
	copy(data[:4], graphMagic[:])
	binary.LittleEndian.PutUint16(data[4:6], 1)
	binary.LittleEndian.PutUint32(data[8:12], uint32(h.dimension))
	binary.LittleEndian.PutUint32(data[12:16], uint32(h.m))
	binary.LittleEndian.PutUint32(data[16:20], uint32(h.efConstruction))
	binary.LittleEndian.PutUint32(data[20:24], uint32(h.efSearch))
	binary.LittleEndian.PutUint64(data[24:32], uint64(len(h.nodes)))
	binary.LittleEndian.PutUint64(data[32:40], uint64(int64(h.entry)))
	binary.LittleEndian.PutUint32(data[40:44], uint32(int32(h.maxLevel)))
	data[44] = metricCode(h.metric)
	binary.LittleEndian.PutUint64(data[48:56], payloadSize)
	offset := graphHeaderSize
	for ordinal, item := range h.nodes {
		binary.LittleEndian.PutUint64(data[offset:offset+8], item.id)
		binary.LittleEndian.PutUint32(data[offset+8:offset+12], uint32(item.level))
		if item.deleted {
			binary.LittleEndian.PutUint32(data[offset+12:offset+16], 1)
		}
		offset += 16
		for layer := 0; layer <= item.level; layer++ {
			neighbors := h.neighborsAt(ordinal, layer)
			binary.LittleEndian.PutUint32(data[offset:offset+4], uint32(len(neighbors)))
			offset += 4
			for _, neighbor := range neighbors {
				binary.LittleEndian.PutUint32(data[offset:offset+4], uint32(neighbor))
				offset += 4
			}
		}
	}
	binary.LittleEndian.PutUint32(data[56:60], graphChecksum(data))
	return data, nil
}

// LoadGraph restores topology using vectors from the checkpoint vector column.
func LoadGraph(cfg index.Config, ids []uint64, vectors [][]float32, data []byte) (*Index, error) {
	if len(data) < graphHeaderSize || string(data[:4]) != string(graphMagic[:]) {
		return nil, fmt.Errorf("invalid hnsw graph magic")
	}
	if binary.LittleEndian.Uint16(data[4:6]) != 1 || binary.LittleEndian.Uint16(data[6:8]) != 0 ||
		binary.LittleEndian.Uint32(data[60:64]) != 0 {
		return nil, fmt.Errorf("unsupported hnsw graph version or reserved fields")
	}
	count := binary.LittleEndian.Uint64(data[24:32])
	length := binary.LittleEndian.Uint64(data[48:56])
	if count > uint64(^uint(0)>>1) || length != uint64(len(data)-graphHeaderSize) || count != uint64(len(ids)) || len(ids) != len(vectors) {
		return nil, fmt.Errorf("hnsw graph length or record count mismatch")
	}
	if binary.LittleEndian.Uint32(data[8:12]) != uint32(cfg.Dimension) ||
		binary.LittleEndian.Uint32(data[12:16]) != uint32(cfg.M) ||
		binary.LittleEndian.Uint32(data[16:20]) != uint32(cfg.EFConstruction) ||
		binary.LittleEndian.Uint32(data[20:24]) != uint32(cfg.EFSearch) || data[44] != metricCode(cfg.Metric) {
		return nil, fmt.Errorf("hnsw graph configuration mismatch")
	}
	if graphChecksum(data) != binary.LittleEndian.Uint32(data[56:60]) {
		return nil, fmt.Errorf("hnsw graph checksum mismatch")
	}
	h, err := New(cfg)
	if err != nil {
		return nil, err
	}
	h.nodes = make([]node, int(count))
	h.packedOffsets = make([][]int, int(count))
	h.packedNeighbors = make([]int, 0, int(count)*cfg.M)
	h.immutableGraph = true
	h.ordinals = make(map[uint64]int, int(count))
	h.vectors = make([]float32, 0, int(count)*cfg.Dimension)
	h.constructionVisited = make([]uint32, int(count))
	offset := graphHeaderSize
	for ordinal := range h.nodes {
		if offset+16 > len(data) {
			return nil, fmt.Errorf("truncated hnsw graph node")
		}
		id := binary.LittleEndian.Uint64(data[offset : offset+8])
		level := int(binary.LittleEndian.Uint32(data[offset+8 : offset+12]))
		flags := binary.LittleEndian.Uint32(data[offset+12 : offset+16])
		offset += 16
		if id != ids[ordinal] || level > 32 || flags > 1 || len(vectors[ordinal]) != cfg.Dimension {
			return nil, fmt.Errorf("invalid hnsw graph node %d", ordinal)
		}
		item := node{id: id, level: level, deleted: flags == 1}
		offsets := make([]int, level+2)
		for layer := 0; layer <= level; layer++ {
			if offset+4 > len(data) {
				return nil, fmt.Errorf("truncated hnsw graph layer")
			}
			neighborCount := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
			offset += 4
			if neighborCount > cfg.M || neighborCount > (len(data)-offset)/4 {
				return nil, fmt.Errorf("invalid hnsw neighbor count")
			}
			offsets[layer] = len(h.packedNeighbors)
			for range neighborCount {
				neighbor := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
				offset += 4
				if neighbor >= int(count) || neighbor == ordinal {
					return nil, fmt.Errorf("invalid hnsw neighbor ordinal")
				}
				h.packedNeighbors = append(h.packedNeighbors, neighbor)
			}
		}
		offsets[level+1] = len(h.packedNeighbors)
		h.packedOffsets[ordinal] = offsets
		h.nodes[ordinal] = item
		if !item.deleted {
			h.ordinals[id] = ordinal
			h.live++
		}
		h.vectors = append(h.vectors, vectors[ordinal]...)
	}
	if offset != len(data) {
		return nil, fmt.Errorf("hnsw graph has trailing payload")
	}
	h.entry = int(int64(binary.LittleEndian.Uint64(data[32:40])))
	h.maxLevel = int(int32(binary.LittleEndian.Uint32(data[40:44])))
	if (count == 0 && (h.entry != -1 || h.maxLevel != -1)) || (count > 0 && (h.entry < 0 || h.entry >= int(count) || h.maxLevel != h.nodes[h.entry].level)) {
		return nil, fmt.Errorf("invalid hnsw graph entry point")
	}
	return h, nil
}

func graphChecksum(data []byte) uint32 {
	value := crc32.Update(0, graphCRCTable, data[4:56])
	value = crc32.Update(value, graphCRCTable, data[60:64])
	return crc32.Update(value, graphCRCTable, data[graphHeaderSize:])
}

func metricCode(metric core.Metric) byte {
	switch metric {
	case core.MetricCosine:
		return 1
	case core.MetricDot:
		return 2
	case core.MetricL2:
		return 3
	default:
		return math.MaxUint8
	}
}
