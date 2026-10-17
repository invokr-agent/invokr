// Package mathutil provides small, dependency-free numeric helpers shared
// across the codebase (score clamping and cosine similarity).
package mathutil

import "math"

// Clamp01 clamps value to the [0, 1] range.
func Clamp01(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

// ClampInt returns def when v <= 0, max when v > max, and v otherwise.
func ClampInt(v, def, limit int) int {
	if v <= 0 {
		return def
	}
	return min(v, limit)
}

// Norm returns the Euclidean norm of vector, or 0 when the vector is empty
// or all-zero.
func Norm(vector []float32) float64 {
	var sum float64
	for _, value := range vector {
		v := float64(value)
		sum += v * v
	}
	return math.Sqrt(sum)
}

// CosineWithNorms returns the cosine similarity of a and b given their
// precomputed norms. It returns 0 for length mismatches, empty vectors, or a
// zero norm.
func CosineWithNorms(a []float32, normA float64, b []float32, normB float64) float64 {
	if len(a) != len(b) || len(a) == 0 || normA == 0 || normB == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return dot / (normA * normB)
}
