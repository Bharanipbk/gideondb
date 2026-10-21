package segment

import (
	"hash/fnv"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/index/pq"
)

// UsesPQ reports whether segments of this collection with count records are
// product-quantized. Smaller segments, and every mutable segment, keep exact
// flat search.
func UsesPQ(config core.CollectionConfig, count int) bool {
	return config.Index.Type == core.IndexFlat && config.Index.Quantization == core.QuantizationPQ && count >= pq.MinTrainingVectors
}

// BuildPQ trains codebooks on records and returns the serialized codebook and
// codes in records order, or nil when the segment is not product-quantized.
func BuildPQ(config core.CollectionConfig, records []core.Record) ([]byte, error) {
	if !UsesPQ(config, len(records)) {
		return nil, nil
	}
	vectors := make([][]float32, len(records))
	for position, record := range records {
		vectors[position] = record.Vector
	}
	codebook, err := pq.Train(vectors, config.Dimension, config.Index.PQSubspaces, config.Metric)
	if err != nil {
		return nil, err
	}
	codes, err := codebook.EncodeAll(len(vectors), func(position int) ([]float32, error) { return vectors[position], nil })
	if err != nil {
		return nil, err
	}
	return pq.Marshal(codebook, codes, pqFingerprint(records))
}

// pqFingerprint hashes the record keys in order so persisted codes are only
// reused for the exact record layout they were written for.
func pqFingerprint(records []core.Record) uint64 {
	hash := fnv.New64a()
	for _, record := range records {
		_, _ = hash.Write([]byte(key(record.Namespace, record.ID)))
		_, _ = hash.Write([]byte{0xff})
	}
	return hash.Sum64()
}

// buildPQIndex restores persisted codes when they match the configuration and
// record layout, and otherwise trains from the segment's own vectors. Older
// checkpoints without a pq file therefore retrain once on load.
func buildPQIndex(config core.CollectionConfig, records []core.Record, vectorAt func(position int) ([]float32, error), score pq.Scorer, persisted []byte) (*pq.Index, error) {
	ids := make([]uint64, len(records))
	for position := range ids {
		ids[position] = uint64(position + 1)
	}
	if persisted != nil {
		codebook, codes, fingerprint, err := pq.Unmarshal(persisted)
		if err == nil && codebook.Dimension == config.Dimension && codebook.Subspaces == config.Index.PQSubspaces &&
			codebook.Metric == config.Metric && len(codes) == len(records)*codebook.Subspaces && fingerprint == pqFingerprint(records) {
			return pq.NewWithCodes(codebook, config.Index.Oversampling, score, ids, codes)
		}
	}
	vectors := make([][]float32, len(records))
	for position := range vectors {
		vector, err := vectorAt(position)
		if err != nil {
			return nil, err
		}
		vectors[position] = vector
	}
	codebook, err := pq.Train(vectors, config.Dimension, config.Index.PQSubspaces, config.Metric)
	if err != nil {
		return nil, err
	}
	codes, err := codebook.EncodeAll(len(vectors), func(position int) ([]float32, error) { return vectors[position], nil })
	if err != nil {
		return nil, err
	}
	return pq.NewWithCodes(codebook, config.Index.Oversampling, score, ids, codes)
}
