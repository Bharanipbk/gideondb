package distance

import (
	"math"
	"math/rand"
	"testing"

	"github.com/Bharanipbk/gideondb/internal/core"
)

func TestScore(t *testing.T) {
	tests := []struct {
		name   string
		metric core.Metric
		want   float32
	}{
		{name: "dot", metric: core.MetricDot, want: 11},
		{name: "l2", metric: core.MetricL2, want: -8},
		{name: "cosine", metric: core.MetricCosine, want: 11 / float32(math.Sqrt(125))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Score(test.metric, []float32{1, 2}, []float32{3, 4})
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(float64(got-test.want)) > 1e-6 {
				t.Fatalf("Score() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestScoreRejectsDimensionMismatch(t *testing.T) {
	if _, err := Score(core.MetricDot, []float32{1}, []float32{1, 2}); err == nil {
		t.Fatal("expected dimension error")
	}
}

func TestUnrolledKernelsMatchScalarReference(t *testing.T) {
	random := rand.New(rand.NewSource(7))
	for _, dimension := range []int{1, 3, 4, 7, 32, 127, 768} {
		a, c := make([]float32, dimension), make([]float32, dimension)
		for i := range a {
			a[i], c[i] = random.Float32()*2-1, random.Float32()*2-1
		}
		var dotWant, l2Want, aa, bb float32
		for i := range a {
			dotWant += a[i] * c[i]
			difference := a[i] - c[i]
			l2Want += difference * difference
			aa += a[i] * a[i]
			bb += c[i] * c[i]
		}
		cosineWant := dotWant / float32(math.Sqrt(float64(aa*bb)))
		for _, test := range []struct {
			metric core.Metric
			want   float32
		}{{core.MetricDot, dotWant}, {core.MetricL2, -l2Want}, {core.MetricCosine, cosineWant}} {
			got, err := Score(test.metric, a, c)
			if err != nil {
				t.Fatal(err)
			}
			tolerance := 1e-5 * math.Max(1, math.Abs(float64(test.want)))
			if math.Abs(float64(got-test.want)) > tolerance {
				t.Fatalf("dimension=%d metric=%s got=%g want=%g", dimension, test.metric, got, test.want)
			}
		}
		prepared, err := ScoreWithQuerySquaredNorm(core.MetricCosine, a, SquaredNorm(a), c)
		if err != nil || math.Abs(float64(prepared-cosineWant)) > 1e-5 {
			t.Fatalf("prepared cosine dimension=%d got=%g want=%g err=%v", dimension, prepared, cosineWant, err)
		}
	}
}

func BenchmarkDot768(b *testing.B) {
	a, c := make([]float32, 768), make([]float32, 768)
	for i := range a {
		a[i], c[i] = float32(i%11), float32(i%7)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = Score(core.MetricDot, a, c)
	}
}

func BenchmarkL2Squared768(b *testing.B) {
	a, c := make([]float32, 768), make([]float32, 768)
	for i := range a {
		a[i], c[i] = float32(i%11), float32(i%7)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = Score(core.MetricL2, a, c)
	}
}

func BenchmarkCosine768(b *testing.B) {
	a, c := make([]float32, 768), make([]float32, 768)
	for i := range a {
		a[i], c[i] = float32(i%11), float32(i%7)
	}
	b.ReportAllocs()
	for b.Loop() {
		_, _ = Score(core.MetricCosine, a, c)
	}
}

func BenchmarkCosinePrepared768(b *testing.B) {
	a, c := make([]float32, 768), make([]float32, 768)
	for i := range a {
		a[i], c[i] = float32(i%11), float32(i%7)
	}
	norm := SquaredNorm(a)
	b.ReportAllocs()
	for b.Loop() {
		_, _ = ScoreWithQuerySquaredNorm(core.MetricCosine, a, norm, c)
	}
}
