package core

import "testing"

func TestDiskANNIndexConfig(t *testing.T) {
	base := CollectionConfig{Name: "c", Dimension: 4, Metric: MetricL2, ShardCount: 1, Index: IndexConfig{Type: IndexDiskANN}}
	normalized := base.Normalized()
	if normalized.Index.MaxDegree != 64 || normalized.Index.Alpha != 1.2 || normalized.Index.EFConstruction != 128 || normalized.Index.EFSearch != 100 {
		t.Fatalf("defaults = %+v", normalized.Index)
	}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []IndexConfig{
		{Type: IndexDiskANN, MaxDegree: 2},
		{Type: IndexDiskANN, MaxDegree: 300},
		{Type: IndexDiskANN, Alpha: 0.5},
		{Type: IndexDiskANN, Alpha: 3},
		{Type: IndexDiskANN, MaxDegree: 64, EFConstruction: 32},
		{Type: IndexDiskANN, EFSearch: 20000},
		{Type: IndexDiskANN, M: 16},
		{Type: IndexDiskANN, Quantization: QuantizationBinary},
		{Type: IndexHNSW, MaxDegree: 32},
		{Type: IndexFlat, Alpha: 1.2},
	}
	for _, index := range invalid {
		config := base
		config.Index = index
		if err := config.Validate(); err == nil {
			t.Errorf("Validate(%+v) succeeded", index)
		}
	}
}
