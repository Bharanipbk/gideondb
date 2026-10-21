//go:build !arm64 || purego

package distance

const useAssembly = false

func dot(a, b []float32) float32       { return dotGeneric(a, b) }
func l2Squared(a, b []float32) float32 { return l2SquaredGeneric(a, b) }
