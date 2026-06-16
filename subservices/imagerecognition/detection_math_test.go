package main

import (
	"image"
	"math"
	"testing"
)

func TestSigmoid(t *testing.T) {
	if got := sigmoid(0); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("sigmoid(0) = %v, want 0.5", got)
	}
	if sigmoid(10) <= 0.99 || sigmoid(-10) >= 0.01 {
		t.Errorf("sigmoid saturation wrong: +10=%v -10=%v", sigmoid(10), sigmoid(-10))
	}
}

func TestIoU(t *testing.T) {
	tests := []struct {
		name string
		a, b Box
		want float64
	}{
		{"identical", Box{0, 0, 10, 10}, Box{0, 0, 10, 10}, 1},
		{"disjoint", Box{0, 0, 10, 10}, Box{20, 20, 10, 10}, 0},
		{"half overlap", Box{0, 0, 10, 10}, Box{5, 0, 10, 10}, 1.0 / 3.0},
		{"touching edges", Box{0, 0, 10, 10}, Box{10, 0, 10, 10}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := iou(tc.a, tc.b)
			if math.Abs(got-tc.want) > 1e-6 {
				t.Errorf("iou = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNonMaxSuppression(t *testing.T) {
	dets := []ObjectDetection{
		{Label: "person", Confidence: 0.9, Box: Box{0, 0, 10, 10}},
		{Label: "person", Confidence: 0.8, Box: Box{1, 1, 10, 10}}, //overlaps the first
		{Label: "person", Confidence: 0.95, Box: Box{100, 100, 10, 10}},
		{Label: "car", Confidence: 0.7, Box: Box{0, 0, 10, 10}}, //different class, kept
	}
	kept := nonMaxSuppression(dets, 0.5)

	//The overlapping lower-confidence person must be dropped; the rest stay.
	if len(kept) != 3 {
		t.Fatalf("kept %d detections, want 3: %+v", len(kept), kept)
	}
	//Highest confidence detection must be first.
	if kept[0].Confidence != 0.95 {
		t.Errorf("first kept confidence = %v, want 0.95", kept[0].Confidence)
	}
	for _, d := range kept {
		if d.Label == "person" && d.Confidence == 0.8 {
			t.Errorf("overlapping low-confidence person was not suppressed")
		}
	}
}

func TestNonMaxSuppressionEmpty(t *testing.T) {
	if got := nonMaxSuppression(nil, 0.5); got != nil {
		t.Errorf("expected nil for empty input, got %+v", got)
	}
}

func TestLetterbox(t *testing.T) {
	//A 200x100 image letterboxed to 64 should keep aspect ratio (scale 0.32),
	//producing a 64x32 active region centred vertically with grey padding.
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	dst, scale, padX, padY := letterbox(src, 64)

	if dst.Bounds().Dx() != 64 || dst.Bounds().Dy() != 64 {
		t.Fatalf("letterbox size = %v, want 64x64", dst.Bounds())
	}
	if math.Abs(scale-0.32) > 1e-6 {
		t.Errorf("scale = %v, want 0.32", scale)
	}
	if padX != 0 {
		t.Errorf("padX = %d, want 0", padX)
	}
	if padY != 16 {
		t.Errorf("padY = %d, want 16", padY)
	}

	//Round-trip a box from letterboxed space back to source space.
	got := unletterboxBox(Box{X: 0, Y: 16, Width: 64, Height: 32}, scale, padX, padY, 200, 100)
	want := Box{X: 0, Y: 0, Width: 200, Height: 100}
	if got != want {
		t.Errorf("unletterboxBox = %+v, want %+v", got, want)
	}
}
