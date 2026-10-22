// Package distance implements portable vector distance kernels.
package distance

import (
	"fmt"
	"math"

	"github.com/Bharanipbk/gideondb/internal/core"
)

// Score returns a similarity score where larger is always better.
func Score(metric core.Metric, a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, core.ErrDimensionMismatch
	}
	switch metric {
	case core.MetricDot:
		return dot(a, b), nil
	case core.MetricL2:
		return -l2Squared(a, b), nil
	case core.MetricCosine:
		return cosine(a, b), nil
	default:
		return 0, fmt.Errorf("%w: unsupported metric %q", core.ErrInvalidArgument, metric)
	}
}

// SquaredNorm returns the squared L2 norm using the same kernel as the search
// paths, so cached norms match freshly computed ones exactly.
func SquaredNorm(vector []float32) float32 { return dot(vector, vector) }

// ScoreWithQuerySquaredNorm avoids recomputing the query norm for every
// candidate in a cosine scan. The supplied norm is ignored by other metrics.
func ScoreWithQuerySquaredNorm(metric core.Metric, query []float32, querySquaredNorm float32, vector []float32) (float32, error) {
	if len(query) != len(vector) {
		return 0, core.ErrDimensionMismatch
	}
	if metric != core.MetricCosine {
		return Score(metric, query, vector)
	}
	product, vectorSquaredNorm := cosineProductAndVectorNorm(query, vector)
	if querySquaredNorm == 0 || vectorSquaredNorm == 0 {
		return 0, nil
	}
	return product / float32(math.Sqrt(float64(querySquaredNorm*vectorSquaredNorm))), nil
}

// CosineWithSquaredNorms scores cosine similarity when both squared norms are
// already known, as in an index that caches them per vector. It matches
// ScoreWithQuerySquaredNorm exactly because both use the same reductions.
func CosineWithSquaredNorms(query []float32, querySquaredNorm float32, vector []float32, vectorSquaredNorm float32) float32 {
	if querySquaredNorm == 0 || vectorSquaredNorm == 0 {
		return 0
	}
	return dot(query, vector) / float32(math.Sqrt(float64(querySquaredNorm*vectorSquaredNorm)))
}

// cosineProductAndVectorNorm is built from dot so its results match
// SquaredNorm and CosineWithSquaredNorms bit for bit.
func cosineProductAndVectorNorm(query, vector []float32) (float32, float32) {
	return dot(query, vector), dot(vector, vector)
}

// dotGeneric is the portable kernel. Eight independent accumulators hide
// floating-point add latency, and reslicing to a fixed eight-element window
// lets the compiler drop bounds checks inside the loop.
func dotGeneric(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		x, y := a[i:i+8:i+8], b[i:i+8:i+8]
		s0 += x[0] * y[0]
		s1 += x[1] * y[1]
		s2 += x[2] * y[2]
		s3 += x[3] * y[3]
		s4 += x[4] * y[4]
		s5 += x[5] * y[5]
		s6 += x[6] * y[6]
		s7 += x[7] * y[7]
	}
	sum := ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7))
	for ; i < len(a); i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func l2SquaredGeneric(a, b []float32) float32 {
	b = b[:len(a)]
	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i+8 <= len(a); i += 8 {
		x, y := a[i:i+8:i+8], b[i:i+8:i+8]
		d0, d1, d2, d3 := x[0]-y[0], x[1]-y[1], x[2]-y[2], x[3]-y[3]
		d4, d5, d6, d7 := x[4]-y[4], x[5]-y[5], x[6]-y[6], x[7]-y[7]
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
		s4 += d4 * d4
		s5 += d5 * d5
		s6 += d6 * d6
		s7 += d7 * d7
	}
	sum := ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7))
	for ; i < len(a); i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}

func cosine(a, b []float32) float32 {
	product, aa, bb := dot(a, b), dot(a, a), dot(b, b)
	if aa == 0 || bb == 0 {
		return 0
	}
	return product / float32(math.Sqrt(float64(aa*bb)))
}
