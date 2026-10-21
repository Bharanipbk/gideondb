package core

import "testing"

func TestPQConfigValidation(t *testing.T) {
	base := CollectionConfig{Name: "c", Dimension: 12, Metric: MetricL2, ShardCount: 1}
	valid := base
	valid.Index = IndexConfig{Type: IndexFlat, Quantization: QuantizationPQ}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if normalized := valid.Normalized(); normalized.Index.PQSubspaces != 3 || normalized.Index.Oversampling != 8 {
		t.Fatalf("defaults=%+v", normalized.Index)
	}
	for _, invalid := range []IndexConfig{
		{Type: IndexHNSW, Quantization: QuantizationPQ},
		{Type: IndexFlat, Quantization: QuantizationPQ, PQSubspaces: 5},
		{Type: IndexFlat, Quantization: QuantizationPQ, PQSubspaces: 24},
		{Type: IndexFlat, Quantization: QuantizationPQ, PQSubspaces: -1},
		{Type: IndexFlat, Quantization: QuantizationPQ, Oversampling: 101},
		{Type: IndexFlat, Quantization: QuantizationBinary, PQSubspaces: 4},
		{Type: IndexFlat, PQSubspaces: 4},
	} {
		config := base
		config.Index = invalid
		if err := config.Validate(); err == nil {
			t.Fatalf("expected %+v to be rejected", invalid)
		}
	}
}
