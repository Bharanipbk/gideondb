// Package quantization contains experimental vector codecs used to measure
// recall and memory tradeoffs before a format is admitted to persisted storage.
package quantization

import (
	"fmt"
	"math"

	"github.com/Bharanipbk/gideondb/internal/core"
)

// Int8Vector is a per-vector symmetric int8 representation. Values use
// [-127,127], leaving -128 unused so positive and negative ranges are balanced.
type Int8Vector struct {
	Values []int8
	Scale  float32
}

func QuantizeInt8(vector []float32) (Int8Vector, error) {
	maximum := float32(0)
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return Int8Vector{}, fmt.Errorf("%w: vector contains a non-finite value", core.ErrInvalidArgument)
		}
		maximum = max(maximum, float32(math.Abs(float64(value))))
	}
	result := Int8Vector{Values: make([]int8, len(vector))}
	if maximum == 0 {
		return result, nil
	}
	result.Scale = maximum / 127
	for position, value := range vector {
		quantized := math.Round(float64(value / result.Scale))
		quantized = math.Max(-127, math.Min(127, quantized))
		result.Values[position] = int8(quantized)
	}
	return result, nil
}

func (v Int8Vector) Dequantize() []float32 {
	result := make([]float32, len(v.Values))
	for position, value := range v.Values {
		result[position] = float32(value) * v.Scale
	}
	return result
}

// Score returns an approximate similarity score with the same larger-is-better
// convention as distance.Score.
func Score(metric core.Metric, query, vector Int8Vector) (float32, error) {
	if len(query.Values) != len(vector.Values) {
		return 0, core.ErrDimensionMismatch
	}
	var product, queryNorm, vectorNorm int64
	var squaredDistance float32
	for position, queryValue := range query.Values {
		q, v := int64(queryValue), int64(vector.Values[position])
		product += q * v
		queryNorm += q * q
		vectorNorm += v * v
		if metric == core.MetricL2 {
			difference := float32(q)*query.Scale - float32(v)*vector.Scale
			squaredDistance += difference * difference
		}
	}
	switch metric {
	case core.MetricDot:
		return float32(product) * query.Scale * vector.Scale, nil
	case core.MetricCosine:
		if queryNorm == 0 || vectorNorm == 0 {
			return 0, nil
		}
		return float32(float64(product) / math.Sqrt(float64(queryNorm*vectorNorm))), nil
	case core.MetricL2:
		return -squaredDistance, nil
	default:
		return 0, fmt.Errorf("%w: unsupported metric %q", core.ErrInvalidArgument, metric)
	}
}
