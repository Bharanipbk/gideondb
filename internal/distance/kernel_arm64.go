//go:build !purego

package distance

// useAssembly is false under the purego build tag, which tests use to check
// the assembly against the portable kernels.
const useAssembly = true

//go:noescape
func dotNEON(a, b *float32, n int) float32

//go:noescape
func l2SquaredNEON(a, b *float32, n int) float32

// dot runs whole 16-float blocks through NEON and finishes the tail in Go.
func dot(a, b []float32) float32 {
	blocks := len(a) &^ 15
	if blocks == 0 {
		return dotGeneric(a, b)
	}
	b = b[:len(a)]
	sum := dotNEON(&a[0], &b[0], blocks)
	for i := blocks; i < len(a); i++ {
		sum += a[i] * b[i]
	}
	return sum
}

func l2Squared(a, b []float32) float32 {
	blocks := len(a) &^ 15
	if blocks == 0 {
		return l2SquaredGeneric(a, b)
	}
	b = b[:len(a)]
	sum := l2SquaredNEON(&a[0], &b[0], blocks)
	for i := blocks; i < len(a); i++ {
		d := a[i] - b[i]
		sum += d * d
	}
	return sum
}
