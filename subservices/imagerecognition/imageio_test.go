package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestDecodeImage(t *testing.T) {
	orig := solidImage(8, 6, color.RGBA{10, 20, 30, 255})
	data := encodePNG(t, orig)

	got, err := decodeImage(data)
	if err != nil {
		t.Fatalf("decodeImage: %v", err)
	}
	if got.Bounds().Dx() != 8 || got.Bounds().Dy() != 6 {
		t.Errorf("decoded bounds = %v, want 8x6", got.Bounds())
	}

	if _, err := decodeImage([]byte("not an image")); err == nil {
		t.Error("expected error decoding garbage data")
	}
}

func TestResizeImage(t *testing.T) {
	src := solidImage(40, 20, color.RGBA{0, 128, 255, 255})
	dst := resizeImage(src, 10, 5)
	if dst.Bounds().Dx() != 10 || dst.Bounds().Dy() != 5 {
		t.Fatalf("resize bounds = %v, want 10x5", dst.Bounds())
	}
	//A solid image stays roughly the same colour after resizing.
	r, g, b, _ := dst.At(5, 2).RGBA()
	if r>>8 > 20 || abs(int(g>>8)-128) > 12 || abs(int(b>>8)-255) > 12 {
		t.Errorf("resized colour drifted: r=%d g=%d b=%d", r>>8, g>>8, b>>8)
	}
}

func TestCropImage(t *testing.T) {
	src := solidImage(20, 20, color.White)
	tests := []struct {
		name string
		box  Box
		w, h int
	}{
		{"interior", Box{5, 5, 8, 8}, 8, 8},
		{"clamped to bounds", Box{15, 15, 100, 100}, 5, 5},
		{"degenerate returns 1x1", Box{50, 50, 5, 5}, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := cropImage(src, tc.box)
			if c.Bounds().Dx() != tc.w || c.Bounds().Dy() != tc.h {
				t.Errorf("crop bounds = %v, want %dx%d", c.Bounds(), tc.w, tc.h)
			}
		})
	}
}

func TestGrayscalePixels(t *testing.T) {
	img := solidImage(4, 3, color.RGBA{255, 255, 255, 255})
	pix, w, h := grayscalePixels(img)
	if w != 4 || h != 3 {
		t.Fatalf("dims = %dx%d, want 4x3", w, h)
	}
	if len(pix) != 12 {
		t.Fatalf("len(pix) = %d, want 12", len(pix))
	}
	for i, p := range pix {
		if p < 250 {
			t.Errorf("pixel %d = %d, want ~255 for white", i, p)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
