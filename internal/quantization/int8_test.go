package quantization

import (
	"math"
	"math/rand/v2"
	"sort"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
	"github.com/Bharanipbk/gideondb/internal/distance"
)

func TestInt8QuantizationErrorAndMetrics(t *testing.T) {
	a := []float32{-1, -0.25, 0, 0.5, 1}
	b := []float32{0.75, -0.5, 0.25, 0, -1}
	qa, err := QuantizeInt8(a)
	if err != nil {
		t.Fatal(err)
	}
	qb, _ := QuantizeInt8(b)
	for position, value := range qa.Dequantize() {
		if math.Abs(float64(value-a[position])) > float64(qa.Scale)/2+1e-6 {
			t.Fatalf("position %d error exceeds half a quantization step", position)
		}
	}
	for _, metric := range []core.Metric{core.MetricDot, core.MetricCosine, core.MetricL2} {
		exact, _ := distance.Score(metric, a, b)
		approximate, err := Score(metric, qa, qb)
		if err != nil || math.Abs(float64(exact-approximate)) > 0.03 {
			t.Fatalf("metric %s exact=%g approximate=%g err=%v", metric, exact, approximate, err)
		}
	}
	if _, err := QuantizeInt8([]float32{float32(math.NaN())}); err == nil {
		t.Fatal("accepted a non-finite vector")
	}
}

func TestInt8RecallAgainstExactFlatScores(t *testing.T) {
	const count, dimension, queries, k = 1000, 32, 25, 10
	random := rand.New(rand.NewPCG(42, 9))
	vectors := make([][]float32, count)
	quantized := make([]Int8Vector, count)
	for id := range vectors {
		vectors[id] = make([]float32, dimension)
		for position := range vectors[id] {
			vectors[id][position] = random.Float32()*2 - 1
		}
		quantized[id], _ = QuantizeInt8(vectors[id])
	}
	matches := 0
	for queryID := range queries {
		query := vectors[queryID*17]
		quantizedQuery, _ := QuantizeInt8(query)
		exact, approximate := make([]scoredID, count), make([]scoredID, count)
		for id := range vectors {
			exact[id].id, approximate[id].id = id, id
			exact[id].score, _ = distance.Score(core.MetricCosine, query, vectors[id])
			approximate[id].score, _ = Score(core.MetricCosine, quantizedQuery, quantized[id])
		}
		sortScores(exact)
		sortScores(approximate)
		oracle := make(map[int]struct{}, k)
		for _, item := range exact[:k] {
			oracle[item.id] = struct{}{}
		}
		for _, item := range approximate[:k] {
			if _, exists := oracle[item.id]; exists {
				matches++
			}
		}
	}
	recall := float64(matches) / float64(queries*k)
	t.Logf("symmetric int8 cosine recall@%d = %.3f", k, recall)
	if recall < 0.95 {
		t.Fatalf("recall@%d = %.3f, want at least 0.95", k, recall)
	}
}

type scoredID struct {
	id    int
	score float32
}

func sortScores(values []scoredID) {
	sort.Slice(values, func(i, j int) bool {
		return values[i].score > values[j].score || (values[i].score == values[j].score && values[i].id < values[j].id)
	})
}

func BenchmarkInt8Cosine768(b *testing.B) {
	a, vector := make([]float32, 768), make([]float32, 768)
	for position := range a {
		a[position], vector[position] = float32(position%11), float32(position%7)
	}
	qa, _ := QuantizeInt8(a)
	qv, _ := QuantizeInt8(vector)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = Score(core.MetricCosine, qa, qv)
	}
}

func BenchmarkQuantizeInt8_768(b *testing.B) {
	vector := make([]float32, 768)
	for position := range vector {
		vector[position] = float32(position%11) / 11
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = QuantizeInt8(vector)
	}
}
