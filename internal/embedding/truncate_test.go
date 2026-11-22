package embedding

import (
	"math"
	"testing"
)

func TestTruncate(t *testing.T) {
	inf := float32(math.Inf(1))
	tests := []struct {
		name    string
		vector  []float32
		dim     int
		want    []float32
		wantErr bool
	}{
		{name: "keeps the first dims and renormalises them", vector: []float32{3, 4, 9, 9}, dim: 2, want: []float32{0.6, 0.8}},
		{name: "renormalises a vector already at the width", vector: []float32{0, 5, 0, 12}, dim: 4, want: []float32{0, 5.0 / 13, 0, 12.0 / 13}},
		{name: "fewer values than the width", vector: []float32{3, 4}, dim: 3, wantErr: true},
		{name: "a zero prefix", vector: []float32{0, 0, 1, 1}, dim: 2, wantErr: true},
		{name: "NaN", vector: []float32{1, float32(math.NaN()), 0}, dim: 2, wantErr: true},
		{name: "+Inf", vector: []float32{1, inf, 0}, dim: 2, wantErr: true},
		{name: "-Inf", vector: []float32{-inf, 1, 0}, dim: 2, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Truncate(tt.vector, tt.dim)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("Truncate(%v, %d) = %v, want an error", tt.vector, tt.dim, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Truncate: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Truncate(%v, %d) = %v, want %v", tt.vector, tt.dim, got, tt.want)
			}
			for i := range got {
				if math.Abs(float64(got[i]-tt.want[i])) > 1e-6 {
					t.Fatalf("Truncate(%v, %d) = %v, want %v", tt.vector, tt.dim, got, tt.want)
				}
			}
		})
	}
}
