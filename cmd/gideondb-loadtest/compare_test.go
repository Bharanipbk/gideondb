package main

import (
	"math"
	"testing"
)

func TestOpenSearchPrepareNormalizesForCosine(t *testing.T) {
	o := &openSearchEngine{normalize: true}
	got := o.prepare([]float32{3, 4})
	if math.Abs(float64(got[0])-0.6) > 1e-6 || math.Abs(float64(got[1])-0.8) > 1e-6 {
		t.Fatalf("prepare = %v, want [0.6 0.8]", got)
	}
	if zero := o.prepare([]float32{0, 0}); zero[0] != 0 || zero[1] != 0 {
		t.Fatalf("zero vector changed: %v", zero)
	}
	if raw := (&openSearchEngine{}).prepare([]float32{3, 4}); raw[0] != 3 {
		t.Fatalf("l2 vectors must pass through unchanged: %v", raw)
	}
}

func TestParallelErrReturnsFirstError(t *testing.T) {
	err := parallelErr(100, 4, func(i int) error {
		if i == 37 {
			return errTest
		}
		return nil
	})
	if err != errTest {
		t.Fatalf("parallelErr = %v, want errTest", err)
	}
	if err := parallelErr(10, 4, func(int) error { return nil }); err != nil {
		t.Fatalf("parallelErr = %v, want nil", err)
	}
}

func TestContainerMemoryMiBWithoutContainer(t *testing.T) {
	if got := containerMemoryMiB(""); got != 0 {
		t.Fatalf("containerMemoryMiB(\"\") = %v, want 0", got)
	}
}

type testError string

func (e testError) Error() string { return string(e) }

const errTest = testError("boom")
