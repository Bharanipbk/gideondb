package core

import (
	"errors"
	"math"
	"testing"
)

func TestHalfPrecisionConversion(t *testing.T) {
	for _, value := range []float32{0, 1, -1, 0.5, 65504, -2.5, 6.1035156e-05, 5.9604645e-08} {
		if got := Float16ToFloat32(Float32ToFloat16(value)); got != value {
			t.Fatalf("float16 round trip %v = %v", value, got)
		}
	}
	if got := Float16ToFloat32(Float32ToFloat16(1.0004883)); got != 1 && got != 1.0009766 {
		t.Fatalf("float16 rounding = %v", got)
	}
	if !math.IsInf(float64(Float16ToFloat32(Float32ToFloat16(1e6))), 1) {
		t.Fatal("float16 overflow must become +Inf")
	}
	for _, value := range []float32{0, 1, -3, 1e30, 0.15625} {
		if got := BFloat16ToFloat32(Float32ToBFloat16(value)); math.Abs(float64(got-value)) > math.Abs(float64(value))/128 {
			t.Fatalf("bfloat16 round trip %v = %v", value, got)
		}
	}
	if got := BFloat16ToFloat32(Float32ToBFloat16(1.00390625)); got != 1 {
		t.Fatalf("bfloat16 ties to even = %v", got)
	}
}

func TestVectorFieldValidationAndPreparation(t *testing.T) {
	config := CollectionConfig{Name: "c", Dimension: 2, Metric: MetricCosine, ShardCount: 1, Vectors: map[string]VectorField{
		"title":   {Dimension: 3, Metric: MetricDot, Datatype: DatatypeFloat16},
		"colbert": {Dimension: 2, Metric: MetricCosine, MultiVector: true, Index: IndexConfig{Type: IndexHNSW}},
	}}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []map[string]VectorField{
		{"1x": {Dimension: 2, Metric: MetricDot}},
		{"x": {Dimension: 0, Metric: MetricDot}},
		{"x": {Dimension: 2, Metric: "bad"}},
		{"x": {Dimension: 2, Metric: MetricDot, Datatype: "int4"}},
		{"x": {Dimension: 2, Metric: MetricDot, Index: IndexConfig{Quantization: QuantizationBinary}}},
	} {
		invalid := config
		invalid.Vectors = bad
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Validate(%v) = %v", bad, err)
		}
	}
	record, err := config.PrepareNamedVectors(Record{ID: "a", Vectors: map[string][]float32{"title": {0.1, 1, 2}}, MultiVectors: map[string][][]float32{"colbert": {{1, 0}, {0, 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if record.Vectors["title"][0] != Float16ToFloat32(Float32ToFloat16(0.1)) || record.Vectors["title"][0] == 0.1 {
		t.Fatalf("float16 field must be rounded at write time: %v", record.Vectors["title"])
	}
	for _, bad := range []Record{
		{Vectors: map[string][]float32{"missing": {1}}},
		{Vectors: map[string][]float32{"title": {1, 2}}},
		{Vectors: map[string][]float32{"colbert": {1, 2}}},
		{MultiVectors: map[string][][]float32{"title": {{1, 2, 3}}}},
		{MultiVectors: map[string][][]float32{"colbert": {}}},
		{Vectors: map[string][]float32{"title": {1e6, 0, 0}}},
	} {
		if _, err := config.PrepareNamedVectors(bad); err == nil {
			t.Fatalf("PrepareNamedVectors(%v) succeeded", bad)
		}
	}
}

func TestMaxSimAndRerankValidation(t *testing.T) {
	dot := func(_ Metric, a, b []float32) (float32, error) {
		var sum float32
		for i := range a {
			sum += a[i] * b[i]
		}
		return sum, nil
	}
	score, err := MaxSim(MetricDot, [][]float32{{1, 0}, {0, 1}}, [][]float32{{0.5, 0}, {0, 2}, {1, 0}}, dot)
	if err != nil || score != 3 {
		t.Fatalf("MaxSim = %v, %v", score, err)
	}
	config := CollectionConfig{Name: "c", Dimension: 2, Metric: MetricDot, ShardCount: 1, Vectors: map[string]VectorField{"colbert": {Dimension: 2, Metric: MetricDot, MultiVector: true}}}
	if err := (RerankSpec{Type: RerankMaxSim, Field: "colbert", MultiVector: [][]float32{{1, 0}}}).Validate(config, 10); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []RerankSpec{
		{Type: "other"},
		{Type: RerankMaxSim, Field: "missing", MultiVector: [][]float32{{1, 0}}},
		{Type: RerankCrossEncoder, Model: "m"},
		{Type: RerankCrossEncoder, Model: "m", Query: "q", Document: "body"},
		{Type: RerankCrossEncoder, Model: "m", Query: "q", Candidates: MaxRerankCandidates + 1},
	} {
		if err := bad.Validate(config, 10); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("Validate(%+v) = %v", bad, err)
		}
	}
	if got := (RerankSpec{}).CandidateCount(10); got != DefaultRerankCandidates {
		t.Fatalf("default candidates = %d", got)
	}
	if got := (NamedQuery{TopK: 10}).CandidateCount(); got != 100 {
		t.Fatalf("named candidates = %d", got)
	}
}
