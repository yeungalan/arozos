package main

import (
	"math"
	"testing"
)

func TestL2Normalize(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want []float32
	}{
		{"unit vector unchanged", []float32{1, 0, 0}, []float32{1, 0, 0}},
		{"scales to unit length", []float32{3, 4}, []float32{0.6, 0.8}},
		{"zero vector untouched", []float32{0, 0}, []float32{0, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := append([]float32(nil), tc.in...)
			l2Normalize(v)
			for i := range v {
				if math.Abs(float64(v[i]-tc.want[i])) > 1e-6 {
					t.Errorf("index %d = %v, want %v", i, v[i], tc.want[i])
				}
			}
			//A normalised non-zero vector must have length 1.
			var sum float64
			for _, x := range v {
				sum += float64(x) * float64(x)
			}
			if tc.name != "zero vector untouched" && math.Abs(math.Sqrt(sum)-1) > 1e-6 {
				t.Errorf("length = %v, want 1", math.Sqrt(sum))
			}
		})
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"identical", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"orthogonal", []float32{1, 0}, []float32{0, 1}, 0},
		{"opposite", []float32{1, 0}, []float32{-1, 0}, -1},
		{"length mismatch", []float32{1, 0}, []float32{1}, 0},
		{"zero vector", []float32{0, 0}, []float32{1, 1}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cosineSimilarity(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("cosineSimilarity = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRoundTo(t *testing.T) {
	tests := []struct {
		f      float64
		places int
		want   float64
	}{
		{3.14159, 2, 3.14},
		{0.98765, 4, 0.9877},
		{2.5, 0, 3},
		{1.23, -1, 1.23},
	}
	for _, tc := range tests {
		if got := roundTo(tc.f, tc.places); got != tc.want {
			t.Errorf("roundTo(%v,%d) = %v, want %v", tc.f, tc.places, got, tc.want)
		}
	}
}
