package facerecognition

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestCosineDistance(t *testing.T) {
	tests := []struct {
		name string
		a    []float32
		b    []float32
		want func(d float64) bool
	}{
		{"identical is zero", []float32{1, 0, 0}, []float32{1, 0, 0}, func(d float64) bool { return math.Abs(d) < 1e-6 }},
		{"orthogonal is one", []float32{1, 0}, []float32{0, 1}, func(d float64) bool { return math.Abs(d-1) < 1e-6 }},
		{"opposite is two", []float32{1, 0}, []float32{-1, 0}, func(d float64) bool { return math.Abs(d-2) < 1e-6 }},
		{"scale invariant", []float32{2, 0}, []float32{5, 0}, func(d float64) bool { return math.Abs(d) < 1e-6 }},
		{"length mismatch is max", []float32{1, 0}, []float32{1}, func(d float64) bool { return d == math.MaxFloat64 }},
		{"zero vector is max", []float32{0, 0}, []float32{1, 0}, func(d float64) bool { return d == math.MaxFloat64 }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cosineDistance(tc.a, tc.b); !tc.want(got) {
				t.Errorf("cosineDistance(%v,%v) = %f, unexpected", tc.a, tc.b, got)
			}
		})
	}

	//Symmetry
	a := []float32{0.2, 0.5, -0.1, 0.8}
	b := []float32{0.1, 0.4, 0.3, 0.7}
	if math.Abs(cosineDistance(a, b)-cosineDistance(b, a)) > 1e-9 {
		t.Errorf("cosineDistance is not symmetric")
	}
}

func TestL2Normalize(t *testing.T) {
	v := []float32{3, 4} //norm 5
	l2normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("l2normalize = %v, want [0.6 0.8]", v)
	}

	//Unit length after normalization
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("normalized vector length^2 = %f, want 1", sum)
	}

	//Zero vector is left unchanged (no NaN)
	zero := []float32{0, 0, 0}
	l2normalize(zero)
	for _, x := range zero {
		if x != 0 {
			t.Errorf("zero vector mutated by l2normalize: %v", zero)
		}
	}
}

func TestFaceToCHWTensor(t *testing.T) {
	const size = 8
	img := image.NewRGBA(image.Rect(0, 0, 20, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 20; x++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255}) //pure red
		}
	}

	data := faceToCHWTensor(img, size)
	if len(data) != 3*size*size {
		t.Fatalf("tensor length = %d, want %d", len(data), 3*size*size)
	}
	plane := size * size
	//Red plane should be (255-127.5)/128 ≈ 0.996; green/blue (0-127.5)/128 ≈ -0.996
	wantR := float32((255 - embedNormalizeMean) / embedNormalizeScale)
	wantGB := float32((0 - embedNormalizeMean) / embedNormalizeScale)
	for i := 0; i < plane; i++ {
		if math.Abs(float64(data[i]-wantR)) > 0.05 {
			t.Fatalf("red plane[%d] = %f, want ~%f", i, data[i], wantR)
		}
		if math.Abs(float64(data[plane+i]-wantGB)) > 0.05 {
			t.Fatalf("green plane[%d] = %f, want ~%f", i, data[plane+i], wantGB)
		}
		if math.Abs(float64(data[2*plane+i]-wantGB)) > 0.05 {
			t.Fatalf("blue plane[%d] = %f, want ~%f", i, data[2*plane+i], wantGB)
		}
	}
}

func TestCropFace(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 100, 100))

	tests := []struct {
		name              string
		x, y, w, h        int
		margin            float64
		wantW, wantH      int
		wantClampedToFull bool
	}{
		{"interior no margin", 40, 40, 20, 20, 0, 20, 20, false},
		{"interior with margin", 40, 40, 20, 20, 0.5, 40, 40, false},
		{"margin clamps at edges", 0, 0, 20, 20, 0.5, 30, 30, false}, //x0,y0 clamp to 0; x1,y1 = 30
		{"degenerate falls back to full", 0, 0, 0, 0, 0, 100, 100, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := cropFace(img, tc.x, tc.y, tc.w, tc.h, tc.margin)
			b := out.Bounds()
			if b.Dx() != tc.wantW || b.Dy() != tc.wantH {
				t.Errorf("crop size = %dx%d, want %dx%d", b.Dx(), b.Dy(), tc.wantW, tc.wantH)
			}
		})
	}
}

func TestClassicalSignatureStable(t *testing.T) {
	//The classical signature must be deterministic and encode the descriptor
	//version, so a descriptor change forces a re-scan.
	if classicalSignature() != classicalSignature() {
		t.Errorf("classicalSignature is not stable")
	}
	if classicalSignature() == "" {
		t.Errorf("classicalSignature must not be empty")
	}
}
