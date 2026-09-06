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

// SquaredNorm returns the squared L2 norm using the same four-lane reduction
// as the search kernels.
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

func cosineProductAndVectorNorm(query, vector []float32) (float32, float32) {
	var p0, p1, p2, p3, n0, n1, n2, n3 float32
	i := 0
	for ; i+3 < len(query); i += 4 {
		q0, q1, q2, q3 := query[i], query[i+1], query[i+2], query[i+3]
		v0, v1, v2, v3 := vector[i], vector[i+1], vector[i+2], vector[i+3]
		p0, p1, p2, p3 = p0+q0*v0, p1+q1*v1, p2+q2*v2, p3+q3*v3
		n0, n1, n2, n3 = n0+v0*v0, n1+v1*v1, n2+v2*v2, n3+v3*v3
	}
	product, norm := (p0+p1)+(p2+p3), (n0+n1)+(n2+n3)
	for ; i < len(query); i++ {
		product += query[i] * vector[i]
		norm += vector[i] * vector[i]
	}
	return product, norm
}

func dot(a, b []float32) float32 {
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

func l2Squared(a, b []float32) float32 {
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

func cosine(a, b []float32) float32 {
	var p0, p1, p2, p3 float32
	var aa0, aa1, aa2, aa3 float32
	var bb0, bb1, bb2, bb3 float32
	i := 0
	for ; i+3 < len(a); i += 4 {
		a0, a1, a2, a3 := a[i], a[i+1], a[i+2], a[i+3]
		b0, b1, b2, b3 := b[i], b[i+1], b[i+2], b[i+3]
		p0, p1, p2, p3 = p0+a0*b0, p1+a1*b1, p2+a2*b2, p3+a3*b3
		aa0, aa1, aa2, aa3 = aa0+a0*a0, aa1+a1*a1, aa2+a2*a2, aa3+a3*a3
		bb0, bb1, bb2, bb3 = bb0+b0*b0, bb1+b1*b1, bb2+b2*b2, bb3+b3*b3
	}
	product, aa, bb := (p0+p1)+(p2+p3), (aa0+aa1)+(aa2+aa3), (bb0+bb1)+(bb2+bb3)
	for ; i < len(a); i++ {
		product += a[i] * b[i]
		aa += a[i] * a[i]
		bb += b[i] * b[i]
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return product / float32(math.Sqrt(float64(aa*bb)))
}
