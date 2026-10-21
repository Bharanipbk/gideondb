package distance

import (
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// referenceDot and referenceL2 accumulate in float64, so they are a fixed
// point that every float32 kernel must stay close to.
func referenceDot(a, b []float32) float64 {
	var sum float64
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

func referenceL2(a, b []float32) float64 {
	var sum float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sum += d * d
	}
	return sum
}

// kernelTolerance bounds float32 summation error relative to the sum of
// absolute terms, which is what reordering a reduction can change.
func kernelTolerance(a, b []float32, l2 bool) float64 {
	var magnitude float64
	for i := range a {
		if l2 {
			d := float64(a[i]) - float64(b[i])
			magnitude += d * d
		} else {
			magnitude += math.Abs(float64(a[i]) * float64(b[i]))
		}
	}
	return 1e-6*float64(len(a)+1)*magnitude + 1e-30
}

func checkKernels(t *testing.T, a, b []float32) {
	t.Helper()
	wantDot, wantL2 := referenceDot(a, b), referenceL2(a, b)
	dotTolerance, l2Tolerance := kernelTolerance(a, b, false), kernelTolerance(a, b, true)
	for _, kernel := range []struct {
		name string
		dot  func(a, b []float32) float32
		l2   func(a, b []float32) float32
	}{{"active", dot, l2Squared}, {"generic", dotGeneric, l2SquaredGeneric}} {
		if got := float64(kernel.dot(a, b)); math.Abs(got-wantDot) > dotTolerance {
			t.Fatalf("%s dot dimension=%d got=%g want=%g", kernel.name, len(a), got, wantDot)
		}
		if got := float64(kernel.l2(a, b)); math.Abs(got-wantL2) > l2Tolerance {
			t.Fatalf("%s l2 dimension=%d got=%g want=%g", kernel.name, len(a), got, wantL2)
		}
	}
	// The active and portable kernels must agree with each other too, so a
	// build with or without assembly ranks candidates the same way.
	if got, want := float64(dot(a, b)), float64(dotGeneric(a, b)); math.Abs(got-want) > dotTolerance {
		t.Fatalf("dot assembly/generic disagree dimension=%d: %g vs %g", len(a), got, want)
	}
	if got, want := float64(l2Squared(a, b)), float64(l2SquaredGeneric(a, b)); math.Abs(got-want) > l2Tolerance {
		t.Fatalf("l2 assembly/generic disagree dimension=%d: %g vs %g", len(a), got, want)
	}
}

func TestKernelsMatchReferenceForEveryLength(t *testing.T) {
	random := rand.New(rand.NewSource(11))
	for dimension := 0; dimension <= 300; dimension++ {
		a, b := make([]float32, dimension), make([]float32, dimension)
		for i := range a {
			a[i], b[i] = random.Float32()*20-10, random.Float32()*20-10
		}
		checkKernels(t, a, b)
	}
	for _, dimension := range []int{768, 1024, 1536, 4096} {
		a, b := make([]float32, dimension), make([]float32, dimension)
		for i := range a {
			a[i], b[i] = float32(random.NormFloat64()), float32(random.NormFloat64())
		}
		checkKernels(t, a, b)
	}
}

func TestKernelsReadOnlyTheirSlices(t *testing.T) {
	// Subslices of a larger buffer must not read past their own length.
	buffer := make([]float32, 64)
	for i := range buffer {
		buffer[i] = 1000
	}
	a, b := buffer[0:16:16], buffer[32:48:48]
	for i := range a {
		a[i], b[i] = 1, 2
	}
	if got := dot(a, b); got != 32 {
		t.Fatalf("dot = %g, want 32", got)
	}
	if got := l2Squared(a, b); got != 16 {
		t.Fatalf("l2 = %g, want 16", got)
	}
}

func FuzzKernels(f *testing.F) {
	f.Add([]byte{0, 0, 128, 63, 0, 0, 0, 64})
	f.Add(make([]byte, 8*37))
	f.Fuzz(func(t *testing.T, data []byte) {
		count := len(data) / 8
		a, b := make([]float32, count), make([]float32, count)
		for i := range count {
			a[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[8*i:]))
			b[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[8*i+4:]))
			// Keep values finite and away from overflow; NaN and Inf
			// propagate in any summation order and say nothing about it.
			if !finiteSmall(a[i]) || !finiteSmall(b[i]) {
				return
			}
		}
		checkKernels(t, a, b)
	})
}

func finiteSmall(v float32) bool {
	return !math.IsNaN(float64(v)) && math.Abs(float64(v)) < 1e15
}

// legacyDot and legacyL2 are the four-lane kernels used before the NEON and
// eight-lane versions, kept to benchmark against.
func legacyDot(a, b []float32) float32 {
	var sum0, sum1, sum2, sum3 float32
	i := 0
	for ; i+3 < len(a); i += 4 {
		sum0 += a[i] * b[i]
		sum1 += a[i+1] * b[i+1]
		sum2 += a[i+2] * b[i+2]
		sum3 += a[i+3] * b[i+3]
	}
	sum := (sum0 + sum1) + (sum2 + sum3)
	for ; i < len(a); i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func legacyL2(a, b []float32) float32 {
	var sum0, sum1, sum2, sum3 float32
	i := 0
	for ; i+3 < len(a); i += 4 {
		d0, d1 := a[i]-b[i], a[i+1]-b[i+1]
		d2, d3 := a[i+2]-b[i+2], a[i+3]-b[i+3]
		sum0 += d0 * d0
		sum1 += d1 * d1
		sum2 += d2 * d2
		sum3 += d3 * d3
	}
	sum := (sum0 + sum1) + (sum2 + sum3)
	for ; i < len(a); i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

var benchmarkSink float32

func BenchmarkKernels(b *testing.B) {
	kernels := []struct {
		name string
		fn   func(a, b []float32) float32
	}{
		{"dot/legacy", legacyDot}, {"dot/generic", dotGeneric}, {"dot/active", dot},
		{"l2/legacy", legacyL2}, {"l2/generic", l2SquaredGeneric}, {"l2/active", l2Squared},
	}
	for _, dimension := range []int{100, 128, 384, 768, 1536} {
		x, y := make([]float32, dimension), make([]float32, dimension)
		for i := range x {
			x[i], y[i] = float32(i%11)*0.1, float32(i%7)*0.2
		}
		for _, kernel := range kernels {
			b.Run(fmt.Sprintf("%s/d=%d", kernel.name, dimension), func(b *testing.B) {
				for b.Loop() {
					benchmarkSink = kernel.fn(x, y)
				}
			})
		}
	}
}
