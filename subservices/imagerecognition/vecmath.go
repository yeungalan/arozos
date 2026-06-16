package main

import "math"

/*
	vecmath.go

	Small vector helpers used by the face descriptor / people store.
*/

// l2Normalize scales v in place to unit Euclidean length. A zero vector is left
// unchanged.
func l2Normalize(v []float32) {
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	norm := math.Sqrt(sumSq)
	if norm == 0 {
		return
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
}

// cosineSimilarity returns the cosine of the angle between a and b (range
// -1..1). Mismatched-length or zero vectors yield 0.
func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// roundTo rounds f to the given number of decimal places.
func roundTo(f float64, places int) float64 {
	if places < 0 {
		return f
	}
	shift := math.Pow(10, float64(places))
	return math.Round(f*shift) / shift
}
