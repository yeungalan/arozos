package facerecognition

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func TestCascadeUnpack(t *testing.T) {
	//The embedded facefinder cascade must always unpack: this guards the
	//binary asset against corruption.
	d := newDetector()
	classifier, err := d.classifierInstance()
	if err != nil {
		t.Fatalf("unable to unpack embedded cascade: %v", err)
	}
	if classifier == nil {
		t.Fatalf("classifierInstance returned nil classifier")
	}

	//A second call must reuse the same instance (sync.Once)
	again, err := d.classifierInstance()
	if err != nil || again != classifier {
		t.Errorf("classifierInstance not memoized")
	}
}

func TestDetectFacesOnBlankImage(t *testing.T) {
	tests := []struct {
		name string
		fill color.Gray
		w    int
		h    int
	}{
		{"black", color.Gray{Y: 0}, 320, 240},
		{"white", color.Gray{Y: 255}, 240, 320},
		{"gray", color.Gray{Y: 127}, 64, 64},
	}
	d := newDetector()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewGray(image.Rect(0, 0, tc.w, tc.h))
			for y := 0; y < tc.h; y++ {
				for x := 0; x < tc.w; x++ {
					img.SetGray(x, y, tc.fill)
				}
			}
			faces, err := d.DetectFaces(img, 40)
			if err != nil {
				t.Fatalf("DetectFaces returned error on blank image: %v", err)
			}
			if len(faces) != 0 {
				t.Errorf("found %d faces in a blank image, want 0", len(faces))
			}
		})
	}
}

func TestDetectFacesOnTinyImage(t *testing.T) {
	d := newDetector()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	faces, err := d.DetectFaces(img, 40)
	if err != nil {
		t.Fatalf("DetectFaces returned error on 1x1 image: %v", err)
	}
	if len(faces) != 0 {
		t.Errorf("found %d faces in a 1x1 image, want 0", len(faces))
	}
}

func TestDownscaleForDetection(t *testing.T) {
	tests := []struct {
		name        string
		w           int
		h           int
		wantScale   float64
		wantLongest int
	}{
		{"small stays untouched", 640, 480, 1.0, 640},
		{"large landscape is downscaled", 4000, 3000, 4000.0 / detectionMaxDimension, detectionMaxDimension},
		{"large portrait is downscaled", 3000, 4000, 4000.0 / detectionMaxDimension, detectionMaxDimension},
		{"boundary stays untouched", detectionMaxDimension, 100, 1.0, detectionMaxDimension},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img := image.NewGray(image.Rect(0, 0, tc.w, tc.h))
			scaled, scale := downscaleForDetection(img)
			if scale != tc.wantScale {
				t.Errorf("scale = %f, want %f", scale, tc.wantScale)
			}
			longest := scaled.Bounds().Dx()
			if scaled.Bounds().Dy() > longest {
				longest = scaled.Bounds().Dy()
			}
			if longest != tc.wantLongest {
				t.Errorf("longest side = %d, want %d", longest, tc.wantLongest)
			}
		})
	}
}

func TestSupportedImageExt(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".jpg", true},
		{"jpg", true},
		{".JPEG", true},
		{".png", true},
		{".webp", true},
		{".gif", false},
		{".arw", false},
		{".cr2", false},
		{".txt", false},
		{"", false},
	}
	for _, tc := range tests {
		t.Run("ext_"+tc.ext, func(t *testing.T) {
			if got := supportedImageExt(tc.ext); got != tc.want {
				t.Errorf("supportedImageExt(%q) = %v, want %v", tc.ext, got, tc.want)
			}
		})
	}
}

func TestDecodeImage(t *testing.T) {
	//Build one JPEG and one PNG in memory
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			src.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 80, A: 255})
		}
	}
	jpegBuffer := bytes.Buffer{}
	if err := jpeg.Encode(&jpegBuffer, src, nil); err != nil {
		t.Fatalf("unable to prepare test JPEG: %v", err)
	}
	pngBuffer := bytes.Buffer{}
	if err := png.Encode(&pngBuffer, src); err != nil {
		t.Fatalf("unable to prepare test PNG: %v", err)
	}

	tests := []struct {
		name    string
		data    []byte
		ext     string
		wantErr bool
	}{
		{"jpeg decodes", jpegBuffer.Bytes(), ".jpg", false},
		{"png decodes", pngBuffer.Bytes(), ".png", false},
		{"garbage fails", []byte("not an image at all"), ".jpg", true},
		{"empty fails", []byte{}, ".png", true},
		//A PNG with a webp extension must be routed to the webp decoder
		//(and fail there), proving the extension check is case-insensitive
		{"uppercase webp ext routes to webp decoder", pngBuffer.Bytes(), ".WEBP", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			img, err := DecodeImage(tc.data, tc.ext)
			if tc.wantErr {
				if err == nil {
					t.Errorf("DecodeImage expected error, got image %v", img.Bounds())
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeImage failed: %v", err)
			}
			if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 8 {
				t.Errorf("decoded bounds = %v, want 8x8", img.Bounds())
			}
		})
	}
}

func TestCropImage(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	src.Set(5, 5, color.NRGBA{R: 200, G: 10, B: 10, A: 255})

	crop := cropImage(src, image.Rect(4, 4, 8, 8))
	if crop.Bounds().Dx() != 4 || crop.Bounds().Dy() != 4 {
		t.Fatalf("crop bounds = %v, want 4x4", crop.Bounds())
	}
	r, _, _, _ := crop.At(1, 1).RGBA()
	if r>>8 != 200 {
		t.Errorf("crop did not copy the source pixel, red = %d, want 200", r>>8)
	}
}
