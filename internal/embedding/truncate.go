package embedding

import (
	"fmt"
	"math"
)

// Truncate returns the first dim values of vector, L2-normalised: a Matryoshka-trained
// model's dim-wide embedding. It refuses a vector narrower than dim, and a prefix whose
// norm is zero or not finite.
func Truncate(vector []float32, dim int) ([]float32, error) {
	if len(vector) < dim {
		return nil, fmt.Errorf("vector has %d dimensions, want at least %d", len(vector), dim)
	}
	var sumSquares float64
	for _, v := range vector[:dim] {
		sumSquares += float64(v) * float64(v)
	}
	norm := math.Sqrt(sumSquares)
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return nil, fmt.Errorf("vector has norm %v, which cannot be normalised", norm)
	}
	truncated := make([]float32, dim)
	for i, v := range vector[:dim] {
		truncated[i] = float32(float64(v) / norm)
	}
	return truncated, nil
}
