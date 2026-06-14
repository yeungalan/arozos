package main

import (
	"image/color"
	"math"
	"testing"
)

func TestFaceDetectorOnRealPhoto(t *testing.T) {
	det, err := newFaceDetector()
	if err != nil {
		t.Fatalf("newFaceDetector: %v", err)
	}

	img := loadFixture(t, "faces_sample.jpg")
	faces := det.detect(img)
	if len(faces) == 0 {
		t.Fatal("expected at least one face in faces_sample.jpg")
	}
	b := img.Bounds()
	for i, f := range faces {
		if f.Confidence <= 0 || f.Confidence > 1 {
			t.Errorf("face %d confidence %v out of (0,1]", i, f.Confidence)
		}
		if f.Box.Width <= 0 || f.Box.Height <= 0 {
			t.Errorf("face %d has empty box %+v", i, f.Box)
		}
		if f.Box.X < 0 || f.Box.Y < 0 || f.Box.X+f.Box.Width > b.Dx() || f.Box.Y+f.Box.Height > b.Dy() {
			t.Errorf("face %d box %+v escapes image bounds %v", i, f.Box, b)
		}
	}
}

func TestFaceDetectorNoFaceInSolidImage(t *testing.T) {
	det, err := newFaceDetector()
	if err != nil {
		t.Fatal(err)
	}
	if faces := det.detect(solidImage(200, 200, color.RGBA{120, 130, 140, 255})); len(faces) != 0 {
		t.Errorf("expected no faces in a solid image, got %d", len(faces))
	}
}

func TestDescribeFaceDeterministicAndNormalised(t *testing.T) {
	img := loadFixture(t, "person_a.jpg")
	box := Box{X: 50, Y: 50, Width: 240, Height: 240}

	d1 := describeFace(img, box)
	d2 := describeFace(img, box)

	//Expected dimensionality: 32*32 appearance + 4*4*8 gradient histogram.
	if len(d1) != faceDescriptorEdge*faceDescriptorEdge+4*4*8 {
		t.Fatalf("descriptor length = %d, want %d", len(d1), faceDescriptorEdge*faceDescriptorEdge+128)
	}
	//Deterministic.
	for i := range d1 {
		if d1[i] != d2[i] {
			t.Fatalf("descriptor not deterministic at %d: %v != %v", i, d1[i], d2[i])
		}
	}
	//Unit length.
	var sum float64
	for _, v := range d1 {
		sum += float64(v) * float64(v)
	}
	if math.Abs(math.Sqrt(sum)-1) > 1e-3 {
		t.Errorf("descriptor length = %v, want ~1", math.Sqrt(sum))
	}
}

func TestDescribeFaceLightingRobust(t *testing.T) {
	img := loadFixture(t, "person_a.jpg")
	box := Box{X: 50, Y: 50, Width: 240, Height: 240}

	base := describeFace(img, box)
	brighter := describeFace(adjustBrightness(img, 1.3), box)

	//Contrast normalisation should keep the same face similar under a global
	//brightness change.
	if sim := cosineSimilarity(base, brighter); sim < 0.9 {
		t.Errorf("brightness robustness: similarity %v, want >= 0.9", sim)
	}
}

func TestQualityToConfidenceMonotonic(t *testing.T) {
	if qualityToConfidence(5) >= qualityToConfidence(30) {
		t.Error("confidence should increase with quality")
	}
	if c := qualityToConfidence(0); c != 0 {
		t.Errorf("confidence at Q=0 = %v, want 0", c)
	}
}
