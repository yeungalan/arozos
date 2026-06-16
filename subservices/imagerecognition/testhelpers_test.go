package main

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
)

// loadFixture decodes a test image from the testdata directory.
func loadFixture(t *testing.T, name string) image.Image {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return img
}

// newRecognizerForTest builds a recognizer rooted at a throwaway directory so
// the people gallery never leaks between tests.
func newRecognizerForTest(t *testing.T) *Recognizer {
	t.Helper()
	lg := newSvcLogger("[test]")
	r, err := NewRecognizer(t.TempDir(), lg)
	if err != nil {
		t.Fatalf("NewRecognizer: %v", err)
	}
	t.Cleanup(r.Close)
	return r
}

// solidImage returns a w*h image filled with a single colour.
func solidImage(w, h int, c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

// adjustBrightness returns a copy of img with every channel scaled by factor.
func adjustBrightness(img image.Image, factor float64) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			out.Set(x, y, color.RGBA{
				R: clampByte(float64(r>>8) * factor),
				G: clampByte(float64(g>>8) * factor),
				B: clampByte(float64(bl>>8) * factor),
				A: uint8(a >> 8),
			})
		}
	}
	return out
}

func clampByte(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
